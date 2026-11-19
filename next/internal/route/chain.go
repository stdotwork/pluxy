package route

import "context"

// Step is one step of a fallback chain.
type Step struct {
	Model    string   `json:"model"`
	Provider string   `json:"provider,omitempty"`
	Accounts []string `json:"accounts,omitempty"`
}

// MaxChainSteps is the most steps a chain has.
const MaxChainSteps = 8

// CheckSteps is a stub.
func (r *Router) CheckSteps(ctx context.Context, steps []Step) error { return nil }
