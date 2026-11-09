package configdoc

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// place returns where a setting is in the document: the JSON path in a JSON config; in a
// config.yaml the v8 path, or the older spelling when only that is there (today's core and
// this one read both, and the v8 one wins when both are set).
func (d *Doc) place(jsonPath, v8, old string) []string {
	switch {
	case !d.YAML:
		return strings.Split(jsonPath, ".")
	case old != "" && d.Get(strings.Split(v8, ".")...) == nil && d.Get(strings.Split(old, ".")...) != nil:
		return strings.Split(old, ".")
	default:
		return strings.Split(v8, ".")
	}
}

// clientKeys returns where the client keys are: client_keys in a JSON config; in a
// config.yaml access.api-keys, or the older layout's root api-keys list.
func (d *Doc) clientKeys() []string {
	if d.YAML && d.Get("access", "api-keys") == nil {
		if n := d.Get("api-keys"); n != nil && n.Kind == yaml.SequenceNode {
			return []string{"api-keys"}
		}
	}
	return d.place("client_keys", "access.api-keys", "")
}

// ClientKeys returns the client keys the document sets.
func (d *Doc) ClientKeys() ([]string, error) {
	n := d.Get(d.clientKeys()...)
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: %s is not a list", ErrInvalid, strings.Join(d.clientKeys(), "."))
	}
	var keys []string
	for _, c := range n.Content {
		if c.Kind == yaml.AliasNode && c.Alias != nil {
			c = c.Alias
		}
		if c.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%w: %s holds something other than keys", ErrInvalid, strings.Join(d.clientKeys(), "."))
		}
		keys = append(keys, c.Value)
	}
	return keys, nil
}

// AddClientKey adds a client key, written like the keys already there.
func (d *Doc) AddClientKey(key string) error {
	path := d.clientKeys()
	n := d.Get(path...)
	if isNull(n) {
		return d.Set(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{String(key)}}, path...)
	}
	if _, err := d.ClientKeys(); err != nil {
		return err
	}
	k := String(key)
	if len(n.Content) > 0 {
		k.Style = n.Content[len(n.Content)-1].Style &^ yaml.TaggedStyle
	}
	n.Content = append(n.Content, k)
	d.edited = true
	return nil
}

// RemoveClientKeys removes every client key for which drop is true, and returns how many keys
// are left.
func (d *Doc) RemoveClientKeys(drop func(key string) bool) (int, error) {
	if _, err := d.ClientKeys(); err != nil {
		return 0, err
	}
	n := d.Get(d.clientKeys()...)
	if isNull(n) {
		return 0, nil
	}
	kept := slices.DeleteFunc(slices.Clone(n.Content), func(c *yaml.Node) bool {
		if c.Kind == yaml.AliasNode && c.Alias != nil {
			return drop(c.Alias.Value)
		}
		return drop(c.Value)
	})
	if len(kept) != len(n.Content) {
		n.Content = kept
		d.edited = true
	}
	return len(kept), nil
}

// SetManagementKey sets the management key: management_key in a JSON config; in a
// config.yaml management.secret-key, or the older remote-management.secret-key when only that
// is there.
func (d *Doc) SetManagementKey(key string) error {
	return d.Set(String(key), d.place("management_key", "management.secret-key", "remote-management.secret-key")...)
}

// Strategies are the routing strategies by today's core's names, and the names it also takes
// for them; least-active is this core's own.
var strategies = map[string]string{
	"round-robin": "round-robin", "fill-first": "fill-first", "fillfirst": "fill-first", "ff": "fill-first",
	"weighted-round-robin": "weighted-round-robin", "weightedroundrobin": "weighted-round-robin",
	"wrr": "weighted-round-robin", "weighted": "weighted-round-robin", "least-active": "least-active",
}

// SettingsPatch is a change to the basic and routing settings, with today's core's names for
// them. A field left out stays as it is.
type SettingsPatch struct {
	Routing *RoutingPatch `json:"routing"`
}

// RoutingPatch is a change to the routing settings.
type RoutingPatch struct {
	// Strategy is round-robin, fill-first, weighted-round-robin or least-active.
	Strategy *string `json:"strategy"`
	// SessionAffinity sends requests of one session to the same account.
	SessionAffinity *bool `json:"session-affinity"`
	// SessionAffinityTTL is how long a session stays bound after its last request, such as
	// "1h".
	SessionAffinityTTL *string     `json:"session-affinity-ttl"`
	Retry              *RetryPatch `json:"retry"`
}

// RetryPatch is a change to the retry settings.
type RetryPatch struct {
	// MaxRetryCredentials is how many accounts one request may try; 0 is no limit.
	MaxRetryCredentials *int64 `json:"max-retry-credentials"`
	// MaxRetryInterval is the longest one request waits for a blocked account, in seconds.
	MaxRetryInterval *int64 `json:"max-retry-interval"`
}

// ApplySettings writes a settings change into the document, in the file's own names: a JSON
// config's, or in a config.yaml today's core's.
func (d *Doc) ApplySettings(p SettingsPatch) error {
	r := p.Routing
	if r == nil {
		return nil
	}
	if r.Strategy != nil {
		s, ok := strategies[strings.ToLower(strings.TrimSpace(*r.Strategy))]
		if !ok {
			return fmt.Errorf("%w: routing.strategy %q: use round-robin, fill-first, weighted-round-robin or least-active", ErrInvalid, *r.Strategy)
		}
		if !d.YAML && s == "weighted-round-robin" {
			s = "weighted"
		}
		if err := d.Set(String(s), "routing", "strategy"); err != nil {
			return err
		}
	}
	if r.SessionAffinity != nil {
		switch {
		case d.YAML:
			if err := d.Set(Bool(*r.SessionAffinity), "routing", "session-affinity"); err != nil {
				return err
			}
		case !*r.SessionAffinity:
			d.Delete("routing", "session_affinity")
		case isNull(d.Get("routing", "session_affinity")):
			if err := d.Set(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, "routing", "session_affinity"); err != nil {
				return err
			}
		}
	}
	if r.SessionAffinityTTL != nil {
		s := strings.TrimSpace(*r.SessionAffinityTTL)
		ttl, err := time.ParseDuration(s)
		if err != nil || ttl <= 0 {
			return fmt.Errorf("%w: routing.session-affinity-ttl %q is not a duration above zero, such as 1h or 30m", ErrInvalid, *r.SessionAffinityTTL)
		}
		if d.YAML {
			err = d.Set(String(s), "routing", "session-affinity-ttl")
		} else if isNull(d.Get("routing", "session_affinity")) {
			// In a JSON config the TTL is inside routing.session_affinity, whose presence
			// turns affinity on.
			err = fmt.Errorf("%w: routing.session-affinity-ttl needs routing.session-affinity on", ErrInvalid)
		} else {
			err = d.Set(Int(max(ttl, time.Second).Milliseconds()), "routing", "session_affinity", "ttl_ms")
		}
		if err != nil {
			return err
		}
	}
	if t := r.Retry; t != nil {
		if n := t.MaxRetryCredentials; n != nil {
			if *n < 0 {
				return fmt.Errorf("%w: routing.retry.max-retry-credentials must not be negative", ErrInvalid)
			}
			if err := d.Set(Int(*n), d.place("routing.max_accounts", "routing.retry.max-retry-credentials", "max-retry-credentials")...); err != nil {
				return err
			}
		}
		if n := t.MaxRetryInterval; n != nil {
			if *n < 0 || *n > 30*24*60*60 {
				return fmt.Errorf("%w: routing.retry.max-retry-interval must be between 0 and 2592000 seconds (30 days)", ErrInvalid)
			}
			v := *n
			if !d.YAML {
				v *= 1000
			}
			if err := d.Set(Int(v), d.place("routing.max_wait_ms", "routing.retry.max-retry-interval", "max-retry-interval")...); err != nil {
				return err
			}
		}
	}
	return nil
}
