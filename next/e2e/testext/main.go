// Command testext is a provider extension for core's lifecycle tests. It answers with the
// version in its package's manifest and its process ID, so a test can tell which generation and
// which process served a request. It calls no upstream.
//
// The requested model picks the behaviour:
//
//	"gated"  sends its first text, then waits until GET <settings.gate> answers, then finishes
//	"crash"  sends its first text, then exits with status 2
//	other    answers at once
//
// A file named crash-on-start in the package folder makes it exit before its handshake. Each
// start appends the process ID to the file starts in the package folder.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type settings struct {
	Gate string `json:"gate"`
}

func main() {
	// Core starts the program in its package folder.
	if _, err := os.Stat("crash-on-start"); err == nil {
		fmt.Fprintln(os.Stderr, "testext: crash-on-start is set")
		os.Exit(3)
	}
	var m struct {
		Version string `json:"version"`
	}
	if b, err := os.ReadFile("pluxy-extension.json"); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	f, err := os.OpenFile("starts", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
	}
	reply := fmt.Sprintf("v=%s pid=%d", m.Version, os.Getpid())
	err = sdk.Serve(sdk.Extension{
		ID:      os.Getenv(protocol.EnvExtensionID),
		Version: m.Version,
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			return generate(ctx, call, out, reply)
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testext:", err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream, reply string) error {
	var s settings
	_ = json.Unmarshal(call.Account.Settings, &s)
	if err := out.Start(llm.ResponseInfo{ID: "testext-" + strconv.Itoa(os.Getpid()), Model: call.Request.Model}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	if err := out.Text(item, reply); err != nil {
		return err
	}
	switch call.Request.Model {
	case "crash":
		os.Exit(2)
	case "gated":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Gate, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "gate: " + err.Error()}
		}
		resp.Body.Close()
	}
	if err := out.Text(item, " done"); err != nil {
		return err
	}
	if err := out.EndItem(item); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: "stop"}, nil)
}
