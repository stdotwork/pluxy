package conformance

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestMain(m *testing.M) {
	switch os.Getenv("PLUXY_CONFORMANCE_TEST_MODE") {
	case "minor-23":
		serveFakeModels(23)
		return
	case "minor-24":
		serveFakeModels(24)
		return
	}
	os.Exit(m.Run())
}

func serveFakeModels(minor int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = ln.Close()
		os.Exit(0)
	}()
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			var call protocol.Call
			if protocol.ReadFrame(conn, &call) != nil {
				return
			}
			switch call.Op {
			case protocol.OpDescribe:
				_ = protocol.WriteFrame(conn, protocol.Reply{
					Event: llm.Event{Type: protocol.TypeDescribeResult},
					Info: &protocol.Info{
						ID:            os.Getenv(protocol.EnvExtensionID),
						Version:       "0.1.0",
						Protocol:      protocol.Version,
						ProtocolMinor: minor,
						Operations:    []string{protocol.OpGenerate, protocol.OpModels},
					},
				})
			case protocol.OpModels:
				_ = protocol.WriteFrame(conn, protocol.Reply{
					Event: llm.Event{Type: protocol.TypeModelsResult},
					Models: &protocol.ModelList{
						OwnedBy: "test",
						From:    protocol.ModelListFromAccount,
						Models: []protocol.Model{
							{
								ID:                  "m-cap",
								ContextLength:       272000,
								MaxCompletionTokens: 128000,
								Capabilities: &protocol.ModelCapabilities{
									Tools:            true,
									StructuredOutput: true,
									Vision:           true,
									PDF:              false,
									Reasoning:        true,
									ReasoningLevels:  []string{"low", "medium", "high"},
									DeprecatedAt:     1770000000,
								},
							},
						},
					},
				})
			}
		}()
	}
}

func fakeCapabilitiesManifest(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ext.sh")
	script := fmt.Sprintf("#!/bin/sh\nexport PLUXY_CONFORMANCE_TEST_MODE=%s\nexec %q\n", mode, self)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	manifest := fmt.Sprintf(`{"manifest_version":0,"id":"test/cap","version":"0.1.0","protocol":[0],"programs":{%q:"ext.sh"}}`, platform)
	mpath := filepath.Join(dir, host.ManifestFile)
	if err := os.WriteFile(mpath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return mpath
}

func TestConformanceModelCapabilities(t *testing.T) {
	if protocol.Minor < 24 {
		t.Errorf("protocol.Minor = %d, want >= 24", protocol.Minor)
	}

	// 1. Conformance validation of ModelCapabilities fields.
	valid := protocol.Model{
		ID: "good-model",
		Capabilities: &protocol.ModelCapabilities{
			Tools:            true,
			StructuredOutput: true,
			Vision:           true,
			PDF:              true,
			Reasoning:        true,
			ReasoningLevels:  []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
			DeprecatedAt:     1770000000,
		},
	}
	if err := checkModelCapabilities(valid); err != nil {
		t.Errorf("checkModelCapabilities(valid) = %v, want nil", err)
	}

	invalidCases := []struct {
		name string
		cap  *protocol.ModelCapabilities
	}{
		{
			name: "unknown_reasoning_level",
			cap:  &protocol.ModelCapabilities{Reasoning: true, ReasoningLevels: []string{"low", "turbo"}},
		},
		{
			name: "levels_without_reasoning",
			cap:  &protocol.ModelCapabilities{Reasoning: false, ReasoningLevels: []string{"high"}},
		},
		{
			name: "duplicate_reasoning_level",
			cap:  &protocol.ModelCapabilities{Reasoning: true, ReasoningLevels: []string{"high", "high"}},
		},
		{
			name: "negative_deprecated_at",
			cap:  &protocol.ModelCapabilities{DeprecatedAt: -1},
		},
	}
	for _, tc := range invalidCases {
		if err := checkModelCapabilities(protocol.Model{ID: tc.name, Capabilities: tc.cap}); err == nil {
			t.Errorf("checkModelCapabilities(%s) = nil, want error", tc.name)
		}
	}

	// 2. Wire round-trip and minor version 24 gating: minor 23 drops extension-sent capabilities,
	// minor 24 preserves them and passes checkModelCapabilities.
	for _, tc := range []struct {
		mode    string
		wantCap bool
	}{
		{"minor-23", false},
		{"minor-24", true},
	} {
		ext, err := host.Start(fakeCapabilitiesManifest(t, tc.mode), host.Options{Log: io.Discard})
		if err != nil {
			t.Fatalf("%s start: %v", tc.mode, err)
		}
		list, lerr := ext.Models(context.Background(), protocol.Account{ID: "a"})
		ext.Stop()
		if lerr != nil {
			t.Fatalf("%s Models: %v", tc.mode, lerr)
		}
		if len(list.Models) != 1 {
			t.Fatalf("%s len(Models) = %d, want 1", tc.mode, len(list.Models))
		}
		got := list.Models[0].Capabilities
		if !tc.wantCap {
			if got != nil {
				t.Errorf("%s: Capabilities = %+v, want nil below minor 24", tc.mode, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("%s: Capabilities = nil, want non-nil at minor 24", tc.mode)
		}
		if !got.Tools || !got.StructuredOutput || !got.Vision || got.PDF || !got.Reasoning ||
			!slices.Equal(got.ReasoningLevels, []string{"low", "medium", "high"}) || got.DeprecatedAt != 1770000000 {
			t.Errorf("%s: Capabilities = %+v", tc.mode, got)
		}
		if err := checkModelCapabilities(list.Models[0]); err != nil {
			t.Errorf("%s: checkModelCapabilities = %v", tc.mode, err)
		}
	}
}
