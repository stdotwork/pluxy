// Package management holds the management API's request and reply types and a client for it.
// Core's handlers write these types and the pluxy command reads them, so the two cannot drift.
// README.md in this folder describes the API and the command.
package management

import (
	"encoding/json"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

// Prefix is where the management API is served. The 0 is its version.
const Prefix = "/v0/management"

// ErrorBody is every failed call's reply: {"error": {"code": "...", "message": "..."}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the error in an ErrorBody. Code is stable; Message is for a person.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Revision describes one version of the config file core has seen.
type Revision struct {
	// Revision counts the config versions core has tried to load, from 1.
	Revision uint64 `json:"revision"`
	// Hash is the SHA-256 of the file's contents.
	Hash string `json:"hash,omitempty"`
	// At is when core read it (desired) or published it (active).
	At time.Time `json:"at"`
	// Error says why this version is not active, when it failed.
	Error string `json:"error,omitempty"`
}

// ExtensionStatus is an extension generation's status, as GET /status shows it.
type ExtensionStatus struct {
	host.Status
	// Current is true for a generation new requests go to.
	Current bool `json:"current"`
}

// Status is GET /status's reply, and POST /reload's: core's live state.
type Status struct {
	SafeMode bool `json:"safe_mode"`
	// Desired is the newest config version core has seen; Active is the one serving.
	Desired    Revision          `json:"desired"`
	Active     Revision          `json:"active"`
	Extensions []ExtensionStatus `json:"extensions"`
	// Version, Commit and Built say which build of core runs.
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
}

// InstallRequest is what POST /packages asks for.
type InstallRequest struct {
	ID       string `json:"id"`
	Version  string `json:"version,omitempty"`
	Registry string `json:"registry,omitempty"`
	Enable   bool   `json:"enable,omitempty"`
}

// Package is an installed package.
type Package struct {
	packages.Installed
	// State is "disabled", the running extension's state, "failed" (enabled but it could not
	// start; Error says why) or "stopped" (enabled, in safe mode).
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// PackagesReply is GET /packages's reply.
type PackagesReply struct {
	Packages []Package `json:"packages"`
}

// PackageReply is the reply of a call that installs, enables or disables a package.
type PackageReply struct {
	Package Package `json:"package"`
}

// Registry is what one registry offers.
type Registry struct {
	packages.Registry
	Packages []RegistryPackage `json:"packages,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// RegistryPackage is one package a registry offers.
type RegistryPackage struct {
	ID       string            `json:"id"`
	Releases []RegistryRelease `json:"releases"`
}

// RegistryRelease is one release a registry offers.
type RegistryRelease struct {
	Version   string   `json:"version"`
	Protocol  []int    `json:"protocol"`
	Platforms []string `json:"platforms"`
}

// RegistriesReply is GET /registries's reply.
type RegistriesReply struct {
	Registries []Registry `json:"registries"`
}

// Account is an account as the management API lists it. It never holds a secret.
type Account struct {
	ID string `json:"id"`
	// Source is "config" for an account set in core's config, "login" for one added by login.
	Source    string `json:"source"`
	Extension string `json:"extension,omitempty"`
	// Kind is the credential's kind, for example api_key or oauth.
	Kind     string `json:"kind,omitempty"`
	State    string `json:"state"`
	Disabled bool   `json:"disabled,omitempty"`
	// Reason says why the account needs a new login.
	Reason      string `json:"reason,omitempty"`
	Label       string `json:"label,omitempty"`
	Version     uint64 `json:"version,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Refreshable bool   `json:"refreshable"`
	// RenewalError is why the last automatic renewal failed, while core waits before trying
	// again.
	RenewalError *llm.Error `json:"renewal_error,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	// The account's routing fields, as its config entry or its login set them (route.Account).
	Priority       int                `json:"priority,omitempty"`
	Weight         *int               `json:"weight,omitempty"`
	DisableCooling *bool              `json:"disable_cooling,omitempty"`
	MaxConcurrency int                `json:"max_concurrency,omitempty"`
	RequestRetry   *int               `json:"request_retry,omitempty"`
	Prefix         string             `json:"prefix,omitempty"`
	Models         []route.ModelEntry `json:"models,omitempty"`
	ExcludedModels []string           `json:"excluded_models,omitempty"`
	ModelAlias     []route.ModelAlias `json:"model_alias,omitempty"`
	Endpoint       string             `json:"endpoint,omitempty"`
	// ProxyURL is the account's own proxy_url, with "redacted" in place of its user name and
	// password.
	ProxyURL string `json:"proxy_url,omitempty"`
	// Headers are the account's headers, with a placeholder in place of each secret value, the
	// one GET /config shows for it.
	Headers map[string]string `json:"headers,omitempty"`
	// Settings are an account added by login's settings, with a placeholder in place of each
	// secret, as GET /config shows the config file.
	Settings json.RawMessage `json:"settings,omitempty"`
	// SettingsInexact marks settings that hold a number a float64 does not hold exactly (a long
	// fraction, a very large or very small exponent, an integer past 2^53), which a client that
	// reads JSON numbers as float64, as a browser does, would write back changed.
	SettingsInexact bool `json:"settings_inexact,omitempty"`

	// What routing knows about the account now (route.Runtime). An account that receives no
	// requests (disabled, or waiting for a new login) has none of it.
	CooldownUntil  *time.Time       `json:"cooldown_until,omitempty"`
	Cooldowns      []route.Cooldown `json:"cooldowns,omitempty"`
	LastError      *llm.Error       `json:"last_error,omitempty"`
	LastErrorAt    *time.Time       `json:"last_error_at,omitempty"`
	ActiveRequests int              `json:"active_requests,omitempty"`

	// Check is the result of core's last check of the account; absent while core has none.
	Check *AccountCheck `json:"check,omitempty"`
}

// Account check states (AccountCheck.State).
const (
	CheckRunning    = "checking"
	CheckOK         = "ok"
	CheckFailed     = "failed"
	CheckNotChecked = "not_checked"
)

// AccountCheck is the result of core's check of an account: the extension's check call, which
// asks the upstream something that costs the account nothing. Core checks an account when it
// starts, and when a reload or a login adds or changes one.
type AccountCheck struct {
	// State is checking, ok, failed, or not_checked when the extension cannot check the account.
	State string `json:"state"`
	// Detail says what was checked (ok) or why the account was not checked (not_checked).
	Detail string `json:"detail,omitempty"`
	// Error is why the check failed (failed).
	Error *llm.Error `json:"error,omitempty"`
	// At is when the check ended.
	At *time.Time `json:"at,omitempty"`
}

// AccountsReply is GET /accounts's reply.
type AccountsReply struct {
	Accounts []Account `json:"accounts"`
}

// AccountReply is the reply of a call that changes one account.
type AccountReply struct {
	Account Account `json:"account"`
}

// AccountPatch is the part of PATCH /accounts/{id}'s body the pluxy command sends. The call
// takes the account's routing fields too, by their JSON names.
type AccountPatch struct {
	Disabled *bool `json:"disabled,omitempty"`
}

// CooldownResetReply is POST /accounts/{id}/cooldown/reset's reply.
type CooldownResetReply struct {
	// Account is the account after the reset.
	Account Account `json:"account"`
	// Cleared counts the cooldowns removed.
	Cleared int `json:"cleared"`
}

// TestRequest is POST /accounts/{id}/test's body; an empty model lets core pick one.
type TestRequest struct {
	Model string `json:"model"`
}

// TestResult is the outcome of POST /accounts/{id}/test.
type TestResult struct {
	OK bool `json:"ok"`
	// Model is the model the request named; Served is the one that answered, when the
	// upstream said.
	Model  string `json:"model"`
	Served string `json:"served_model,omitempty"`
	// LatencyMS is the time from sending the request to its last event.
	LatencyMS int64 `json:"latency_ms"`
	// Reply is the start of the model's text reply.
	Reply        string     `json:"reply,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        *llm.Usage `json:"usage,omitempty"`
	Error        *llm.Error `json:"error,omitempty"`
}

// TestReply is POST /accounts/{id}/test's reply.
type TestReply struct {
	Result TestResult `json:"result"`
}

// QuotaReply is GET /accounts/{id}/quota's reply: the account's plan and usage windows, as its
// provider reports them.
type QuotaReply struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Quota     *protocol.Quota `json:"quota"`
}

// ModelsReply is GET /models's reply.
type ModelsReply struct {
	Models []route.ServedModel `json:"models"`
}

// RefreshModelsRequest is POST /models/refresh's optional body.
type RefreshModelsRequest struct {
	Account string `json:"account,omitempty"`
}

// RefreshAccountResult is one account's outcome in POST /models/refresh.
type RefreshAccountResult = route.RefreshResult

// RefreshModelsReply is POST /models/refresh's reply.
type RefreshModelsReply struct {
	Models   []route.ServedModel    `json:"models"`
	Accounts []RefreshAccountResult `json:"accounts"`
}

// ClientKey is a client key as the management API lists it.
type ClientKey struct {
	// ID names the key in calls, without showing it. It stays the same across restarts (derived
	// with the key in state/key-id.key).
	ID string `json:"id"`
	// Masked is the key with all but its first and last two characters hidden; a key shorter
	// than 8 characters is hidden whole.
	Masked string `json:"masked"`
}

// ClientKeysReply is GET /client-keys's reply.
type ClientKeysReply struct {
	Keys []ClientKey `json:"keys"`
}

// NewClientKeyReply is POST /client-keys's reply, the one reply that shows a client key.
type NewClientKeyReply struct {
	Key   ClientKey `json:"key"`
	Value string    `json:"value"`
}

// Config is GET /config's reply: the config document with its secrets replaced.
type Config struct {
	// Format is "yaml" for a config.yaml with today's core's key names, else "json".
	Format string `json:"format"`
	// Hash is the SHA-256 of the file's contents, as status shows it; a write may send it
	// back as base_hash.
	Hash string `json:"hash"`
	// Content is the file's contents with a placeholder in place of each secret.
	Content string `json:"content"`
}

// Settings is GET /settings's reply: the basic and routing settings, by today's core's names.
type Settings struct {
	Routing       RoutingSettings       `json:"routing"`
	Observability ObservabilitySettings `json:"observability"`
	// TokenCountEstimate is this core's own setting (no counterpart in today's core).
	TokenCountEstimate bool `json:"token_count_estimate"`
	// ProxyURL is the global proxy_url, with "redacted" in place of its user name and password.
	ProxyURL string `json:"proxy-url"`
	// Payload is the payload rules, with today's core's names; absent when there are none.
	Payload *payload.Rules `json:"payload,omitempty"`
}

// ObservabilitySettings are the log and usage settings in Settings.
type ObservabilitySettings struct {
	Logs  LogSettings   `json:"logs"`
	Usage UsageSettings `json:"usage"`
}

// LogSettings are the log settings in Settings.
type LogSettings struct {
	LoggingToFile      bool `json:"logging-to-file"`
	LogsMaxTotalSizeMB int  `json:"logs-max-total-size-mb"`
	RequestLog         bool `json:"request-log"`
	ErrorLogsMaxFiles  int  `json:"error-logs-max-files"`
}

// UsageSettings are the usage settings in Settings.
type UsageSettings struct {
	UsageStatisticsEnabled bool `json:"usage-statistics-enabled"`
}

// RoutingSettings are the routing settings in Settings.
type RoutingSettings struct {
	Strategy           string           `json:"strategy"`
	SessionAffinity    bool             `json:"session-affinity"`
	SessionAffinityTTL string           `json:"session-affinity-ttl"`
	ForceModelPrefix   bool             `json:"force-model-prefix"`
	Retry              RetrySettings    `json:"retry"`
	Cooldown           CooldownSettings `json:"cooldown"`
}

// RetrySettings are the retry settings in RoutingSettings.
type RetrySettings struct {
	// RequestRetry is null while the config leaves rounds to this core's own rules.
	RequestRetry        *int `json:"request-retry"`
	MaxRetryCredentials int  `json:"max-retry-credentials"`
	// MaxRetryInterval is in seconds.
	MaxRetryInterval float64 `json:"max-retry-interval"`
}

// CooldownSettings are the cooldown settings in RoutingSettings.
type CooldownSettings struct {
	DisableCooling bool `json:"disable-cooling"`
	// TransientErrorCooldownSeconds is 0 for this core's backoff, -1 for none.
	TransientErrorCooldownSeconds float64 `json:"transient-error-cooldown-seconds"`
	// QuotaResponseMode is retryable (the default) or usage-limit.
	QuotaResponseMode string `json:"quota-response-mode"`
}

// LogLines is GET /logs's reply.
type LogLines = logfile.Lines

// UsageReply is GET /usage's reply: whether usage is recorded, and the summary.
type UsageReply struct {
	Enabled bool `json:"enabled"`
	usage.Summary
}

// UsageEventsReply is GET /usage/events's reply.
type UsageEventsReply struct {
	Enabled bool `json:"enabled"`
	usage.Page
}
