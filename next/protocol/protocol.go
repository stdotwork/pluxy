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

// Version is the protocol version this package implements: the major version, which the
// handshake carries.
const Version = 0

// Minor is the minor version this package implements within Version. An extension reports it in
// describe (Info.ProtocolMinor); core uses what a minor version adds only with an extension that
// reports it. Minor 1 adds sessions.
const Minor = 1

// MaxFrame is the largest frame either side may send.
const MaxFrame = 64 << 20

// Environment variables core sets for an extension.
const (
	EnvProtocolVersions = "PLUXY_PROTOCOL_VERSIONS"
	EnvSocket           = "PLUXY_SOCKET"
	EnvExtensionID      = "PLUXY_EXTENSION_ID"
)

// EnvTestLoginHosts, set to 1 in core's environment, lets account settings move an extension's
// login and token endpoints to a test server. Only tests set it. Core passes it on to extensions
// when it is set; an extension honours such settings only when it holds it (sdk.TestLoginHost).
const EnvTestLoginHosts = "PLUXY_TEST_LOGIN_HOSTS"

// HandshakeMarker starts the handshake line.
const HandshakeMarker = "pluxy"

// Operations.
const (
	OpDescribe = "describe"
	OpGenerate = "generate"
	OpLogin    = "login"
	OpRefresh  = "refresh"
	// OpSession opens a session (minor version 1): one connection that carries turns, one at a
	// time, on one account.
	OpSession = "session"
)

// Frame types that are not events.
const (
	TypeCall           = "call"
	TypeDescribeResult = "describe.result"
	// TypeLoginInput is a frame core sends during a login call, after the call frame: what the
	// user entered for the extension's last prompt.
	TypeLoginInput = "login.input"
	// TypeLoginPrompt is a frame an extension sends during a login call: what to show the user.
	TypeLoginPrompt = "login.prompt"
	// TypeLoginDone ends a login call successfully, with the new credential.
	TypeLoginDone = "login.done"
	// TypeRefreshDone ends a refresh call successfully, with the renewed credential.
	TypeRefreshDone = "refresh.done"
	// TypeSessionReady is the extension's answer to a session call that it accepts.
	TypeSessionReady = "session.ready"
	// TypeSessionTurn is a frame core sends on a session: one request, answered with events.
	TypeSessionTurn = "session.turn"
	// TypeSessionInterrupt is a frame core sends on a session while a turn runs: the client's
	// control message for the running turn, in Raw.
	TypeSessionInterrupt = "session.interrupt"
)

// Session kinds (SessionInfo.Format).
const (
	// SessionOpenAIResponses is a Responses WebSocket session: turns are Responses requests and
	// may continue the session's previous response with previous_response_id.
	SessionOpenAIResponses = llm.FormatOpenAIResponses
)

// SessionInfo says what kind of session a session call opens.
type SessionInfo struct {
	Format string `json:"format"`
}

// Call is the first frame core sends on a connection. For every op but login it is the only
// one; a login call may be followed by login.input frames.
type Call struct {
	Type    string       `json:"type"`
	ID      string       `json:"id"`
	Op      string       `json:"op"`
	Account *Account     `json:"account,omitempty"`
	Request *llm.Request `json:"request,omitempty"`
	// Login is what a login call asks for.
	Login *Login `json:"login,omitempty"`
	// Input is what the user entered, in a login.input frame.
	Input map[string]string `json:"input,omitempty"`
	// Session is what a session call opens.
	Session *SessionInfo `json:"session,omitempty"`
	// Raw is the client's control message, in a session.interrupt frame.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Login is the subject of a login call.
type Login struct {
	// Method is one of the login methods the extension's describe lists.
	Method string `json:"method"`
	// Input holds what the user entered when starting the login, by the method's input names
	// (for an api_key login, the key).
	Input map[string]string `json:"input,omitempty"`
}

// Account is the account a call runs under.
type Account struct {
	ID         string          `json:"id"`
	Settings   json.RawMessage `json:"settings,omitempty"`
	Credential *Credential     `json:"credential,omitempty"`
}

// Credential kinds core knows. Extensions may use other kinds; core stores them as they are.
const (
	// CredentialAPIKey is a static key in APIKey. Core never refreshes it.
	CredentialAPIKey = "api_key"
	// CredentialOAuth is an OAuth grant: AccessToken, usually RefreshToken and ExpiresAt.
	CredentialOAuth = "oauth"
)

// Credential is the secret a call uses. Every string in it, except Kind, is treated as secret:
// core removes each from what a call sends back, so keep non-secret values out of it.
type Credential struct {
	Kind         string `json:"kind"`
	APIKey       string `json:"api_key,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is when AccessToken stops working, in Unix seconds; 0 means it does not
	// expire or the extension does not know.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// RefreshAt is when core should renew the credential, in Unix seconds. 0: 5 minutes
	// before ExpiresAt. Providers whose tokens are best renewed earlier (days ahead, say) set it.
	RefreshAt int64 `json:"refresh_at,omitempty"`
	// Data holds anything else the provider needs, as a JSON object (an ID token, a
	// service-account key, an account ID). Core stores and passes it on without reading it.
	Data json.RawMessage `json:"data,omitempty"`
}

// Info is the reply to describe.
type Info struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	// ProtocolMinor is the minor version of Protocol the extension implements; 0 when absent.
	ProtocolMinor int      `json:"protocol_minor,omitempty"`
	Operations    []string `json:"operations"`
	// LoginMethods are the ways login can add an account; present when Operations includes
	// login.
	LoginMethods []LoginMethod `json:"login_methods,omitempty"`
}

// Login method kinds.
const (
	// LoginAPIKey: the user enters a key; the extension checks it and returns a credential.
	LoginAPIKey = "api_key"
	// LoginBrowser: the user opens a URL and signs in; the extension gets the result on its own
	// callback or as a pasted redirect URL (OAuth authorization code with PKCE, RFC 7636).
	LoginBrowser = "browser"
	// LoginDevice: the user enters a code on another device while the extension polls (OAuth
	// device authorization grant, RFC 8628).
	LoginDevice = "device"
)

// LoginMethod is one way to log in.
type LoginMethod struct {
	// Method names it in a login call. Kind says how it works; an extension with two browser
	// logins (say, two account types) gives them different methods of the same kind.
	Method string `json:"method"`
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
	// Inputs are what the user enters when starting the login.
	Inputs []LoginInput `json:"inputs,omitempty"`
}

// LoginInput is one value the user enters.
type LoginInput struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Secret marks a value that must be hidden as it is typed and never shown again.
	Secret bool `json:"secret,omitempty"`
	// Optional marks a value the user may leave empty.
	Optional bool `json:"optional,omitempty"`
}

// Prompt kinds.
const (
	PromptBrowser = "browser"
	PromptDevice  = "device"
	PromptInput   = "input"
)

// Prompt is what a login asks the user to do.
type Prompt struct {
	// Kind is browser (open URL), device (open VerificationURI and enter UserCode) or input
	// (enter Inputs).
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
	// For device prompts (RFC 8628 section 3.2).
	UserCode                string `json:"user_code,omitempty"`
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	// ExpiresIn is how long the prompt stays valid, in seconds.
	ExpiresIn int64 `json:"expires_in,omitempty"`
	// Inputs the user may send back with login.input. A browser prompt usually accepts the
	// redirect URL, for when the extension's own callback cannot be reached.
	Inputs []LoginInput `json:"inputs,omitempty"`
}

// Grant is the result of a login or refresh.
type Grant struct {
	Credential *Credential `json:"credential"`
	// Label names the account for people, for example the e-mail address it belongs to. Not
	// secret; login only.
	Label string `json:"label,omitempty"`
}

// Reply is a frame an extension sends: an event, the result of describe, or a login or
// refresh frame.
type Reply struct {
	llm.Event
	Info   *Info   `json:"info,omitempty"`
	Prompt *Prompt `json:"prompt,omitempty"`
	Grant  *Grant  `json:"grant,omitempty"`
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
