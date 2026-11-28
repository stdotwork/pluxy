package route

import (
	"encoding/json"
	"errors"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Account struct {
	ID             string               `json:"id"`
	Models         []ModelEntry         `json:"models,omitempty"`
	ExcludedModels []string             `json:"excluded_models,omitempty"`
	ModelAlias     []ModelAlias         `json:"model_alias,omitempty"`
	Prefix         string               `json:"prefix,omitempty"`
	Priority       int                  `json:"priority,omitempty"`
	Weight         *int                 `json:"weight,omitempty"`
	DisableCooling *bool                `json:"disable_cooling,omitempty"`
	MaxConcurrency int                  `json:"max_concurrency,omitempty"`
	RequestRetry   *int                 `json:"request_retry,omitempty"`
	Endpoint       string               `json:"endpoint,omitempty"`
	Settings       json.RawMessage      `json:"settings,omitempty"`
	ProxyURL       string               `json:"proxy_url,omitempty"`
	Headers        map[string]string    `json:"headers,omitempty"`
	Timeouts       *protocol.Timeouts   `json:"timeouts,omitempty"`
	Credential     *protocol.Credential `json:"credential,omitempty"`
	Managed        bool                 `json:"-"`
	Extension      string               `json:"-"`
	Held           string               `json:"-"`

	gen uint64
	rev uint64
}

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
	if err := upheader.Check("headers", a.Headers); err != nil {
		return err
	}
	if err := ValidateTimeouts("timeouts", a.Timeouts); err != nil {
		return err
	}
	return a.CheckNames()
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

func (a *Account) call() protocol.Account {
	return protocol.Account{ID: a.ID, Settings: a.Settings, Credential: a.Credential, ProxyURL: a.ProxyURL, Headers: a.Headers, Timeouts: a.Timeouts}
}
