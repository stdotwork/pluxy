package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

const (
	museVersion = "1.4.14"
	museBuild   = "dec7d7497fecdad135ec5835a8161dbc92e55b58"
	museSurface = "tbh:exec"
)

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
