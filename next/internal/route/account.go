package route

import (
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/protocol"
)

// Account is one upstream account core can route to. Its identity (ID) is what routing state
// is kept against; the credential it presents is separate, so credentials can change version
// without the account losing its cooldowns or its place in rotation.
type Account struct {
	ID string `json:"id"`
	// Models this account serves. Empty means any model.
	Models []string `json:"models,omitempty"`
	// Priority: only the highest-priority accounts that are eligible are used; lower ones
	// take over while every higher one is blocked. Default 0.
	Priority int `json:"priority,omitempty"`
	// Weight for the weighted strategy. Absent means 1; 0 leaves the account out of weighted
	// routing.
	Weight *int `json:"weight,omitempty"`
	// MaxConcurrency limits attempts running at once on this account. 0 means no limit.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// Endpoint names the upstream endpoint this account calls. Accounts that name the same
	// endpoint share endpoint-scoped state (an outage of one endpoint blocks them all).
	// Default: the account's own ID.
	Endpoint string `json:"endpoint,omitempty"`
	// Settings are passed to the extension as configured.
	Settings json.RawMessage `json:"settings,omitempty"`
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
}

func (a *Account) serves(model string) bool {
	return len(a.Models) == 0 || slices.Contains(a.Models, model)
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
	return protocol.Account{ID: a.ID, Settings: a.Settings, Credential: a.Credential}
}
