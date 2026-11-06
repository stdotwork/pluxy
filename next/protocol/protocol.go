// Package protocol holds the wire types and framing of the pluxy extension protocol. The
// protocol itself is described in README.md next to this file.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Version is the protocol version this package implements.
const Version = 0

// MaxFrame is the largest frame either side may send.
const MaxFrame = 64 << 20

// Environment variables core sets for an extension.
const (
	EnvProtocolVersions = "PLUXY_PROTOCOL_VERSIONS"
	EnvSocket           = "PLUXY_SOCKET"
	EnvExtensionID      = "PLUXY_EXTENSION_ID"
)

// HandshakeMarker starts the handshake line.
const HandshakeMarker = "pluxy"

// Operations.
const (
	OpDescribe = "describe"
	OpGenerate = "generate"
)

// Frame types that are not events.
const (
	TypeCall           = "call"
	TypeDescribeResult = "describe.result"
)

// Call is the single frame core sends on a connection.
type Call struct {
	Type    string       `json:"type"`
	ID      string       `json:"id"`
	Op      string       `json:"op"`
	Account *Account     `json:"account,omitempty"`
	Request *llm.Request `json:"request,omitempty"`
}

// Account is the account a call runs under.
type Account struct {
	ID         string          `json:"id"`
	Settings   json.RawMessage `json:"settings,omitempty"`
	Credential *Credential     `json:"credential,omitempty"`
}

// Credential kinds.
const CredentialAPIKey = "api_key"

// Credential is the secret a call uses.
type Credential struct {
	Kind   string `json:"kind"`
	APIKey string `json:"api_key,omitempty"`
}

// Info is the reply to describe.
type Info struct {
	ID         string   `json:"id"`
	Version    string   `json:"version"`
	Protocol   int      `json:"protocol"`
	Operations []string `json:"operations"`
}

// Reply is a frame an extension sends: an event, or the result of describe.
type Reply struct {
	llm.Event
	Info *Info `json:"info,omitempty"`
}

// ErrFrameTooLarge is returned for a frame longer than MaxFrame.
var ErrFrameTooLarge = errors.New("protocol: frame too large")

// WriteFrame writes v as one frame. It returns ErrFrameTooLarge, having written nothing, when v
// encodes to more than MaxFrame bytes.
func WriteFrame(w io.Writer, v any) error {
	// The header goes first in the same buffer, so the frame is encoded once and not copied.
	var buf bytes.Buffer
	buf.Write(make([]byte, 4))
	enc := json.NewEncoder(&buf)
	// No HTML escaping: it would turn each "<", ">" and "&" in the request or reply into six
	// bytes, and the frame goes to a program, not a web page.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b := buf.Bytes()
	b = b[:len(b)-1] // Encode ends the value with a newline
	n := len(b) - 4
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	binary.BigEndian.PutUint32(b, uint32(n))
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame into v. It returns io.EOF only when the stream ends cleanly between
// frames.
func ReadFrame(r io.Reader, v any) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return fmt.Errorf("protocol: truncated frame header: %w", err)
		}
		return err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > MaxFrame {
		return ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("protocol: truncated frame: %w", err)
	}
	return json.Unmarshal(body, v)
}

// HandshakeLine returns the line an extension prints once it listens.
func HandshakeLine(version int, socket string) string {
	return fmt.Sprintf("%s|%d|unix|%s\n", HandshakeMarker, version, socket)
}

// ParseHandshake parses a handshake line (without its newline).
func ParseHandshake(line string) (version int, network, address string, err error) {
	parts := strings.SplitN(strings.TrimRight(line, "\r\n"), "|", 4)
	if len(parts) != 4 || parts[0] != HandshakeMarker {
		return 0, "", "", fmt.Errorf("protocol: not a handshake line: %q", line)
	}
	version, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", "", fmt.Errorf("protocol: bad version in handshake line: %q", line)
	}
	if parts[2] != "unix" || parts[3] == "" {
		return 0, "", "", fmt.Errorf("protocol: unsupported network in handshake line: %q", line)
	}
	return version, parts[2], parts[3], nil
}

// ParseVersions parses PLUXY_PROTOCOL_VERSIONS.
func ParseVersions(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("protocol: bad version %q", f)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("protocol: no versions")
	}
	return out, nil
}
