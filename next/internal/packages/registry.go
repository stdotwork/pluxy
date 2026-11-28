package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/host"
)

const IndexVersion = 0

const (
	maxIndex       = 8 << 20
	maxUnpacked    = 512 << 20
	maxEntries     = 10000
	maxArchiveSize = maxUnpacked
)

var (
	ErrNotFound = errors.New("not found")
	ErrRefused  = errors.New("refused")
	ErrFetch    = errors.New("fetch failed")
	ErrConflict = errors.New("conflict")
)

type Registry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Index struct {
	RegistryVersion int                `json:"registry_version"`
	Packages        map[string]Package `json:"packages"`
}

type Package struct {
	Title    string    `json:"title,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Tier     string    `json:"tier,omitempty"`
	Releases []Release `json:"releases"`
}

const (
	KindProvider = "provider"
	KindClient   = "client"
)

const (
	TierPick       = "pick"
	TierVerified   = "verified"
	TierUnverified = "unverified"
)

const (
	maxTitle   = 80
	maxSummary = 300
)

var wordPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func describedText(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

type Release struct {
	Version   string              `json:"version"`
	Protocol  []int               `json:"protocol"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (ix *Index) validate() error {
	if ix.RegistryVersion != IndexVersion {
		return fmt.Errorf("registry_version %d is not supported (this core reads %d)", ix.RegistryVersion, IndexVersion)
	}
	for id, p := range ix.Packages {
		if !host.ValidID(id) {
			return fmt.Errorf("package ID %q is not of the form publisher/name", id)
		}
		if !describedText(p.Title, maxTitle) || !describedText(p.Summary, maxSummary) {
			return fmt.Errorf("package %s: title and summary must be one line of at most %d and %d characters", id, maxTitle, maxSummary)
		}
		for name, w := range map[string]string{"kind": p.Kind, "tier": p.Tier} {
			if w != "" && !wordPattern.MatchString(w) {
				return fmt.Errorf("package %s: %s %q is not a lower-case word", id, name, w)
			}
		}
		seen := map[string]bool{}
		for _, r := range p.Releases {
			if !host.ValidVersion(r.Version) || seen[r.Version] {
				return fmt.Errorf("package %s: release version %q is not valid or is listed twice", id, r.Version)
			}
			seen[r.Version] = true
			for plat, a := range r.Artifacts {
				if !sha256Pattern.MatchString(a.SHA256) {
					return fmt.Errorf("package %s %s for %s: sha256 must be 64 lower-case hex digits", id, r.Version, plat)
				}
				if a.Size <= 0 || a.Size > maxArchiveSize {
					return fmt.Errorf("package %s %s for %s: size must be between 1 and %d bytes", id, r.Version, plat, maxArchiveSize)
				}
				if a.URL == "" {
					return fmt.Errorf("package %s %s for %s: url is required", id, r.Version, plat)
				}
			}
		}
	}
	return nil
}

func (ix *Index) pick(id, version string, protocols []int, platform string) (Release, Artifact, error) {
	p, ok := ix.Packages[id]
	if !ok {
		return Release{}, Artifact{}, fmt.Errorf("%w: no package %s", ErrNotFound, id)
	}
	fits := func(r Release) error {
		if len(r.Protocol) == 0 {
			return fmt.Errorf("%w: app-only package: install it in the desktop app (release has no provider protocol)", ErrRefused)
		}
		if !slices.ContainsFunc(r.Protocol, func(v int) bool { return slices.Contains(protocols, v) }) {
			return fmt.Errorf("%w: %s %s speaks protocol %v; this core speaks %v", ErrRefused, id, r.Version, r.Protocol, protocols)
		}
		if _, ok := r.Artifacts[platform]; !ok {
			return fmt.Errorf("%w: %s %s has no archive for %s", ErrRefused, id, r.Version, platform)
		}
		return nil
	}
	if version != "" {
		i := slices.IndexFunc(p.Releases, func(r Release) bool { return r.Version == version })
		if i < 0 {
			return Release{}, Artifact{}, fmt.Errorf("%w: no release %s of %s", ErrNotFound, version, id)
		}
		r := p.Releases[i]
		if err := fits(r); err != nil {
			return Release{}, Artifact{}, err
		}
		return r, r.Artifacts[platform], nil
	}
	var first error
	for _, r := range p.Releases {
		err := fits(r)
		if err == nil {
			return r, r.Artifacts[platform], nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		first = fmt.Errorf("%w: %s lists no releases", ErrNotFound, id)
	}
	return Release{}, Artifact{}, fmt.Errorf("no release of %s runs here: %w", id, first)
}

func RedactURL(loc string) string {
	if !isHTTPS(loc) {
		return loc
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "(an https:// URL that does not parse)"
	}
	u.User, u.Fragment = nil, ""
	if u.RawQuery != "" {
		u.RawQuery = "redacted"
	}
	return u.String()
}

func isHTTPS(loc string) bool { return strings.HasPrefix(strings.ToLower(loc), "https://") }

func CheckLocation(loc string) error {
	if isHTTPS(loc) {
		u, err := url.Parse(loc)
		if err != nil || u.Host == "" {
			return fmt.Errorf("registry URL %q is not a valid https:// URL", RedactURL(loc))
		}
		return nil
	}
	if u, err := url.Parse(loc); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		return fmt.Errorf("registry %s://…: only https:// URLs and local paths are allowed", u.Scheme)
	}
	if !filepath.IsAbs(loc) {
		return fmt.Errorf("registry path %q must be absolute", loc)
	}
	return nil
}

func resolve(index, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%w: archive url is not valid", ErrRefused)
	}
	if u.Scheme != "" {
		if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
			return "", fmt.Errorf("%w: archive url %s://…: only https:// URLs or paths relative to the index are allowed", ErrRefused, u.Scheme)
		}
		return u.String(), nil
	}
	if isHTTPS(index) {
		base, err := url.Parse(index)
		if err != nil {
			return "", fmt.Errorf("%w: registry URL %s is not valid", ErrRefused, RedactURL(index))
		}
		out := base.ResolveReference(u)
		if !strings.EqualFold(out.Scheme, "https") {
			return "", fmt.Errorf("%w: archive url %s does not resolve to an https:// URL", ErrRefused, RedactURL(out.String()))
		}
		return out.String(), nil
	}
	if u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !filepath.IsLocal(filepath.FromSlash(u.Path)) || strings.Contains(u.Path, `\`) {
		return "", fmt.Errorf("%w: archive url %q must be a path inside the local registry's folder", ErrRefused, u.Path)
	}
	return filepath.Join(filepath.Dir(index), filepath.FromSlash(u.Path)), nil
}

func DefaultClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	return &http.Client{Transport: t, CheckRedirect: httpsRedirectsOnly}
}

func httpsRedirectsOnly(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s: only https:// is allowed", RedactURL("https://"+req.URL.Host+req.URL.Path))
	}
	return nil
}

func open(ctx context.Context, client *http.Client, loc string, limit int64) (io.ReadCloser, error) {
	if !isHTTPS(loc) {
		f, err := os.Open(loc)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrFetch, err)
		}
		return f, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a valid URL", ErrFetch, RedactURL(loc))
	}
	req.Header.Set("Accept-Encoding", "identity")
	if client.CheckRedirect == nil {
		c := *client
		c.CheckRedirect = httpsRedirectsOnly
		client = &c
	}
	resp, err := client.Do(req)
	if err != nil {
		// A *url.Error names the whole URL, query included.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: GET %s: %v", ErrFetch, RedactURL(loc), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: GET %s: %s", ErrFetch, RedactURL(loc), resp.Status)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s is %d bytes, more than the %d allowed", ErrRefused, RedactURL(loc), resp.ContentLength, limit)
	}
	return resp.Body, nil
}

var IndexTimeout = 30 * time.Second

func ReadIndex(ctx context.Context, client *http.Client, r Registry) (*Index, error) {
	if client == nil {
		client = DefaultClient()
	}
	if err := CheckLocation(r.URL); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, IndexTimeout)
	defer cancel()
	timedOut := func(err error) error {
		if parent.Err() == nil && ctx.Err() != nil {
			return fmt.Errorf("registry %s: %w: GET %s: no answer within %s", r.Name, ErrFetch, RedactURL(r.URL), IndexTimeout)
		}
		return err
	}
	body, err := open(ctx, client, r.URL, maxIndex)
	if err != nil {
		return nil, timedOut(fmt.Errorf("registry %s: %w", r.Name, err))
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, maxIndex+1))
	if err != nil {
		return nil, timedOut(fmt.Errorf("registry %s: %w: %v", r.Name, ErrFetch, err))
	}
	if len(b) > maxIndex {
		return nil, fmt.Errorf("registry %s: %w: the index is larger than %d bytes", r.Name, ErrRefused, maxIndex)
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, fmt.Errorf("registry %s: %w: the index is not valid JSON: %v", r.Name, ErrRefused, err)
	}
	if err := ix.validate(); err != nil {
		return nil, fmt.Errorf("registry %s: %w: %v", r.Name, ErrRefused, err)
	}
	return &ix, nil
}
