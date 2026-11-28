package configdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/ignite"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

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

func (d *Doc) clientKeys() []string {
	if d.YAML && d.Get("access", "api-keys") == nil {
		if n := d.Get("api-keys"); n != nil && n.Kind == yaml.SequenceNode {
			return []string{"api-keys"}
		}
	}
	return d.place("client_keys", "access.api-keys", "")
}

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

var clientKeyLabels = []string{"client_key_labels"}

var ErrLabelIndirect = errors.New("this key's label is set through an alias or merge in config.yaml, so it cannot be changed here: edit config.yaml")

func (d *Doc) labelsInEffect() (map[string]string, error) {
	var v struct {
		Labels map[string]string `yaml:"client_key_labels"`
	}
	if err := d.root.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: client_key_labels: %v", ErrInvalid, err)
	}
	return v.Labels, nil
}

func (d *Doc) RemoveClientKeyLabel(id string) {
	d.Delete(append(slices.Clone(clientKeyLabels), id)...)
	if m := d.Get(clientKeyLabels...); m != nil && m.Kind == yaml.MappingNode && len(m.Content) == 0 {
		d.Delete(clientKeyLabels...)
	}
}

func (d *Doc) SetClientKeyLabel(id, label string) error {
	labels := d.Get(clientKeyLabels...)
	if labels != nil && labels.Kind == yaml.AliasNode {
		return ErrLabelIndirect
	}
	before, err := d.labelsInEffect()
	if err != nil {
		return err
	}
	if own := child(labels, id); (own != nil && own.Kind == yaml.AliasNode) || (own == nil && before[id] != "") {
		return ErrLabelIndirect
	}
	if label != "" {
		if err := d.Set(String(label), append(slices.Clone(clientKeyLabels), id)...); err != nil {
			return err
		}
	} else {
		d.RemoveClientKeyLabel(id)
	}
	want := maps.Clone(before)
	if label == "" {
		delete(want, id)
	} else {
		if want == nil {
			want = map[string]string{}
		}
		want[id] = label
	}
	b, err := d.encode()
	if err != nil {
		return err
	}
	written, err := Parse(b, d.YAML)
	if err != nil {
		return ErrLabelIndirect
	}
	after, err := written.labelsInEffect()
	if err != nil {
		return err
	}
	if !maps.Equal(after, want) {
		return ErrLabelIndirect
	}
	return nil
}

func (d *Doc) SetManagementKey(key string) error {
	return d.Set(String(key), d.place("management_key", "management.secret-key", "remote-management.secret-key")...)
}

var strategies = map[string]string{
	"round-robin": "round-robin", "fill-first": "fill-first", "fillfirst": "fill-first", "ff": "fill-first",
	"weighted-round-robin": "weighted-round-robin", "weightedroundrobin": "weighted-round-robin",
	"wrr": "weighted-round-robin", "weighted": "weighted-round-robin", "least-active": "least-active",
	"soonest-reset": "soonest-reset",
}

type SettingsPatch struct {
	Routing               *RoutingPatch       `json:"routing"`
	Observability         *ObservabilityPatch `json:"observability"`
	TokenCountEstimate    *bool               `json:"token_count_estimate"`
	ProxyURL              *string             `json:"proxy-url"`
	ManagementAllowRemote *bool               `json:"management_allow_remote"`
	Listen                *string             `json:"listen"`
	Payload               json.RawMessage     `json:"payload"`
}

type ObservabilityPatch struct {
	Logs  *LogsPatch  `json:"logs"`
	Usage *UsagePatch `json:"usage"`
}

type LogsPatch struct {
	LoggingToFile      *bool  `json:"logging-to-file"`
	LogsMaxTotalSizeMB *int64 `json:"logs-max-total-size-mb"`
	RequestLog         *bool  `json:"request-log"`
	ErrorLogsMaxFiles  *int64 `json:"error-logs-max-files"`
}

type UsagePatch struct {
	UsageStatisticsEnabled *bool `json:"usage-statistics-enabled"`
}

type RoutingPatch struct {
	Strategy           *string         `json:"strategy"`
	SessionAffinity    *bool           `json:"session-affinity"`
	SessionAffinityTTL *string         `json:"session-affinity-ttl"`
	ForceModelPrefix   *bool           `json:"force-model-prefix"`
	Retry              *RetryPatch     `json:"retry"`
	Cooldown           *CooldownPatch  `json:"cooldown"`
	Timeouts           json.RawMessage `json:"timeouts"`
	Ignition           json.RawMessage `json:"ignition"`
}

type CooldownPatch struct {
	DisableCooling                *bool   `json:"disable-cooling"`
	TransientErrorCooldownSeconds *int64  `json:"transient-error-cooldown-seconds"`
	QuotaResponseMode             *string `json:"quota-response-mode"`
}

type RetryPatch struct {
	RequestRetry        *int64 `json:"request-retry"`
	MaxRetryCredentials *int64 `json:"max-retry-credentials"`
	MaxRetryInterval    *int64 `json:"max-retry-interval"`
}

func (d *Doc) ApplySettings(p SettingsPatch) error {
	if err := d.applyRouting(p.Routing); err != nil {
		return err
	}
	if p.TokenCountEstimate != nil {
		if err := d.Set(Bool(*p.TokenCountEstimate), d.place("token_count_estimate", "token_count_estimate", "")...); err != nil {
			return err
		}
	}
	if p.ProxyURL != nil {
		if err := d.applyProxyURL(*p.ProxyURL); err != nil {
			return err
		}
	}
	if p.ManagementAllowRemote != nil {
		if err := d.Set(Bool(*p.ManagementAllowRemote), d.place("management_allow_remote", "management.allow-remote", "remote-management.allow-remote")...); err != nil {
			return err
		}
	}
	if p.Listen != nil {
		if err := d.applyListen(*p.Listen); err != nil {
			return err
		}
	}
	if p.Payload != nil {
		if err := d.applyPayload(p.Payload); err != nil {
			return err
		}
	}
	return d.applyObservability(p.Observability)
}

func CheckListen(s string) (host string, port uint16, err error) {
	s = strings.TrimSpace(s)
	host, portText, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, fmt.Errorf("%w: listen %q must be an address and a port, such as 127.0.0.1:8317 or 0.0.0.0:8317", ErrInvalid, s)
	}
	n, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("%w: listen %q: the port must be a number from 0 to 65535", ErrInvalid, s)
	}
	return host, uint16(n), nil
}

func (d *Doc) applyListen(s string) error {
	host, port, err := CheckListen(s)
	if err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if !d.YAML {
		return d.Set(String(s), "listen")
	}
	if err := d.Set(String(host), d.place("", "server.host", "host")...); err != nil {
		return err
	}
	return d.Set(Int(int64(port)), d.place("", "server.port", "port")...)
}

func (d *Doc) applyPayload(raw json.RawMessage) error {
	var r *payload.Rules
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		return fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	if err := payload.Validate(r); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if r == nil || (len(r.Default) == 0 && len(r.DefaultRaw) == 0 && len(r.Override) == 0 && len(r.OverrideRaw) == 0 && len(r.Filter) == 0) {
		if d.YAML {
			d.Delete("requests", "payload")
			d.Delete("payload")
		} else {
			d.Delete("payload")
		}
		return nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil || n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		return fmt.Errorf("%w: payload: %v", ErrInvalid, err)
	}
	blockStyle(n.Content[0])
	return d.Set(n.Content[0], d.place("payload", "requests.payload", "payload")...)
}

func blockStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		blockStyle(c)
	}
}

func (d *Doc) applyProxyURL(s string) error {
	s = strings.TrimSpace(s)
	if err := proxyurl.Check("proxy-url", s); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if s == "" {
		if d.YAML {
			d.Delete("requests", "proxy-url")
			d.Delete("proxy-url")
		} else {
			d.Delete("proxy_url")
		}
		return nil
	}
	return d.Set(String(s), d.place("proxy_url", "requests.proxy-url", "proxy-url")...)
}

func (d *Doc) applyObservability(o *ObservabilityPatch) error {
	if o == nil {
		return nil
	}
	if l := o.Logs; l != nil {
		if l.LoggingToFile != nil {
			if err := d.Set(Bool(*l.LoggingToFile), d.place("logs.to_file", "observability.logs.logging-to-file", "logging-to-file")...); err != nil {
				return err
			}
		}
		if n := l.LogsMaxTotalSizeMB; n != nil {
			if *n < 0 || *n > 1<<20 {
				return fmt.Errorf("%w: observability.logs.logs-max-total-size-mb must be between 0 (no cap) and 1048576", ErrInvalid)
			}
			if err := d.Set(Int(*n), d.place("logs.max_total_size_mb", "observability.logs.logs-max-total-size-mb", "logs-max-total-size-mb")...); err != nil {
				return err
			}
		}
		if l.RequestLog != nil {
			if err := d.Set(Bool(*l.RequestLog), d.place("logs.request_log", "observability.logs.request-log", "request-log")...); err != nil {
				return err
			}
		}
		if n := l.ErrorLogsMaxFiles; n != nil {
			if *n < 0 || *n > 1000000 {
				return fmt.Errorf("%w: observability.logs.error-logs-max-files must be between 0 (keep all) and 1000000", ErrInvalid)
			}
			if err := d.Set(Int(*n), d.place("logs.error_logs_max_files", "observability.logs.error-logs-max-files", "error-logs-max-files")...); err != nil {
				return err
			}
		}
	}
	if u := o.Usage; u != nil && u.UsageStatisticsEnabled != nil {
		if err := d.Set(Bool(*u.UsageStatisticsEnabled), d.place("usage.enabled", "observability.usage.usage-statistics-enabled", "usage-statistics-enabled")...); err != nil {
			return err
		}
	}
	return nil
}

func (d *Doc) applyRouting(r *RoutingPatch) error {
	if r == nil {
		return nil
	}
	if r.Strategy != nil {
		s, ok := strategies[strings.ToLower(strings.TrimSpace(*r.Strategy))]
		if !ok {
			return fmt.Errorf("%w: routing.strategy %q: use round-robin, fill-first, weighted-round-robin, least-active or soonest-reset", ErrInvalid, *r.Strategy)
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
		default:
			if err := d.Set(Bool(*r.SessionAffinity), "routing", "session_affinity", "enabled"); err != nil {
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
		} else if en := d.Get("routing", "session_affinity", "enabled"); en != nil && en.Kind == yaml.ScalarNode && en.Value == "false" {
			err = fmt.Errorf("%w: routing.session-affinity-ttl needs routing.session-affinity on", ErrInvalid)
		} else {
			err = d.Set(Int(max(ttl, time.Second).Milliseconds()), "routing", "session_affinity", "ttl_ms")
		}
		if err != nil {
			return err
		}
	}
	if r.ForceModelPrefix != nil {
		if err := d.Set(Bool(*r.ForceModelPrefix), d.place("routing.force_model_prefix", "routing.force-model-prefix", "force-model-prefix")...); err != nil {
			return err
		}
	}
	if t := r.Retry; t != nil {
		if n := t.RequestRetry; n != nil {
			if *n < 0 || *n > 1000 {
				return fmt.Errorf("%w: routing.retry.request-retry must be between 0 and 1000", ErrInvalid)
			}
			if err := d.Set(Int(*n), d.place("routing.request_retry", "routing.retry.request-retry", "request-retry")...); err != nil {
				return err
			}
		}
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
	if c := r.Cooldown; c != nil {
		if c.DisableCooling != nil {
			if err := d.Set(Bool(*c.DisableCooling), d.place("routing.disable_cooling", "routing.cooldown.disable-cooling", "disable-cooling")...); err != nil {
				return err
			}
		}
		if n := c.TransientErrorCooldownSeconds; n != nil {
			if *n < -1 || *n > 30*24*60*60 {
				return fmt.Errorf("%w: routing.cooldown.transient-error-cooldown-seconds must be -1 (no cooldown), 0 (the default) or up to 2592000 seconds (30 days)", ErrInvalid)
			}
			v := *n
			if !d.YAML && v > 0 {
				v *= 1000
			}
			if err := d.Set(Int(v), d.place("routing.transient_error_cooldown_ms", "routing.cooldown.transient-error-cooldown-seconds", "transient-error-cooldown-seconds")...); err != nil {
				return err
			}
		}
		if m := c.QuotaResponseMode; m != nil {
			mode := strings.ToLower(strings.TrimSpace(*m))
			if mode != "retryable" && mode != "usage-limit" {
				return fmt.Errorf("%w: routing.cooldown.quota-response-mode must be \"retryable\" or \"usage-limit\"", ErrInvalid)
			}
			if err := d.Set(String(mode), d.place("routing.quota_response_mode", "routing.cooldown.quota-response-mode", "quota-cooldown-response-mode")...); err != nil {
				return err
			}
		}
	}
	if r.Timeouts != nil {
		if err := d.applyTimeouts(r.Timeouts); err != nil {
			return err
		}
	}
	if r.Ignition != nil {
		return d.applyIgnition(r.Ignition)
	}
	return nil
}

func (d *Doc) applyIgnition(raw json.RawMessage) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		if !d.YAML {
			d.Delete("routing", "ignition")
		}
		return nil
	}
	if d.YAML {
		return fmt.Errorf("%w: routing.ignition is this core's own setting and a config.yaml has no name for it; a config.yaml's plugins.configs.lamplighter is read as it, so change that instead", ErrInvalid)
	}
	var c ignite.Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("%w: routing.ignition: %v", ErrInvalid, err)
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("%w: routing.ignition: %v", ErrInvalid, err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil || n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		return fmt.Errorf("%w: routing.ignition: %v", ErrInvalid, err)
	}
	return d.Set(n.Content[0], "routing", "ignition")
}

func (d *Doc) applyTimeouts(raw json.RawMessage) error {
	if string(raw) == "null" {
		if !d.YAML {
			d.Delete("routing", "timeouts")
		}
		return nil
	}
	var t protocol.Timeouts
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return fmt.Errorf("%w: routing.timeouts: %v", ErrInvalid, err)
	}
	if err := route.ValidateTimeouts("routing.timeouts", &t); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, f := range []struct {
		name string
		v    *int64
	}{{"header_ms", t.HeaderMS}, {"idle_ms", t.IdleMS}, {"total_ms", t.TotalMS}} {
		if f.v == nil {
			continue
		}
		if d.YAML {
			return fmt.Errorf("%w: routing.timeouts is this core's own setting and a config.yaml has no name for it", ErrInvalid)
		}
		if err := d.Set(Int(*f.v), "routing", "timeouts", f.name); err != nil {
			return err
		}
	}
	return nil
}
