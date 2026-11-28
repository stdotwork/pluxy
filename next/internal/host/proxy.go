package host

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const proxyMinor = 11

const headersMinor = 12

const timeoutsMinor = 22

const (
	defaultHeaderTimeoutMS int64 = 60_000
	defaultIdleTimeoutMS   int64 = 300_000
	defaultTotalTimeoutMS  int64 = 600_000
)

func (e *Extension) outbound(account *protocol.Account, client http.Header) (*protocol.Credential, *llm.Error) {
	global := ""
	if e.opts.ProxyURL != nil {
		global = e.opts.ProxyURL()
	}
	account.ProxyURL = proxyurl.Resolve(account.ProxyURL, global)
	if account.ProxyURL != "" && e.info.ProtocolMinor < proxyMinor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot send through a proxy (0.%d adds proxy_url); the account has one, so it is not sent there",
				e.manifest.ID, e.info.ProtocolMinor, proxyMinor)}
	}
	copies := upheader.Copies(account.Headers)
	configured := account.Headers
	account.Headers = upheader.Fill(account.Headers, client)
	if len(account.Headers) > 0 && e.info.ProtocolMinor < headersMinor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot send an account's headers (0.%d adds headers); the account has some, so it is not sent there",
				e.manifest.ID, e.info.ProtocolMinor, headersMinor)}
	}
	call := outboundForms{Proxy: proxyurl.Password(account.ProxyURL), Headers: upheader.Forms(account.Headers)}
	held := call
	if copies {
		held.Headers = upheader.Forms(upheader.Fill(configured, nil))
	}
	e.held.addOutbound(account.ID, held.credential())
	return call.credential(), nil
}

type outboundForms struct {
	Proxy   []string `json:"proxy,omitempty"`
	Headers []string `json:"headers,omitempty"`
}

func (d outboundForms) credential() *protocol.Credential {
	if len(d.Proxy) == 0 && len(d.Headers) == 0 {
		return nil
	}
	data, _ := json.Marshal(d)
	return &protocol.Credential{Kind: "upstream", Data: data}
}

func outboundSecrets(c *protocol.Credential) map[string]string {
	var d outboundForms
	_ = json.Unmarshal(c.Data, &d)
	out := map[string]string{}
	for i, f := range d.Proxy {
		out[fmt.Sprintf("proxy %d", i)] = f
	}
	for i, f := range d.Headers {
		out[fmt.Sprintf("header %d", i)] = f
	}
	return out
}

func (e *Extension) resolveTimeouts(account *protocol.Account) {
	if e.info.ProtocolMinor < timeoutsMinor {
		account.Timeouts = nil
		return
	}
	var global *protocol.Timeouts
	if e.opts.Timeouts != nil {
		global = e.opts.Timeouts()
	}
	if global == nil {
		global = &protocol.Timeouts{}
	}
	own := account.Timeouts
	if own == nil {
		own = &protocol.Timeouts{}
	}
	pick := func(a, g *int64, def int64) *int64 {
		v := def
		if g != nil {
			v = *g
		}
		if a != nil {
			v = *a
		}
		return &v
	}
	account.Timeouts = &protocol.Timeouts{
		HeaderMS: pick(own.HeaderMS, global.HeaderMS, defaultHeaderTimeoutMS),
		IdleMS:   pick(own.IdleMS, global.IdleMS, defaultIdleTimeoutMS),
		TotalMS:  pick(own.TotalMS, global.TotalMS, defaultTotalTimeoutMS),
	}
}
