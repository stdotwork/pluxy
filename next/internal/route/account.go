package route

import (
	"encoding/json"
	"errors"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Account is one upstream account core can route to. Its identity (ID) is what routing state
// is kept against; the credential it presents is separate, so credentials can change version
// without the account losing its cooldowns or its place in rotation.
type Account struct {
	ID string `json:"id"`
	// Models this account serves, each by its upstream name and optionally the name clients
	// use (ModelEntry). Empty means any model.
	Models []ModelEntry `json:"models,omitempty"`
	// ExcludedModels are patterns of model names the account does not serve (see excluded).
	ExcludedModels []string `json:"excluded_models,omitempty"`
	// ModelAlias renames models the account serves (see ModelAlias).
	ModelAlias []ModelAlias `json:"model_alias,omitempty"`
	// Prefix, when set, is a namespace for the account's models: clients name them
	// "<prefix>/<model>" (and also without it, unless routing forces prefixes).
	Prefix string `json:"prefix,omitempty"`
	// Priority: only the highest-priority accounts that are eligible are used; lower ones
	// take over while every higher one is blocked. Default 0.
	Priority int `json:"priority,omitempty"`
	// Weight for the weighted strategy. Absent means 1; 0 leaves the account out of weighted
	// routing.
	Weight *int `json:"weight,omitempty"`
	// DisableCooling, when set, overrides routing's DisableCooling for this account.
	DisableCooling *bool `json:"disable_cooling,omitempty"`
	// MaxConcurrency limits attempts running at once on this account. 0 means no limit.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// RequestRetry, when set, is how many rounds after the first this account takes part in,
	// in place of routing's RequestRetry (today's per-key request-retry): the account leaves a
	// request's rounds past its own count, and a count above routing's runs more rounds for it.
	RequestRetry *int `json:"request_retry,omitempty"`
	// Endpoint names the upstream endpoint this account calls. Accounts that name the same
	// endpoint share endpoint-scoped state (an outage of one endpoint blocks them all).
	// Default: the account's own ID.
	Endpoint string `json:"endpoint,omitempty"`
	// Settings are passed to the extension as configured.
	Settings json.RawMessage `json:"settings,omitempty"`
	// ProxyURL is the proxy the account's upstream traffic goes through (today's proxy-url): an
	// http, https, socks5 or socks5h URL, or direct or none for no proxy. Empty: the config's
	// proxy_url. Its user name and password are secret (package proxyurl).
	ProxyURL string `json:"proxy_url,omitempty"`
	// Credential is the secret the account presents.
	Credential *protocol.Credential `json:"credential,omitempty"`
	// Managed marks an account whose credential core's credential store keeps: each attempt
	// asks the router's Credentials for it instead of using Credential.
	Managed bool `json:"-"`
	// Extension is the ID of the extension that serves the account, set by core. Attempts on
	// the account go to that extension, and only its credentials are fetched for them.
	Extension string `json:"-"`

	// gen is the generation of the router that holds this copy (see State.Retain).
	gen uint64
	// rev is the account's revision in State when this copy was made: a copy of an older
	// revision is about the account as it was before SetAccounts changed it (see State.restart).
	rev uint64
}

// Check checks the account's routing and naming fields, as a config's accounts are checked.
func (a *Account) Check() error {
	if a.MaxConcurrency < 0 {
		return errors.New("max_concurrency must not be negative")
	}
	if a.RequestRetry != nil && *a.RequestRetry < 0 {
		return errors.New("request_retry must not be negative")
	}
	if err := proxyurl.Check("proxy_url", a.ProxyURL); err != nil {
		return err
	}
	return a.checkNames()
}

func (a *Account) weight() int {
	if a.Weight == nil {
		return 1
	}
	return max(*a.Weight, 0)
}

func (a *Account) endpoint() string {
	if a.Endpoint != "" {
		return a.Endpoint
	}
	return a.ID
}

// call returns the account as an extension call carries it.
func (a *Account) call() protocol.Account {
	return protocol.Account{ID: a.ID, Settings: a.Settings, Credential: a.Credential, ProxyURL: a.ProxyURL}
}
