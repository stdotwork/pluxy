package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const statusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	token := sessionToken(call.Account.Credential)
	if token == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the account has no Devin session token", Scope: llm.ScopeCredential}
	}
	meta := message{}.str(1, cliName).str(2, cliVersion).str(3, token).str(4, "en").str(5, runtime.GOOS).
		str(7, cliVersion).str(12, cliName).str(31, deviceFingerprint(s.DeviceSeed))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+statusPath, bytes.NewReader(message{}.bytes(1, meta)))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	setHeaders(req.Header, token)
	req.Header.Set("Content-Type", "application/proto")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "Devin is unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "Devin's reply broke off", Scope: llm.ScopeEndpoint, Retryable: true}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, quotaFailure(resp.StatusCode, body, sdk.AccountSecrets(call.Account), resp.Header.Get("Retry-After"))
	}
	st, err := parseUserStatus(body)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Devin's user status reply is not a protobuf message", Scope: llm.ScopeEndpoint, Status: resp.StatusCode}
	}
	q := &protocol.Quota{Plan: st.plan, Limits: st.limits()}
	q.Raw, _ = json.Marshal(map[string]any{"user_status": st.raw()})
	return q, nil
}

func quotaFailure(status int, body []byte, secrets []string, retryAfterHeader string) *llm.Error {
	msg, keep := connectMessage(body, secrets)
	e := statusError(status, msg, keep, retryAfterHeader)
	switch {
	case e.Code == llm.CodeNotFound:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	case e.Code == llm.CodeQuota:
		e.Code = llm.CodeRateLimit
	}
	if e.Scope == llm.ScopeModel {
		e.Scope = llm.ScopeCredential
	}
	return e
}

type userStatus struct {
	plan          string
	planEnd       int64
	daily, weekly window
}

type window struct {
	remaining    uint64
	hasRemaining bool
	resetAt      int64
}

func parseUserStatus(b []byte) (*userStatus, error) {
	if len(b) == 0 {
		return nil, errBadMessage
	}
	top, err := parseFields(b)
	if err != nil {
		return nil, err
	}
	st := &userStatus{}
	for _, f := range top {
		if f.num != 1 || f.wire != wireBytes {
			continue
		}
		us, err := parseFields(f.data)
		if err != nil {
			return nil, err
		}
		for _, f := range us {
			if f.num != 13 || f.wire != wireBytes {
				continue
			}
			ps, err := parseFields(f.data)
			if err != nil {
				return nil, err
			}
			for _, f := range ps {
				switch {
				case f.num == 1 && f.wire == wireBytes:
					info, err := parseFields(f.data)
					if err != nil {
						return nil, err
					}
					for _, f := range info {
						if f.num == 2 && f.wire == wireBytes {
							st.plan = string(f.data)
						}
					}
				case f.num == 3 && f.wire == wireBytes:
					ts, err := parseFields(f.data)
					if err != nil {
						return nil, err
					}
					for _, f := range ts {
						if f.num == 1 && f.wire == wireVarint {
							st.planEnd = int64(f.val)
						}
					}
				case f.wire == wireVarint:
					switch f.num {
					case 14:
						st.daily.remaining, st.daily.hasRemaining = f.val, true
					case 15:
						st.weekly.remaining, st.weekly.hasRemaining = f.val, true
					case 17:
						st.daily.resetAt = int64(f.val)
					case 18:
						st.weekly.resetAt = int64(f.val)
					}
				}
			}
		}
	}
	return st, nil
}

func (st *userStatus) limits() []protocol.QuotaLimit {
	var out []protocol.QuotaLimit
	for _, w := range []struct {
		id, label string
		secs      int64
		win       window
	}{
		{"daily", "Daily limit", 86400, st.daily},
		{"weekly", "Weekly limit", 7 * 86400, st.weekly},
	} {
		if !w.win.hasRemaining && w.win.resetAt <= 0 {
			continue
		}
		l := protocol.QuotaLimit{ID: w.id, Label: w.label, WindowSeconds: w.secs}
		if w.win.hasRemaining && w.win.remaining <= 100 {
			used := float64(100 - w.win.remaining)
			l.UsedPercent = &used
		}
		if w.win.resetAt > 0 {
			l.ResetsAt = w.win.resetAt
		}
		out = append(out, l)
	}
	return out
}

func (st *userStatus) raw() map[string]any {
	plan := map[string]any{"plan_name": st.plan}
	if st.planEnd > 0 {
		plan["plan_end"] = st.planEnd
	}
	for _, w := range []struct {
		name string
		win  window
	}{{"daily", st.daily}, {"weekly", st.weekly}} {
		if w.win.hasRemaining {
			plan[w.name+"_quota_remaining_percent"] = w.win.remaining
		}
		if w.win.resetAt > 0 {
			plan[w.name+"_quota_reset_at_unix"] = w.win.resetAt
		}
	}
	return map[string]any{"plan_status": plan}
}
