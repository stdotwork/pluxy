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
	c := &protocol.Credential{Kind: "upstream"}
	if secrets := proxyurl.Password(account.ProxyURL); len(secrets) > 0 {
		c.APIKey = secrets[0]
		if len(secrets) > 1 {
			c.AccessToken = secrets[1]
		}
	}
	if values := upheader.Secrets(account.Headers); len(values) > 0 {
		c.Data, _ = json.Marshal(map[string][]string{"headers": values})
	}
	if c.APIKey == "" && c.Data == nil {
		return nil, nil
	}
	e.held.add(account.ID+" upstream", c)
	return c, nil
}

// outboundSecrets returns the strings of c, a credential outbound returned, by name.
func outboundSecrets(c *protocol.Credential) map[string]string {
	out := map[string]string{"proxy": c.APIKey, "proxy password": c.AccessToken}
	var d struct{ Headers []string }
	_ = json.Unmarshal(c.Data, &d)
	for i, v := range d.Headers {
		out[fmt.Sprintf("header %d", i)] = v
	}
	return out
}
