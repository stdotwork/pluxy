package catalogue

import (
	"context"
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

const (
	DefaultURL      = "https://models.dev/api.json"
	DefaultTimeout  = 10 * time.Second
	RefreshInterval = 24 * time.Hour
)

type Options struct {
	StatePath string
	URL       string
	Client    *http.Client
	Timeout   time.Duration
}

type Catalogue struct {
	timeout time.Duration
}

func New(opts Options) *Catalogue {
	t := opts.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	return &Catalogue{timeout: t}
}

func (c *Catalogue) Lookup(_ string) (protocol.Model, bool) {
	return protocol.Model{}, false
}

func (c *Catalogue) Refresh(_ context.Context) error {
	return nil
}
