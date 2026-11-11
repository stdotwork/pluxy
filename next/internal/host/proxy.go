package host

import (
	"encoding/json"
	"fmt"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// proxyMinor is the protocol minor version that adds an account's proxy_url.
const proxyMinor = 11

// proxied turns account.ProxyURL, the account's own proxy_url setting, into the proxy the call
// carries: that setting, else Options.ProxyURL; none for direct or none. It refuses the call when
// it needs a proxy and the extension predates proxy_url, which would send it without one. The
// proxy's user name and password then count as a credential the extension holds, so its output
// is logged without them; the returned credential holds them for the call's own redaction.
func (e *Extension) proxied(account *protocol.Account) (*protocol.Credential, *llm.Error) {
	global := ""
	if e.opts.ProxyURL != nil {
		global = e.opts.ProxyURL()
	}
	account.ProxyURL = proxyurl.Resolve(account.ProxyURL, global)
	if account.ProxyURL == "" {
		return nil, nil
	}
	if e.info.ProtocolMinor < proxyMinor {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot send through a proxy (0.%d adds proxy_url); account %q has one, so it is not sent there",
				e.manifest.ID, e.info.ProtocolMinor, proxyMinor, account.ID)}
	}
	forms := proxyurl.Password(account.ProxyURL)
	if len(forms) == 0 {
		return nil, nil
	}
	data, _ := json.Marshal(forms)
	c := &protocol.Credential{Kind: "proxy", Data: data}
	e.held.addProxy(account.ID, c)
	return c, nil
}

// proxyForms returns the forms of the proxy secret that c, a credential made by proxied, holds.
func proxyForms(c *protocol.Credential) []string {
	var forms []string
	_ = json.Unmarshal(c.Data, &forms)
	return forms
}
