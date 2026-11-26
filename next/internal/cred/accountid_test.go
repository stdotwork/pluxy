package cred

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

// A login started without an account ID adds the account under a new ID, acc_ and 8 hex digits
// from the random source, drawn again while it is an ID or public ID an account core knows has
// (letter case aside for the ID), or an account outside the store (core's config) has.
func TestLoginWithoutIDGetsAFreshID(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)
	if _, err := m.store.Create(Record{ID: "ACC_00000001", Extension: provID, State: StateActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Create(Record{ID: "stored", Extension: provID, State: StateActive}); err != nil {
		t.Fatal(err)
	}
	m.publicID = func(id string) string {
		if id == "stored" {
			return "acc_00000002"
		}
		return id
	}
	m.taken = func(id string) bool { return id == "acc_00000003" }
	draws := []string{"00000001", "00000002", "00000003", "0a1b2c3d"}
	var src []byte
	for _, d := range draws {
		b, _ := hex.DecodeString(d)
		src = append(src, b...)
	}
	m.random = bytes.NewReader(src)

	v, err := m.StartLogin(context.Background(), LoginRequest{Method: "device", Label: "Work"})
	if err != nil || v.State != LoginDone || v.Account != "acc_0a1b2c3d" {
		t.Fatalf("StartLogin without an ID = %+v, %v; want account acc_0a1b2c3d, done", v, err)
	}
	if r, ok := m.store.Get("acc_0a1b2c3d"); !ok || r.Label != "Work" || r.ProviderLabel != "jane@example.test" {
		t.Fatalf("stored %+v (found %v)", r, ok)
	}
	if _, err := m.StartLogin(context.Background(), LoginRequest{Method: "device", Reauth: true}); err == nil {
		t.Fatal("a new login without an account ID was started")
	}
}

// SetAccount sets the label a person gives an account and, given "", takes it away, leaving the
// provider's. An account stored before core kept the provider's label apart is still checked by
// its old label on a new login after a person renamed it.
func TestSetAccountLabel(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)
	runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "work"}})
	label := func(s string) *string { return &s }
	if r, err := m.SetAccount("work", nil, nil, label("Work laptop")); err != nil || r.Label != "Work laptop" || r.ProviderLabel != "jane@example.test" {
		t.Fatalf("SetAccount label = %+v, %v", r, err)
	}
	if r, err := m.SetAccount("work", nil, nil, label("")); err != nil || r.Label != "jane@example.test" {
		t.Fatalf("SetAccount label \"\" = %+v, %v; want the provider's label", r, err)
	}
	if _, err := m.SetAccount("nobody", nil, nil, label("x")); err != ErrNotFound {
		t.Fatalf("SetAccount of an unknown account: %v", err)
	}

	// An account stored before: its label is the provider's, with no ProviderLabel.
	if _, err := m.store.Create(Record{ID: "old", Extension: provID, State: StateActive, Label: "old@example.test"}); err != nil {
		t.Fatal(err)
	}
	if r, err := m.SetAccount("old", nil, nil, label("Old one")); err != nil || r.Label != "Old one" {
		t.Fatalf("renaming the old account: %+v, %v", r, err)
	}
	p.grant = grantLabelled("OLD@example.test", nil)
	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "old"}, Reauth: true}); v.State != LoginDone {
		t.Fatalf("a new login to the renamed old account ended %s: %+v", v.State, v.Error)
	}
	p.grant = grantLabelled("other@example.test", nil)
	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "old"}, Reauth: true}); v.State != LoginFailed {
		t.Fatalf("a new login to the renamed old account as someone else ended %s, want failed", v.State)
	}
	if r, _ := m.store.Get("old"); r.Label != "Old one" || !strings.EqualFold(r.ProviderLabel, "old@example.test") {
		t.Fatalf("old account after the logins: %+v", r)
	}
}

// A login started with an ID of the shape core makes (acc_ and 8 lower-case hex digits), which is
// its own public ID, is refused when that ID is another account's public ID, stored or set in
// core's config, as a drawn ID would be drawn again; so is an imported account of such an ID. A
// free one is taken.
func TestSuppliedCoreShapedIDMustBeFree(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)
	state := route.NewState()
	m.publicID = state.PublicID
	if _, err := m.store.Create(Record{ID: "jane.example", Extension: provID, State: StateActive}); err != nil {
		t.Fatal(err)
	}
	stored, config := state.PublicID("jane.example"), state.PublicID("from-config")
	m.taken = func(id string) bool { return id == "from-config" || id == config }

	for _, id := range []string{stored, config} {
		if _, err := m.Create(Record{ID: id, Extension: provID, State: StateActive}); !errors.Is(err, ErrConflict) {
			t.Errorf("importing account %s, another account's public ID: %v; want a conflict", id, err)
		}
		if v, err := m.StartLogin(context.Background(), LoginRequest{Account: route.Account{ID: id}, Method: "device"}); !errors.Is(err, ErrConflict) {
			t.Errorf("a login to new account %s, another account's public ID: %+v, %v; want a conflict", id, v, err)
		}
		if _, ok := m.store.Get(id); ok {
			t.Errorf("account %s was added", id)
		}
	}
	if v, err := m.StartLogin(context.Background(), LoginRequest{Account: route.Account{ID: "acc_0a1b2c3d"}, Method: "device"}); err != nil || v.State != LoginDone {
		t.Fatalf("a login to new account acc_0a1b2c3d: %+v, %v", v, err)
	}
}

// A login to a new account fails, and adds nothing, when core's config comes to have its ID or
// its public ID as an account's ID or public ID while the login runs. The login reports a
// conflict, as a refusal at its start does, not an internal error.
func TestLoginRefusedWhenConfigTakesItsIDMeanwhile(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)
	state := route.NewState()
	m.publicID = state.PublicID
	var configID atomic.Value
	configID.Store("")
	m.taken = func(id string) bool {
		c := configID.Load().(string)
		return c != "" && (strings.EqualFold(c, id) || state.PublicID(c) == id)
	}

	for _, tc := range []struct{ id, config string }{
		{"acc_0a1b2c3d", "acc_0a1b2c3d"}, // the config takes the ID itself
		{"foo", state.PublicID("foo")},   // the config takes the ID's public ID
		{state.PublicID("bar"), "bar"},   // the ID is the public ID of the config's account
	} {
		configID.Store("")
		p.onLogin = func() { configID.Store(tc.config) } // free when the login starts, taken after
		v, err := m.StartLogin(context.Background(), LoginRequest{Account: route.Account{ID: tc.id}, Method: "device"})
		if err != nil || v.State != LoginFailed || v.Error == nil {
			t.Fatalf("a login to %s whose ID the config took meanwhile (as %s) = %+v, %v; want failed", tc.id, tc.config, v, err)
		}
		if v.Error.Code != llm.CodeInvalidRequest || !strings.Contains(v.Error.Message, "conflict") {
			t.Errorf("a login to %s whose ID the config took meanwhile failed with %s: %s; want a conflict", tc.id, v.Error.Code, v.Error.Message)
		}
		if _, ok := m.store.Get(tc.id); ok {
			t.Fatalf("account %s was added", tc.id)
		}
	}
}

// The ID check works both ways: a new account, by login or by import, is refused while its public
// ID is another account's ID, not only while its ID is another account's public ID. Account foo
// is added, removed, and an account is added whose ID is foo's public ID; adding foo again is then
// refused. The same holds when that account is set in core's config. The refusal does not tell an
// import to leave the ID out, which an import cannot do.
func TestNewAccountWhosePublicIDIsTakenIsRefused(t *testing.T) {
	p := &immediateLoginProvider{fakeProvider: &fakeProvider{}, grant: grantLabelled("jane@example.test", nil)}
	m := loginManager(t, p)
	state := route.NewState()
	m.publicID = state.PublicID

	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: "foo"}}); v.State != LoginDone {
		t.Fatalf("adding foo: %+v", v)
	}
	pub := state.PublicID("foo")
	if err := m.Remove("foo"); err != nil {
		t.Fatal(err)
	}
	if v := runLoginTo(t, m, LoginRequest{Account: route.Account{ID: pub}}); v.State != LoginDone {
		t.Fatalf("adding %s, foo's public ID, once foo is gone: %+v", pub, v)
	}
	configID := state.PublicID("bar")
	m.taken = func(id string) bool { return strings.EqualFold(configID, id) || state.PublicID(configID) == id }

	for _, id := range []string{"foo", "bar"} {
		_, err := m.Create(Record{ID: id, Extension: provID, State: StateActive})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("importing %s, whose public ID %s is another account's ID: %v; want a conflict", id, state.PublicID(id), err)
		} else if strings.Contains(err.Error(), "leave the ID out") {
			t.Errorf("importing %s: the refusal tells an import to leave the ID out: %v", id, err)
		}
		if v, err := m.StartLogin(context.Background(), LoginRequest{Account: route.Account{ID: id}, Method: "device"}); !errors.Is(err, ErrConflict) {
			t.Errorf("a login to new account %s, whose public ID is another account's ID: %+v, %v; want a conflict", id, v, err)
		}
		if _, ok := m.store.Get(id); ok {
			t.Errorf("account %s was added", id)
		}
	}
}
