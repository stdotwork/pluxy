// Package management holds the management API's request and reply types and a client for it.
// Core's handlers write these types and the pluxy command reads them, so the two cannot drift.
// docs/management-api.md describes the API; docs/cli.md describes the command.
package management

import (
	"encoding/json"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/ignite"
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

// APIVersion is GET /status's management_api: it goes up when a call changes in a way an older
// client would misread. Fields are only added between such changes, so a client that reads a
// version it knows keeps working.
const APIVersion = 1

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
	// FirstRun is true until POST /setup/done ends first run.
	FirstRun bool `json:"first_run"`
	// Desired is the newest config version core has seen; Active is the one serving.
	Desired    Revision          `json:"desired"`
	Active     Revision          `json:"active"`
	Extensions []ExtensionStatus `json:"extensions"`
	// Version, Commit and Built say which build of core runs.
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	// ManagementAPI is this API's version (APIVersion): it goes up when a call changes in a way
	// an older client would misread.
	ManagementAPI int `json:"management_api"`
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
	ID string `json:"id"`
	// Title and Summary say what it is, as the registry describes it; empty when it does not.
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Kind is what it adds: "provider", "client", or empty when the registry does not say.
	Kind string `json:"kind,omitempty"`
	// Tier is how far the registry vouches for it: "pick" (offered at setup), "verified" or
	// "unverified" (also when the registry does not say). Core enforces nothing by it.
	Tier     string            `json:"tier"`
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

// Health is an account's health (Account.Health; the README's "Accounts").
type Health = route.Health

// Health states (Health.State) and where a block's end came from (Health.Source).
const (
	HealthOK         = route.HealthOK
	HealthCooling    = route.HealthCooling
	HealthExhausted  = route.HealthExhausted
	HealthNeedsLogin = route.HealthNeedsLogin
	HealthBanned     = route.HealthBanned
	HealthDisabled   = route.HealthDisabled
	HealthBroken     = route.HealthBroken

	SourceProvider   = route.SourceProvider
	SourceBackoff    = route.SourceBackoff
	SourceUnverified = route.SourceUnverified
)

// Account is an account as the management API lists it. It never holds a secret.
type Account struct {
	ID string `json:"id"`
	// PublicID is the ID client replies name the account by (the X-Pluxy-Account header, the
	// pluxy member of an error): short, stable across restarts, and never an email address or a
	// label, whatever ID the account has.
	PublicID string `json:"public_id"`
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
	// Timeouts overrides routing.timeouts for this account (header_ms, idle_ms, total_ms).
	Timeouts *protocol.Timeouts `json:"timeouts,omitempty"`
	// Settings are an account added by login's settings, with a placeholder in place of each
	// secret, as GET /config shows the config file.
	Settings json.RawMessage `json:"settings,omitempty"`
	// SettingsInexact marks settings that hold a number a float64 does not hold exactly (a long
	// fraction, a very large or very small exponent, an integer past 2^53), which a client that
	// reads JSON numbers as float64, as a browser does, would write back changed.
	SettingsInexact bool `json:"settings_inexact,omitempty"`

	// Health is the account's state in one word, with when and why (route.Health): ok,
	// cooling, exhausted, needs_login, banned, disabled or broken.
	Health Health `json:"health"`

	// What routing knows about the account now (route.Runtime). An account that receives no
	// requests (disabled, or waiting for a new login) has none of it.
	CooldownUntil  *time.Time       `json:"cooldown_until,omitempty"`
	Cooldowns      []route.Cooldown `json:"cooldowns,omitempty"`
	LastError      *llm.Error       `json:"last_error,omitempty"`
	LastErrorAt    *time.Time       `json:"last_error_at,omitempty"`
	ActiveRequests int              `json:"active_requests,omitempty"`

	// Check is the result of core's last check of the account; absent while core has none.
	Check *AccountCheck `json:"check,omitempty"`
	// Ignition is the account's window ignition (routing.ignition): each usage window core
	// starts, its state and when core starts it next. Absent when ignition does not run for the
	// account.
	Ignition *AccountIgnition `json:"ignition,omitempty"`

	// Quota is the last quota reply core read for the account, when it read it and what it
	// said, as GET /accounts/{id}/quota replies. It is kept in memory, so it is gone after a
	// restart, and absent while core has none (an account never read, or one with no reading).
	// Core reads nothing to fill it: it comes from that call and from routing's own readings.
	Quota *QuotaReply `json:"quota,omitempty"`
}

// AccountIgnition is an account's window ignition, in Account.
type AccountIgnition = ignite.View

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

// LoginMethods is one running extension's ways to sign in, as GET /login-methods lists them.
type LoginMethods struct {
	Extension string                 `json:"extension"`
	Methods   []protocol.LoginMethod `json:"methods"`
}

// LoginMethodsReply is GET /login-methods's reply.
type LoginMethodsReply struct {
	Extensions []LoginMethods `json:"extensions"`
}

// Login is a sign-in as the management API shows it (cred.LoginView).
type Login = cred.LoginView

// Login states (Login.State).
const (
	LoginPending   = cred.LoginPending
	LoginDone      = cred.LoginDone
	LoginFailed    = cred.LoginFailed
	LoginCancelled = cred.LoginCancelled
)

// LoginReply is POST /accounts's, POST /accounts/{id}/login's and GET /logins/{id}'s reply.
type LoginReply struct {
	Login Login `json:"login"`
}

// AddAccountRequest is POST /accounts's body, as far as the pluxy command fills it in: the
// account's ID, the extension and method that sign in, and what the user entered to start.
type AddAccountRequest struct {
	ID        string            `json:"id"`
	Extension string            `json:"extension,omitempty"`
	Method    string            `json:"method"`
	Input     map[string]string `json:"input,omitempty"`
}

// LoginAgainRequest is POST /accounts/{id}/login's body.
type LoginAgainRequest struct {
	Method string            `json:"method"`
	Input  map[string]string `json:"input,omitempty"`
}

// LoginInputRequest is POST /logins/{id}/input's body: what the user entered for the prompt.
type LoginInputRequest struct {
	Input map[string]string `json:"input"`
}

// AuthImportRequest is POST /accounts/import's body: one credential file's name and text.
type AuthImportRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// AuthImported is one account POST /accounts/import added.
type AuthImported struct {
	ID        string   `json:"id"`
	Extension string   `json:"extension"`
	Ignored   []string `json:"ignored,omitempty"`
}

// AuthImportReply is POST /accounts/import's reply.
type AuthImportReply struct {
	Imported []AuthImported `json:"imported"`
	Skipped  []string       `json:"skipped,omitempty"`
}

// VertexImportReply is POST /vertex/import's reply.
type VertexImportReply struct {
	Status    string `json:"status"`
	Account   string `json:"account"`
	ProjectID string `json:"project_id"`
	Email     string `json:"email,omitempty"`
	Location  string `json:"location"`
}

// RouteReply is GET /route's reply: what a request for the model would do now (route.Plan). Each
// account is named by its ID and its public ID, the one replies name it by.
type RouteReply = route.Plan

// ChainStep is one step of a fallback chain: a model, and optionally the one provider and the
// accounts (by ID) that serve it.
type ChainStep = route.Step

// Chain is a fallback chain: the model name clients ask for, and its steps in order.
type Chain struct {
	Name  string      `json:"name"`
	Steps []ChainStep `json:"steps"`
}

// ChainsReply is GET /chains's reply, sorted by name.
type ChainsReply struct {
	Chains []Chain `json:"chains"`
}

// ChainRequest is PUT /chains/{name}'s body.
type ChainRequest struct {
	Steps []ChainStep `json:"steps"`
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

// AccountModel is one model an account serves, as GET /accounts/{id}/models lists it: the
// model by its upstream name, with what the extension says about it, and the names clients ask
// for it by after the account's exclusions, aliases and prefix.
type AccountModel struct {
	protocol.Model
	// Names are the names clients use for the model on this account; empty when the account's
	// excluded_models leave it out.
	Names []string `json:"names"`
	// Excluded is true when the account's excluded_models leave the model out.
	Excluded bool `json:"excluded,omitempty"`
}

// AccountModelsReply is GET /accounts/{id}/models's reply.
type AccountModelsReply struct {
	Models []AccountModel `json:"models"`
	// From says where the list came from: "account" (read from the provider using the account's
	// credential), "extension" (the extension's built-in list), or omitted.
	From string `json:"from,omitempty"`
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
	// Label is the key's name, such as the tool it was made for; absent for a key with none.
	Label string `json:"label,omitempty"`
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
	// Timeouts are the default upstream timeouts of every account, in milliseconds; absent when
	// the config sets none, which leaves core's own.
	Timeouts *protocol.Timeouts `json:"timeouts,omitempty"`
	// Ignition is window ignition's settings (routing.ignition) as the config sets them; absent
	// when it sets none, which leaves ignition off.
	Ignition *ignite.Config `json:"ignition,omitempty"`
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

// ErrorLogFile is one error log in GET /request-error-logs's reply.
type ErrorLogFile struct {
	Name string `json:"name"`
	// Size is in bytes.
	Size int64 `json:"size"`
	// Modified is when it was last written, in Unix seconds.
	Modified int64 `json:"modified"`
}

// ErrorLogsReply is GET /request-error-logs's reply: the error logs, newest first.
type ErrorLogsReply struct {
	Files []ErrorLogFile `json:"files"`
}

// LogsCleared is DELETE /logs's reply: Removed is how many rotated log files it deleted
// (main.log itself is emptied, not counted).
type LogsCleared struct {
	Removed int `json:"removed"`
}

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

// Client tool states (SetupClient.State).
const (
	ClientStateNotConfigured = "not-configured"
	ClientStateConfigured    = "configured"
	ClientStateNeedsUpdate   = "needs-update"
	// ClientStateRefused is a config file the tool's writer would refuse to change.
	ClientStateRefused = "refused"
	// ClientStateUnchecked is a tool whose settings core does not read, so whether they point at
	// this core is not known. Core's scan reports it for Claude Desktop, Grok Build and Hermes Agent.
	ClientStateUnchecked = "unchecked"
)

// Setup apply outcomes (SetupApplyResult.Status).
const (
	SetupApplyDone  = "done"
	SetupApplyError = "error"
)

// Setup is GET /setup's and POST /setup/scan's reply.
type Setup struct {
	FirstRun   bool        `json:"first_run"`
	Scanning   bool        `json:"scanning"`
	ScannedAt  *time.Time  `json:"scanned_at,omitempty"`
	Found      SetupFound  `json:"found"`
	Picks      []SetupPick `json:"picks"`
	Registries int         `json:"registries"`
}

// SetupFound is what core found on the machine it runs on.
type SetupFound struct {
	Clients []SetupClient `json:"clients"`
	SignIns []SetupSignIn `json:"sign_ins"`
	Imports []SetupImport `json:"imports"`
	Core    SetupCore     `json:"core"`
}

// SetupClient is one coding tool core looked for on the machine.
type SetupClient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Config    string `json:"config"`
	State     string `json:"state"`
}

// SetupSignIn is an existing sign-in core saw on the machine (no token is read).
type SetupSignIn struct {
	Provider string `json:"provider"`
	From     string `json:"from"`
	Label    string `json:"label"`
}

// SetupImport is an existing CLIProxyAPI setup core saw on the machine.
type SetupImport struct {
	Kind     string `json:"kind"`
	Config   string `json:"config"`
	AuthDir  string `json:"auth_dir"`
	Accounts int    `json:"accounts"`
	// LastImport is the last import made from this config on this core, when there was one.
	LastImport *SetupLastImport `json:"last_import,omitempty"`
}

// SetupLastImport is what core kept of an import: when it ran and how many rows each list of its
// report held. It keeps counts only, no row text, name or value.
type SetupLastImport struct {
	At         time.Time `json:"at"`
	Brought    int       `json:"brought"`
	Changed    int       `json:"changed"`
	NotBrought int       `json:"not_brought"`
}

// SetupCore describes core's own listen address and network reachability.
type SetupCore struct {
	Listen               string `json:"listen"`
	ReachableFromNetwork bool   `json:"reachable_from_network"`
}

// SetupPick is one package offered at setup.
type SetupPick struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Installed bool   `json:"installed"`
	Suggested bool   `json:"suggested"`
	Because   string `json:"because,omitempty"`
}

// SetupApplyRequest is POST /setup/apply's body.
type SetupApplyRequest struct {
	Picks []string `json:"picks"`
}

// SetupApplyResult is one pick's outcome in SetupApplyReply.
type SetupApplyResult struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Package *Package `json:"package,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// SetupApplyReply is POST /setup/apply's reply.
type SetupApplyReply struct {
	Results []SetupApplyResult `json:"results"`
}

// SetupImportRequest is POST /setup/import's body: a CLIProxyAPI config.yaml on core's machine,
// and its auth folder (default: the config's oauth.auth-dir). With DryRun nothing is written.
type SetupImportRequest struct {
	Config  string `json:"config"`
	AuthDir string `json:"auth_dir,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// ImportReport is POST /setup/import's reply. Every key of the config and every file of the auth
// folder is in exactly one of Brought, Changed and NotBrought.
type ImportReport struct {
	DryRun     bool         `json:"dry_run"`
	Brought    []ImportRow  `json:"brought"`
	Changed    []ImportRow  `json:"changed"`
	NotBrought []ImportRow  `json:"not_brought"`
	Needs      []ImportNeed `json:"needs"`
}

// ImportRow is one item of an import report: a key of the config (What is its path in the file)
// or a file of the auth folder (What is "auth file", Name its name).
type ImportRow struct {
	What string `json:"what"`
	Name string `json:"name,omitempty"`
	// As is what it became in pluxy.
	As   string `json:"as,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Why  string `json:"why,omitempty"`
	// KeepWith says how to keep what the old setup did, where pluxy changed it.
	KeepWith string `json:"keep_with,omitempty"`
}

// ImportNeed is a provider package the imported accounts need and core does not have installed.
type ImportNeed struct {
	Pick string `json:"pick"`
	Why  string `json:"why"`
}

// SetupDoneReply is POST /setup/done's reply.
type SetupDoneReply struct {
	FirstRun bool `json:"first_run"`
}

// ClientPaste is GET /setup/clients/{id}/paste's reply: what to paste into a client tool's
// config files to point it at this core.
type ClientPaste struct {
	// Tool is the tool's canonical ID (SetupClient.ID), whatever name the call used.
	Tool string `json:"tool"`
	Name string `json:"name"`
	// BaseURL is the address a program uses to reach this core: core's own, for a caller on core's
	// machine, and the address the caller reached core by for a caller on another machine.
	BaseURL string `json:"base_url"`
	// Key is a placeholder for a client key, never its value.
	Key   string            `json:"key"`
	Files []ClientPasteFile `json:"files"`
	// Note says what the files need besides themselves; left out when nothing.
	Note string `json:"note,omitempty"`
}

// ClientPasteFile is one file of a ClientPaste: where it goes (from the home folder, ~) and
// the settings to put in it.
type ClientPasteFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ThemeSummary describes one theme stored by core, as GET /themes lists it and PUT returns it.
type ThemeSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

// ThemesReply is GET /themes's reply: the selected theme ID and the themes stored by core.
type ThemesReply struct {
	Current string         `json:"current"`
	Themes  []ThemeSummary `json:"themes"`
}

// ThemeSelection is PUT /theme's request and reply: the selected theme ID (empty clears it).
type ThemeSelection struct {
	ID string `json:"id"`
}

// RunningRequest is a client request core is serving now, as GET /requests lists it. A Responses
// WebSocket is listed while one of its turns runs, under the socket's trace ID.
type RunningRequest struct {
	// ID is the request's trace ID (the X-CPA-TRACE-ID reply header), which POST
	// /requests/{trace}/cancel takes.
	ID string `json:"id"`
	// StartedAt is when the request arrived.
	StartedAt time.Time `json:"started_at"`
	// Endpoint is the client API path, such as /v1/chat/completions; Format the client's wire
	// format, such as openai.chat.
	Endpoint string `json:"endpoint"`
	Format   string `json:"format,omitempty"`
	Stream   bool   `json:"stream"`
	// Model is the model as the client asked for it; absent until the request's body is read.
	Model string `json:"model,omitempty"`
	// KeyID identifies the client key the request came with, as GET /client-keys lists client keys.
	KeyID string `json:"key_id,omitempty"`
	// ClientIP is the address the request came from, as the access log and usage events give it.
	ClientIP string `json:"client_ip,omitempty"`
	// Account, Provider and UpstreamModel are the account the newest attempt went to (its ID, as
	// GET /accounts lists it), its extension and the model sent upstream; Attempts counts the
	// attempts so far. All four are absent until routing has chosen an account.
	Account       string `json:"account,omitempty"`
	Provider      string `json:"provider,omitempty"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
}

// RequestsReply is GET /requests's reply: the requests being served, oldest first.
type RequestsReply struct {
	Requests []RunningRequest `json:"requests"`
}

// RequestReply is POST /requests/{trace}/cancel's reply: the request as it was when cancelled.
type RequestReply struct {
	Request RunningRequest `json:"request"`
}
