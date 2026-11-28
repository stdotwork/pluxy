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

const Prefix = "/v0/management"

const APIVersion = 1

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Revision struct {
	Revision uint64    `json:"revision"`
	Hash     string    `json:"hash,omitempty"`
	At       time.Time `json:"at"`
	Error    string    `json:"error,omitempty"`
}

type ExtensionStatus struct {
	host.Status
	Current bool `json:"current"`
}

type Status struct {
	SafeMode      bool              `json:"safe_mode"`
	FirstRun      bool              `json:"first_run"`
	Desired       Revision          `json:"desired"`
	Active        Revision          `json:"active"`
	Extensions    []ExtensionStatus `json:"extensions"`
	Version       string            `json:"version"`
	Commit        string            `json:"commit"`
	Built         string            `json:"built"`
	ManagementAPI int               `json:"management_api"`
}

type InstallRequest struct {
	ID       string `json:"id"`
	Version  string `json:"version,omitempty"`
	Registry string `json:"registry,omitempty"`
	Enable   bool   `json:"enable,omitempty"`
}

type Package struct {
	packages.Installed
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type PackagesReply struct {
	Packages []Package `json:"packages"`
}

type PackageReply struct {
	Package Package `json:"package"`
}

type Registry struct {
	packages.Registry
	Packages []RegistryPackage `json:"packages,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type RegistryPackage struct {
	ID       string            `json:"id"`
	Title    string            `json:"title,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Tier     string            `json:"tier"`
	Releases []RegistryRelease `json:"releases"`
}

type RegistryRelease struct {
	Version   string   `json:"version"`
	Protocol  []int    `json:"protocol"`
	Platforms []string `json:"platforms"`
}

type RegistriesReply struct {
	Registries []Registry `json:"registries"`
}

type Health = route.Health

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

type Account struct {
	ID              string             `json:"id"`
	PublicID        string             `json:"public_id"`
	Source          string             `json:"source"`
	Extension       string             `json:"extension,omitempty"`
	Kind            string             `json:"kind,omitempty"`
	State           string             `json:"state"`
	Disabled        bool               `json:"disabled,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	Label           string             `json:"label,omitempty"`
	Version         uint64             `json:"version,omitempty"`
	ExpiresAt       int64              `json:"expires_at,omitempty"`
	Refreshable     bool               `json:"refreshable"`
	RenewalError    *llm.Error         `json:"renewal_error,omitempty"`
	CreatedAt       *time.Time         `json:"created_at,omitempty"`
	UpdatedAt       *time.Time         `json:"updated_at,omitempty"`
	Priority        int                `json:"priority,omitempty"`
	Weight          *int               `json:"weight,omitempty"`
	DisableCooling  *bool              `json:"disable_cooling,omitempty"`
	MaxConcurrency  int                `json:"max_concurrency,omitempty"`
	RequestRetry    *int               `json:"request_retry,omitempty"`
	Prefix          string             `json:"prefix,omitempty"`
	Models          []route.ModelEntry `json:"models,omitempty"`
	ExcludedModels  []string           `json:"excluded_models,omitempty"`
	ModelAlias      []route.ModelAlias `json:"model_alias,omitempty"`
	Endpoint        string             `json:"endpoint,omitempty"`
	ProxyURL        string             `json:"proxy_url,omitempty"`
	Headers         map[string]string  `json:"headers,omitempty"`
	Timeouts        *protocol.Timeouts `json:"timeouts,omitempty"`
	Settings        json.RawMessage    `json:"settings,omitempty"`
	SettingsInexact bool               `json:"settings_inexact,omitempty"`

	Health Health `json:"health"`

	CooldownUntil  *time.Time       `json:"cooldown_until,omitempty"`
	Cooldowns      []route.Cooldown `json:"cooldowns,omitempty"`
	LastError      *llm.Error       `json:"last_error,omitempty"`
	LastErrorAt    *time.Time       `json:"last_error_at,omitempty"`
	ActiveRequests int              `json:"active_requests,omitempty"`

	Check    *AccountCheck    `json:"check,omitempty"`
	Ignition *AccountIgnition `json:"ignition,omitempty"`

	Quota *QuotaReply `json:"quota,omitempty"`
}

type AccountIgnition = ignite.View

const (
	CheckRunning    = "checking"
	CheckOK         = "ok"
	CheckFailed     = "failed"
	CheckNotChecked = "not_checked"
)

type AccountCheck struct {
	State  string     `json:"state"`
	Detail string     `json:"detail,omitempty"`
	Error  *llm.Error `json:"error,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

type AccountsReply struct {
	Accounts []Account `json:"accounts"`
}

type AccountReply struct {
	Account Account `json:"account"`
}

type AccountPatch struct {
	Disabled *bool `json:"disabled,omitempty"`
}

type LoginMethods struct {
	Extension string                 `json:"extension"`
	Methods   []protocol.LoginMethod `json:"methods"`
}

type LoginMethodsReply struct {
	Extensions []LoginMethods `json:"extensions"`
}

type Login = cred.LoginView

const (
	LoginPending   = cred.LoginPending
	LoginDone      = cred.LoginDone
	LoginFailed    = cred.LoginFailed
	LoginCancelled = cred.LoginCancelled
)

type LoginReply struct {
	Login Login `json:"login"`
}

type AddAccountRequest struct {
	ID        string            `json:"id"`
	Extension string            `json:"extension,omitempty"`
	Method    string            `json:"method"`
	Input     map[string]string `json:"input,omitempty"`
}

type LoginAgainRequest struct {
	Method string            `json:"method"`
	Input  map[string]string `json:"input,omitempty"`
}

type LoginInputRequest struct {
	Input map[string]string `json:"input"`
}

type AuthImportRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type AuthImported struct {
	ID        string   `json:"id"`
	Extension string   `json:"extension"`
	Ignored   []string `json:"ignored,omitempty"`
}

type AuthImportReply struct {
	Imported []AuthImported `json:"imported"`
	Skipped  []string       `json:"skipped,omitempty"`
}

type VertexImportReply struct {
	Status    string `json:"status"`
	Account   string `json:"account"`
	ProjectID string `json:"project_id"`
	Email     string `json:"email,omitempty"`
	Location  string `json:"location"`
}

type RouteReply = route.Plan

type ChainStep = route.Step

type Chain struct {
	Name  string      `json:"name"`
	Steps []ChainStep `json:"steps"`
}

type ChainsReply struct {
	Chains []Chain `json:"chains"`
}

type ChainRequest struct {
	Steps []ChainStep `json:"steps"`
}

type CooldownResetReply struct {
	Account Account `json:"account"`
	Cleared int     `json:"cleared"`
}

type TestRequest struct {
	Model string `json:"model"`
}

type TestResult struct {
	OK           bool       `json:"ok"`
	Model        string     `json:"model"`
	Served       string     `json:"served_model,omitempty"`
	LatencyMS    int64      `json:"latency_ms"`
	Reply        string     `json:"reply,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        *llm.Usage `json:"usage,omitempty"`
	Error        *llm.Error `json:"error,omitempty"`
}

type TestReply struct {
	Result TestResult `json:"result"`
}

type QuotaReply struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Quota     *protocol.Quota `json:"quota"`
}

type AccountModel struct {
	protocol.Model
	Names    []string `json:"names"`
	Excluded bool     `json:"excluded,omitempty"`
}

type AccountModelsReply struct {
	Models []AccountModel `json:"models"`
	From   string         `json:"from,omitempty"`
}

type ModelsReply struct {
	Models []route.ServedModel `json:"models"`
}

type RefreshModelsRequest struct {
	Account string `json:"account,omitempty"`
}

type RefreshAccountResult = route.RefreshResult

type RefreshModelsReply struct {
	Models   []route.ServedModel    `json:"models"`
	Accounts []RefreshAccountResult `json:"accounts"`
}

type ClientKey struct {
	ID     string `json:"id"`
	Masked string `json:"masked"`
	Label  string `json:"label,omitempty"`
}

type ClientKeysReply struct {
	Keys []ClientKey `json:"keys"`
}

type NewClientKeyReply struct {
	Key   ClientKey `json:"key"`
	Value string    `json:"value"`
}

type Config struct {
	Format  string `json:"format"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

type Settings struct {
	Routing               RoutingSettings       `json:"routing"`
	Observability         ObservabilitySettings `json:"observability"`
	TokenCountEstimate    bool                  `json:"token_count_estimate"`
	ProxyURL              string                `json:"proxy-url"`
	Payload               *payload.Rules        `json:"payload,omitempty"`
	ManagementAllowRemote bool                  `json:"management_allow_remote"`
	Listen                string                `json:"listen"`
	ListenAfterRestart    string                `json:"listen_after_restart,omitempty"`
	FromThisComputer      bool                  `json:"from_this_computer"`
}

type ObservabilitySettings struct {
	Logs  LogSettings   `json:"logs"`
	Usage UsageSettings `json:"usage"`
}

type LogSettings struct {
	LoggingToFile      bool `json:"logging-to-file"`
	LogsMaxTotalSizeMB int  `json:"logs-max-total-size-mb"`
	RequestLog         bool `json:"request-log"`
	ErrorLogsMaxFiles  int  `json:"error-logs-max-files"`
}

type UsageSettings struct {
	UsageStatisticsEnabled bool `json:"usage-statistics-enabled"`
}

type RoutingSettings struct {
	Strategy           string             `json:"strategy"`
	SessionAffinity    bool               `json:"session-affinity"`
	SessionAffinityTTL string             `json:"session-affinity-ttl"`
	ForceModelPrefix   bool               `json:"force-model-prefix"`
	Retry              RetrySettings      `json:"retry"`
	Cooldown           CooldownSettings   `json:"cooldown"`
	Timeouts           *protocol.Timeouts `json:"timeouts,omitempty"`
	Ignition           *ignite.Config     `json:"ignition,omitempty"`
}

type RetrySettings struct {
	RequestRetry        *int    `json:"request-retry"`
	MaxRetryCredentials int     `json:"max-retry-credentials"`
	MaxRetryInterval    float64 `json:"max-retry-interval"`
}

type CooldownSettings struct {
	DisableCooling                bool    `json:"disable-cooling"`
	TransientErrorCooldownSeconds float64 `json:"transient-error-cooldown-seconds"`
	QuotaResponseMode             string  `json:"quota-response-mode"`
}

type LogLines = logfile.Lines

type ErrorLogFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type ErrorLogsReply struct {
	Files []ErrorLogFile `json:"files"`
}

type LogsCleared struct {
	Removed int `json:"removed"`
}

type UsageReply struct {
	Enabled bool `json:"enabled"`
	usage.Summary
}

type UsageEventsReply struct {
	Enabled bool `json:"enabled"`
	usage.Page
}

const (
	ClientStateNotConfigured = "not-configured"
	ClientStateConfigured    = "configured"
	ClientStateNeedsUpdate   = "needs-update"
	ClientStateRefused       = "refused"
	ClientStateUnchecked     = "unchecked"
)

const (
	SetupApplyDone  = "done"
	SetupApplyError = "error"
)

type Setup struct {
	FirstRun   bool        `json:"first_run"`
	Scanning   bool        `json:"scanning"`
	ScannedAt  *time.Time  `json:"scanned_at,omitempty"`
	Found      SetupFound  `json:"found"`
	Picks      []SetupPick `json:"picks"`
	Registries int         `json:"registries"`
}

type SetupFound struct {
	Clients []SetupClient `json:"clients"`
	SignIns []SetupSignIn `json:"sign_ins"`
	Imports []SetupImport `json:"imports"`
	Core    SetupCore     `json:"core"`
}

type SetupClient struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Installed        bool   `json:"installed"`
	Version          string `json:"version,omitempty"`
	Config           string `json:"config"`
	State            string `json:"state"`
	CanConnect       bool   `json:"can_connect,omitempty"`
	ConnectedByPluxy bool   `json:"connected_by_pluxy,omitempty"`
}

type ClientConnection struct {
	Client     SetupClient            `json:"client"`
	Files      []ClientConnectionFile `json:"files"`
	Key        *ClientKey             `json:"key,omitempty"`
	KeyMade    bool                   `json:"key_made,omitempty"`
	KeyRemoved bool                   `json:"key_removed,omitempty"`
	Model      string                 `json:"model,omitempty"`
}

const (
	ClientFileCreated   = "created"
	ClientFileChanged   = "changed"
	ClientFileUnchanged = "unchanged"
	ClientFileRestored  = "restored"
	ClientFileRemoved   = "removed"
	ClientFileMerged    = "merged"
)

type ClientConnectionFile struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

type SetupSignIn struct {
	Provider string `json:"provider"`
	From     string `json:"from"`
	Label    string `json:"label"`
}

type SetupImport struct {
	Kind       string           `json:"kind"`
	Config     string           `json:"config"`
	AuthDir    string           `json:"auth_dir"`
	Accounts   int              `json:"accounts"`
	LastImport *SetupLastImport `json:"last_import,omitempty"`
}

type SetupLastImport struct {
	At         time.Time `json:"at"`
	Brought    int       `json:"brought"`
	Changed    int       `json:"changed"`
	NotBrought int       `json:"not_brought"`
}

type SetupCore struct {
	Listen               string `json:"listen"`
	ReachableFromNetwork bool   `json:"reachable_from_network"`
}

type SetupPick struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Installed bool   `json:"installed"`
	Suggested bool   `json:"suggested"`
	Because   string `json:"because,omitempty"`
}

type SetupApplyRequest struct {
	Picks []string `json:"picks"`
}

type SetupApplyResult struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Package *Package `json:"package,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type SetupApplyReply struct {
	Results []SetupApplyResult `json:"results"`
}

type SetupImportRequest struct {
	Config  string `json:"config"`
	AuthDir string `json:"auth_dir,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

type ImportReport struct {
	DryRun     bool         `json:"dry_run"`
	Brought    []ImportRow  `json:"brought"`
	Changed    []ImportRow  `json:"changed"`
	NotBrought []ImportRow  `json:"not_brought"`
	Needs      []ImportNeed `json:"needs"`
}

type ImportRow struct {
	What     string `json:"what"`
	Name     string `json:"name,omitempty"`
	As       string `json:"as,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Why      string `json:"why,omitempty"`
	KeepWith string `json:"keep_with,omitempty"`
}

type ImportNeed struct {
	Pick string `json:"pick"`
	Why  string `json:"why"`
}

type SetupDoneReply struct {
	FirstRun bool `json:"first_run"`
}

type ClientPaste struct {
	Tool    string            `json:"tool"`
	Name    string            `json:"name"`
	BaseURL string            `json:"base_url"`
	Key     string            `json:"key"`
	Files   []ClientPasteFile `json:"files"`
	Note    string            `json:"note,omitempty"`
	Model   string            `json:"model,omitempty"`
	Models  []string          `json:"models"`
}

type ClientPasteFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ThemeSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

type ThemesReply struct {
	Current string         `json:"current"`
	Themes  []ThemeSummary `json:"themes"`
}

type ThemeSelection struct {
	ID string `json:"id"`
}

type RunningRequest struct {
	ID            string    `json:"id"`
	StartedAt     time.Time `json:"started_at"`
	Endpoint      string    `json:"endpoint"`
	Format        string    `json:"format,omitempty"`
	Stream        bool      `json:"stream"`
	Model         string    `json:"model,omitempty"`
	KeyID         string    `json:"key_id,omitempty"`
	ClientIP      string    `json:"client_ip,omitempty"`
	Account       string    `json:"account,omitempty"`
	Provider      string    `json:"provider,omitempty"`
	UpstreamModel string    `json:"upstream_model,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
}

type RequestsReply struct {
	Requests []RunningRequest `json:"requests"`
}

type RequestReply struct {
	Request RunningRequest `json:"request"`
}
