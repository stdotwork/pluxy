// Command quiet is a provider extension for the conformance kit's own test of its alive check.
// It streams GET <settings.url> through sdk.Upstream and passes on each "data: " line as text,
// and nothing for any other line.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

var client = &http.Client{Transport: sdk.Upstream(&http.Transport{})}

func main() {
	err := sdk.Serve(sdk.Extension{ID: os.Getenv(protocol.EnvExtensionID), Version: "0.0.0", Generate: generate})
	if err != nil {
		fmt.Fprintln(os.Stderr, "quiet:", err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	var s struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(call.Account.Settings, &s)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable"}
	}
	defer resp.Body.Close()
	if err := out.Start(llm.ResponseInfo{ID: "quiet", Model: call.Request.Model}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			if err := out.Text(item, d); err != nil {
				return err
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := out.EndItem(item); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}
