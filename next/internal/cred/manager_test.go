package cred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The provider extension is the edge of core, so these tests replace it with a scripted
// token endpoint: each refresh waits until the test lets it answer, then rotates the refresh
// token the way RFC 9700 section 4.14.2 describes, refusing a refresh token used before.

const provID = "test/oauth"

type fakeProvider struct {
	mu      sync.Mutex
	calls   int
	used    map[string]bool
	gate    chan struct{} // each refresh waits for a receive, when not nil
	started chan struct{} // gets a value as each refresh starts
	fail    *llm.Error    // returned instead of a credential, when set
	ttl     time.Duration // lifetime of issued access tokens
}

func (p *fakeProvider) Info() protocol.Info {
	return protocol.Info{ID: provID, Operations: []string{protocol.OpGenerate, protocol.OpLogin, protocol.OpRefresh},
		LoginMethods: []protocol.LoginMethod{{Method: "device", Kind: protocol.LoginDevice}}}
}

func (p *fakeProvider) Login(context.Context, protocol.Account, protocol.Login) LoginCall {
	panic("not used")
}

func (p *fakeProvider) Refresh(ctx context.Context, a protocol.Account) (*protocol.Credential, *llm.Error) {
	if p.started != nil {
		p.started <- struct{}{}
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, &llm.Error{Code: llm.CodeCancelled, Message: "cancelled"}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail != nil {
		return nil, p.fail
	}
	if p.used == nil {
		p.used = map[string]bool{}
	}
	old := a.Credential.RefreshToken
	if p.used[old] {
		return nil, &llm.Error{Code: llm.CodeAuth, Scope: llm.ScopeCredential, Message: "refresh token reused"}
	}
	p.used[old] = true
	ttl := p.ttl
	if ttl == 0 {
		ttl = time.Hour
	}
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: fmt.Sprintf("access-%d", p.calls),
		RefreshToken: fmt.Sprintf("refresh-%d", p.calls), ExpiresAt: time.Now().Add(ttl).Unix()}, nil
}

// single is the one provider extension that runs.
type single struct{ p Provider }

func (s single) Provider(id string) Provider {
	if id == provID {
		return s.p
	}
	return nil
}

func (s single) Running() []string { return []string{provID} }

func (p *fakeProvider) refreshCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// newManager returns a manager over a file store in a temporary folder holding one account
// whose access token expires in expiresIn.
func newManager(t *testing.T, p *fakeProvider, expiresIn time.Duration) (*Manager, *Store) {
	t.Helper()
	store, err := Open(FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(Record{ID: "acct", Extension: provID, State: StateActive, Credential: protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: "access-0", RefreshToken: "refresh-0", ExpiresAt: time.Now().Add(expiresIn).Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, single{p}, Options{})
	t.Cleanup(m.Close)
	return m, store
}

func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
}

// Many requests that find the credential about to expire share one renewal: one refresh
// call, and every request gets the renewed credential.
func TestConcurrentRequestsShareOneRenewal(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 100)}
	m, _ := newManager(t, p, time.Minute)
	const n = 20
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]*llm.Error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, e := m.Credential(context.Background(), provID, "acct")
			errs[i] = e
			if c != nil {
				got[i] = c.AccessToken
			}
		}()
	}
	wait(t, p.started)
	// Let every caller reach the renewal before it answers.
	time.Sleep(100 * time.Millisecond)
	close(p.gate)
	wg.Wait()
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
	for i := range n {
		if errs[i] != nil || got[i] != "access-1" {
			t.Errorf("caller %d got %q, %v; want access-1", i, got[i], errs[i])
		}
	}
	// Later requests use the renewed credential without renewing again.
	if c, v, e := m.Credential(context.Background(), provID, "acct"); e != nil || c.AccessToken != "access-1" || v != 2 {
		t.Errorf("after the renewal: %v version %d %v, want access-1 at version 2", c, v, e)
	}
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d after the renewal, want 1", c)
	}
}

// A renewal whose account was signed in to again while it ran is refused: the newer
// credential stands.
func TestRenewalOfReplacedCredentialIsRefused(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	m, store := newManager(t, p, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Credential(context.Background(), provID, "acct")
	}()
	wait(t, p.started)
	cur, _ := store.Get("acct")
	if _, err := store.Update("acct", cur.Registration, cur.Version, func(r *Record) {
		r.Credential = protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "access-login", RefreshToken: "refresh-login", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	}); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	wait(t, done)
	rec, _ := store.Get("acct")
	if rec.Credential.AccessToken != "access-login" || rec.Version != 2 {
		t.Errorf("record holds %q at version %d; want the login's access-login at version 2", rec.Credential.AccessToken, rec.Version)
	}
	reloaded, err := Open(store.b)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := reloaded.Get("acct"); r.Credential.AccessToken != "access-login" {
		t.Errorf("stored file holds %q, want access-login", r.Credential.AccessToken)
	}
}

// A renewal still running when its account is removed and added again under the same ID
// never lands on the new account.
func TestRenewalOfRemovedAccountIsRefused(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	m, store := newManager(t, p, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Credential(context.Background(), provID, "acct")
	}()
	wait(t, p.started)
	if err := m.Remove("acct"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "acct", Extension: provID, State: StateActive, Credential: protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: "access-new", RefreshToken: "refresh-new", ExpiresAt: time.Now().Add(time.Hour).Unix()}}); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	wait(t, done)
	rec, _ := store.Get("acct")
	if rec.Credential.AccessToken != "access-new" || rec.Version != 1 {
		t.Errorf("record holds %q at version %d; want the new account's access-new at version 1", rec.Credential.AccessToken, rec.Version)
	}
}

// A caller that read the credential before a renewal finished does not renew it again.
func TestCallerWithOldVersionDoesNotRenewAgain(t *testing.T) {
	p := &fakeProvider{}
	m, store := newManager(t, p, time.Minute)
	before, _ := store.Get("acct")
	if _, err := m.Refresh(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	// The upstream refused the old credential: the renewed one is ready without a second call.
	if !m.Rejected(context.Background(), "acct", before.Version) {
		t.Error("Rejected with the old version reported no newer credential")
	}
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
}

// Requests that see the upstream refuse the same credential share one renewal.
func TestConcurrentRejectionsShareOneRenewal(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 100)}
	m, store := newManager(t, p, time.Hour)
	rec, _ := store.Get("acct")
	var wg sync.WaitGroup
	var fresh atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Rejected(context.Background(), "acct", rec.Version) {
				fresh.Add(1)
			}
		}()
	}
	wait(t, p.started)
	time.Sleep(100 * time.Millisecond)
	close(p.gate)
	wg.Wait()
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
	if fresh.Load() != 10 {
		t.Errorf("%d of 10 callers got a renewed credential", fresh.Load())
	}
}

// A refused grant leaves the account waiting for a new login: not usable, not routed, and
// not the same as a user's disable.
func TestRejectedGrantNeedsLogin(t *testing.T) {
	p := &fakeProvider{fail: &llm.Error{Code: llm.CodeAuth, Scope: llm.ScopeCredential, Message: "invalid_grant"}}
	m, store := newManager(t, p, time.Minute)
	changed := 0
	m.OnChange(func() { changed++ })
	if _, _, e := m.Credential(context.Background(), provID, "acct"); e == nil || e.Code != llm.CodeAuth {
		t.Fatalf("Credential = %v, want code auth", e)
	}
	rec, _ := store.Get("acct")
	if rec.State != StateReauth || rec.Disabled || rec.Reason != "invalid_grant" {
		t.Errorf("state %q disabled %v reason %q, want %q, not disabled, reason invalid_grant", rec.State, rec.Disabled, rec.Reason, StateReauth)
	}
	if len(m.Accounts()) != 0 || changed != 1 {
		t.Errorf("routable accounts %v, change notices %d; want none and 1", m.Accounts(), changed)
	}
	if _, _, e := m.Credential(context.Background(), provID, "acct"); e == nil || e.Code != llm.CodeAuth {
		t.Errorf("Credential after the refusal = %v, want code auth", e)
	}
	// Enabling is about the user's disable, not the refusal.
	if rec, _ := m.SetDisabled("acct", false); rec.State != StateReauth {
		t.Errorf("enabling cleared the refusal: state %q", rec.State)
	}
}

// After a renewal fails in a way that may pass, requests are not each sent to the token
// endpoint: renewal waits, and an expired credential fails with when to try again.
func TestFailedRenewalBacksOff(t *testing.T) {
	p := &fakeProvider{fail: &llm.Error{Code: llm.CodeUnavailable, Retryable: true, Scope: llm.ScopeEndpoint, Message: "token endpoint down"}}
	m, store := newManager(t, p, -time.Minute)
	for range 3 {
		_, _, e := m.Credential(context.Background(), provID, "acct")
		if e == nil || e.Code != llm.CodeUnavailable || !e.Retryable || e.Scope != llm.ScopeCredential || e.RetryAfterMS <= 0 {
			t.Errorf("Credential = %+v, want a retryable credential-scoped unavailable with retry_after_ms", e)
		}
	}
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
	if rec, _ := store.Get("acct"); rec.State != StateActive || rec.Version != 1 {
		t.Errorf("a passing failure changed the record: state %q version %d", rec.State, rec.Version)
	}
	// An explicit refresh does not wait for the backoff.
	p.mu.Lock()
	p.fail = nil
	p.mu.Unlock()
	if rec, err := m.Refresh(context.Background(), "acct"); err != nil || rec.Credential.AccessToken != "access-2" {
		t.Errorf("Refresh = %q, %v; want access-2", rec.Credential.AccessToken, err)
	}
}

// A renewal that gives a credential already due for renewal is not repeated on every request.
func TestIneffectiveRenewalBacksOff(t *testing.T) {
	p := &fakeProvider{ttl: time.Minute}
	m, _ := newManager(t, p, time.Minute)
	for range 3 {
		if _, _, e := m.Credential(context.Background(), provID, "acct"); e != nil {
			t.Fatal(e)
		}
	}
	if c := p.refreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
}

// A caller that stops waiting does not cancel the renewal: the provider may already have
// rotated the refresh token, and the result must still be stored.
func TestRenewalOutlivesItsCaller(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	m, store := newManager(t, p, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *llm.Error, 1)
	go func() {
		_, _, e := m.Credential(ctx, provID, "acct")
		done <- e
	}()
	wait(t, p.started)
	cancel()
	if e := <-done; e == nil || e.Code != llm.CodeCancelled {
		t.Errorf("cancelled caller got %v, want cancelled", e)
	}
	close(p.gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if rec, _ := store.Get("acct"); rec.Credential.RefreshToken == "refresh-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the renewal's rotated refresh token was not stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Credentials go to their own provider only, and static keys are never renewed.
func TestOnlyOwnCredentials(t *testing.T) {
	p := &fakeProvider{}
	m, store := newManager(t, p, time.Minute)
	if _, err := store.Create(Record{ID: "other", Extension: "someone/else", State: StateActive,
		Credential: protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "theirs", RefreshToken: "theirs-r", ExpiresAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "key", Extension: provID, State: StateActive,
		Credential: protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k", ExpiresAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if c, _, e := m.Credential(context.Background(), provID, "other"); e == nil || c != nil {
		t.Errorf("another extension's credential was handed out: %v %v", c, e)
	}
	if c, _, e := m.Credential(context.Background(), provID, "key"); e != nil || c.APIKey != "k" {
		t.Errorf("api key: %v %v", c, e)
	}
	if c := p.refreshCalls(); c != 0 {
		t.Errorf("refresh calls = %d, want 0", c)
	}
	// Every usable account is routable, each tagged with its own extension: routing sends it
	// only there.
	var ids []string
	for _, a := range m.Accounts() {
		ids = append(ids, a.ID+"@"+a.Extension)
		if !a.Managed || a.Credential != nil {
			t.Errorf("routable account %+v must be managed and carry no credential", a)
		}
	}
	if fmt.Sprint(ids) != "[acct@test/oauth key@test/oauth other@someone/else]" {
		t.Errorf("routable accounts = %v", ids)
	}
}

// The file backend keeps each record readable only by its owner and leaves no partial files.
func TestFileBackendPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Create(Record{ID: "acct", Extension: provID, State: StateActive, Account: route.Account{Models: []route.ModelEntry{{Name: "m"}}},
		Credential: protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("acct", rec.Registration, rec.Version, func(r *Record) { r.Label = "x" }); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, want 0700", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "acct.json" {
		t.Fatalf("folder holds %v, want only acct.json", entries)
	}
	st, _ := os.Stat(filepath.Join(dir, "acct.json"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "acct.json"))
	var onDisk Record
	if err := json.Unmarshal(b, &onDisk); err != nil || onDisk.Version != 2 || onDisk.Label != "x" || onDisk.Account.ID != "acct" {
		t.Errorf("file holds %s", b)
	}
	if _, err := store.Create(Record{ID: "../escape", Extension: provID}); err == nil {
		t.Error("an ID that leaves the folder was accepted")
	}
}

// A provider's refresh_at, earlier than the default lead, is when the credential is renewed.
func TestRefreshAtSetsRenewalTime(t *testing.T) {
	p := &fakeProvider{}
	m, store := newManager(t, p, 24*time.Hour)
	rec, _ := store.Get("acct")
	if _, err := store.Update("acct", rec.Registration, rec.Version, func(r *Record) {
		r.Credential.RefreshAt = time.Now().Add(-time.Second).Unix()
	}); err != nil {
		t.Fatal(err)
	}
	if c, _, e := m.Credential(context.Background(), provID, "acct"); e != nil || c.AccessToken != "access-1" {
		t.Errorf("Credential = %v, %v; want the renewed access-1", c, e)
	}
}

// Disabling an account while its credential is being renewed keeps the renewed credential:
// the provider has already retired the old refresh token.
func TestDisableDuringRenewalKeepsRotatedToken(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 2)}
	m, store := newManager(t, p, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Credential(context.Background(), provID, "acct")
	}()
	wait(t, p.started)
	if _, err := m.SetDisabled("acct", true); err != nil {
		t.Fatal(err)
	}
	p.gate <- struct{}{}
	wait(t, done)
	rec, _ := store.Get("acct")
	if rec.Credential.RefreshToken != "refresh-1" || !rec.Disabled || rec.State != StateActive {
		t.Fatalf("record: refresh %q disabled %v state %q; want refresh-1, disabled, active", rec.Credential.RefreshToken, rec.Disabled, rec.State)
	}
	if _, err := m.SetDisabled("acct", false); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	if rec, err := m.Refresh(context.Background(), "acct"); err != nil || rec.Credential.RefreshToken != "refresh-2" {
		t.Errorf("renewing after enabling: %q, %v; want refresh-2", rec.Credential.RefreshToken, err)
	}
}

// A renewal that fails after the credential it renews was replaced (a new login) leaves no
// backoff on the new credential.
func TestStaleFailureLeavesNewCredentialAlone(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 2),
		fail: &llm.Error{Code: llm.CodeUnavailable, Retryable: true, Message: "old grant failed"}}
	m, store := newManager(t, p, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Credential(context.Background(), provID, "acct")
	}()
	wait(t, p.started)
	cur, _ := store.Get("acct")
	if _, err := store.Update("acct", cur.Registration, cur.Version, func(r *Record) {
		r.Credential = protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "access-login", RefreshToken: "refresh-login", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	}); err != nil {
		t.Fatal(err)
	}
	p.gate <- struct{}{}
	wait(t, done)
	p.mu.Lock()
	p.fail = nil
	p.mu.Unlock()
	close(p.gate)
	// The new credential is due for renewal: it is renewed, not held back by the old failure.
	if c, _, e := m.Credential(context.Background(), provID, "acct"); e != nil || c.AccessToken != "access-2" {
		t.Errorf("Credential = %v, %v; want the renewed access-2", c, e)
	}
}

// failingBackend refuses saves once fail is set.
type failingBackend struct {
	Backend
	fail atomic.Bool
}

func (b *failingBackend) Save(r Record) error {
	if b.fail.Load() {
		return fmt.Errorf("disk full")
	}
	return b.Backend.Save(r)
}

// When the provider refuses the grant and recording that fails, the refusal is still
// reported, not taken for success.
func TestRejectedGrantSaveFailureIsReported(t *testing.T) {
	b := &failingBackend{Backend: FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")}}
	store, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "acct", Extension: provID, State: StateActive, Credential: protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()}}); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{fail: &llm.Error{Code: llm.CodeAuth, Scope: llm.ScopeCredential, Message: "invalid_grant"}}
	m := NewManager(store, single{p}, Options{})
	t.Cleanup(m.Close)
	b.fail.Store(true)
	if _, err := m.Refresh(context.Background(), "acct"); err == nil {
		t.Error("Refresh succeeded although the provider refused the grant")
	}
	// The refused credential is not handed out afterwards either.
	if c, _, e := m.Credential(context.Background(), provID, "acct"); e == nil || e.Code != llm.CodeAuth {
		t.Errorf("Credential after the unrecorded refusal = %v, %v; want code auth", c, e)
	}
}

// A failure recorded against one credential does not hold back another that replaced it,
// however the replacement got there.
func TestFailureBacksOffOnlyItsCredential(t *testing.T) {
	p := &fakeProvider{fail: &llm.Error{Code: llm.CodeUnavailable, Retryable: true, Message: "old grant failed"}}
	m, store := newManager(t, p, time.Minute)
	if _, _, e := m.Credential(context.Background(), provID, "acct"); e != nil {
		t.Fatalf("Credential = %v; the old credential still works", e)
	}
	cur, _ := store.Get("acct")
	if _, err := store.Update("acct", cur.Registration, cur.Version, func(r *Record) {
		r.Credential = protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "access-login", RefreshToken: "refresh-login", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.fail = nil
	p.mu.Unlock()
	if c, _, e := m.Credential(context.Background(), provID, "acct"); e != nil || c.AccessToken != "access-2" {
		t.Errorf("Credential = %v, %v; want the replacement renewed (access-2)", c, e)
	}
}

// IDs that differ only in case would share a file on macOS and Windows, so the second is
// refused.
func TestIDsDifferingInCaseAreOneAccount(t *testing.T) {
	store, err := Open(FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "Work", Extension: provID, State: StateActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "work", Extension: provID, State: StateActive}); err != ErrExists {
		t.Errorf("creating \"work\" beside \"Work\": %v, want ErrExists", err)
	}
}

// slowLoginProvider's logins take until start is closed to begin, then prompt once and wait
// until cancelled.
type slowLoginProvider struct {
	fakeProvider
	start   chan struct{}
	calling chan struct{}
}

type waitingLogin struct {
	ctx    context.Context
	n      int
	inputs chan map[string]string
}

func (l *waitingLogin) Next() protocol.Reply {
	l.n++
	if l.n == 1 {
		return protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &protocol.Prompt{Kind: protocol.PromptInput}}
	}
	<-l.ctx.Done()
	return protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeCancelled, Message: "cancelled"}}}
}
func (l *waitingLogin) Send(in map[string]string) error { l.inputs <- in; return nil }
func (l *waitingLogin) Close()                          {}

func (p *slowLoginProvider) Login(ctx context.Context, _ protocol.Account, _ protocol.Login) LoginCall {
	close(p.calling)
	<-p.start
	return &waitingLogin{ctx: ctx, inputs: make(chan map[string]string, 1)}
}

// Input for a login that is still starting is refused, not sent to a call that does not exist
// yet.
func TestInputWhileLoginStarts(t *testing.T) {
	p := &slowLoginProvider{start: make(chan struct{}), calling: make(chan struct{})}
	store, err := Open(FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, single{p}, Options{})
	t.Cleanup(m.Close)
	started := make(chan error, 1)
	go func() {
		_, err := m.StartLogin(context.Background(), LoginRequest{Account: route.Account{ID: "new"}, Method: "device"})
		started <- err
	}()
	wait(t, p.calling)
	if err := m.LoginInput("login-1", map[string]string{"x": "y"}); !errors.Is(err, ErrConflict) {
		t.Errorf("input while starting: %v, want ErrConflict", err)
	}
	close(p.start)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := m.LoginInput("login-1", map[string]string{"x": "y"}); err != nil {
		t.Errorf("input once started: %v", err)
	}
	m.CancelLogin("login-1")
}

// An account removed while its credential is being renewed, and added again under the same ID
// for another extension, never has the new account's credential handed to the first extension.
func TestRenewalNeverReturnsAnotherExtensionsCredential(t *testing.T) {
	p := &fakeProvider{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	m, store := newManager(t, p, time.Minute) // due for renewal
	type result struct {
		c *protocol.Credential
		e *llm.Error
	}
	done := make(chan result, 1)
	go func() {
		c, _, e := m.Credential(context.Background(), provID, "acct")
		done <- result{c, e}
	}()
	wait(t, p.started)
	if err := m.Remove("acct"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "acct", Extension: "someone/else", State: StateActive,
		Credential: protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "theirs"}}); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	r := <-done
	if r.c != nil {
		t.Fatalf("%s was handed %+v, the credential of another extension's account", provID, r.c)
	}
	if r.e == nil {
		t.Error("no error for an account that was removed")
	}
}

// A new login that names the registration its caller checked is refused, before the provider is
// asked, once the account has been removed and added again.
func TestReauthOfAnotherRegistrationIsRefused(t *testing.T) {
	m, store := newManager(t, &fakeProvider{}, time.Hour)
	rec, _ := store.Get("acct")
	if err := store.Delete("acct"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Record{ID: "acct", Extension: provID, State: StateActive, Credential: protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}); err != nil {
		t.Fatal(err)
	}
	// fakeProvider.Login panics: reaching the provider fails the test. The deadline keeps a
	// login that did start from waiting for a first prompt that never comes.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := m.StartLogin(ctx, LoginRequest{Account: route.Account{ID: "acct"}, Method: "device", Reauth: true, Registration: rec.Registration})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("StartLogin = %v, want ErrConflict", err)
	}
}
