package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

// Muse Code's client identity on api.meta.ai, as today's core sends it: captured from Muse Code
// 1.4.14 (build dec7d749...). Meta's API serves a Muse Code subscription's key only to Muse Code,
// so requests name it, as pluxy's Codex and Kimi extensions name their apps.
const (
	museVersion = "1.4.14"
	museBuild   = "dec7d7497fecdad135ec5835a8161dbc92e55b58"
	museSurface = "tbh:exec"
)

// userAgent is Muse Code's User-Agent for this platform.
func userAgent() string {
	goos, arch := runtime.GOOS, runtime.GOARCH
	if goos == "darwin" {
		goos = "macos"
	}
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return fmt.Sprintf("muse-build/%s (non-interactive; %s-%s; build %s)", museVersion, goos, arch, museBuild)
}

// clientIdentity sets Muse Code's headers on a request to Meta's API. Each request gets a session
// of its own, so requests of unrelated clients never share one Muse session.
func clientIdentity(h http.Header) {
	h.Set("User-Agent", userAgent())
	h.Set("X-Client-Id", museSurface)
	session := newUUIDv7()
	h.Set("X-Tbh-Session-Id", session)
	h.Set("X-Meta-Ai-Gateway-Session-Id", session)
	h.Set("X-Tbh-Thread-Id", session)
	h.Set("X-Tbh-Root-Session-Id", session)
	h.Set("X-Tbh-Agent-Role", "main")
	h.Set("X-Tbh-Contributor-Mode", "1")
}

// newUUIDv7 returns a time-ordered random UUID (RFC 9562 version 7), as Muse Code names sessions.
func newUUIDv7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[0:6], ts[2:8])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
