package host

import (
	"encoding/json"
	"fmt"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// proxyMinor is the protocol minor version that adds an account's proxy_url.
const proxyMinor = 11

// headersMinor is the protocol minor version that adds an account's headers.
const headersMinor = 12

// outbound turns account.ProxyURL, the account's own proxy_url setting, into the proxy the call
// carries: that setting, else Options.ProxyURL; none for direct or none. It refuses the call when
// it needs a proxy or headers and the extension predates them, which would send it without. The
// proxy's user name and password and the account's secret header values then count as a
// credential the extension holds, so its output is logged without them; the returned credential
// holds them for the call's own redaction.
func (e *Extension) outbound(account *protocol.Account) (*protocol.Credential, *llm.Error) {
	global := ""
	if e.opts.ProxyURL != nil {
		global = e.opts.ProxyURL()
	}
	account.ProxyURL = proxyurl.Resolve(account.ProxyURL, global)
	if account.ProxyURL != "" && e.info.ProtocolMinor < proxyMinor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot send through a proxy (0.%d adds proxy_url); account %q has one, so it is not sent there",
				e.manifest.ID, e.info.ProtocolMinor, proxyMinor, account.ID)}
	}
	if len(account.Headers) > 0 && e.info.ProtocolMinor < headersMinor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot send an account's headers (0.%d adds headers); account %q has some, so it is not sent there",
				e.manifest.ID, e.info.ProtocolMinor, headersMinor, account.ID)}
	}
	var d outboundForms
	d.Proxy = proxyurl.Password(account.ProxyURL)
	d.Headers = upheader.Forms(account.Headers)
	if len(d.Proxy) == 0 && len(d.Headers) == 0 {
		return nil, nil
	}
	data, _ := json.Marshal(d)
	c := &protocol.Credential{Kind: "upstream", Data: data}
	e.held.addOutbound(account.ID, c)
	return c, nil
}

// outboundForms are the forms of an account's proxy secret and secret header values that are
// removed from text, as proxyurl.Password and upheader.Forms return them.
type outboundForms struct {
	Proxy   []string `json:"proxy,omitempty"`
	Headers []string `json:"headers,omitempty"`
}

// outboundSecrets returns the forms that c, a credential outbound returned, holds, by name.
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
