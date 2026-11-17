package cred

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Reading what the file system holds for a path: its owner and access list. Declared here
// rather than taken from file_windows.go, so the test does not rest on the code it checks.
var (
	testAdvapi32                     = syscall.NewLazyDLL("advapi32.dll")
	procGetNamedSecurityInfo         = testAdvapi32.NewProc("GetNamedSecurityInfoW")
	procGetSecurityDescriptorControl = testAdvapi32.NewProc("GetSecurityDescriptorControl")
	procGetAce                       = testAdvapi32.NewProc("GetAce")
)

const (
	testSEFileObject         = 1 // SE_FILE_OBJECT
	ownerSecurityInformation = 0x1
	daclSecurityInfo         = 0x4
	seDaclProtected          = 0x1000
	everyone                 = "S-1-1-0"
	system                   = "S-1-5-18"
)

// aclHeader is the start of an ACL; allowedACE is an ACCESS_ALLOWED_ACE (other kinds share its
// first eight bytes).
type aclHeader struct {
	Revision, _ byte
	Size        uint16
	Count       uint16
	_           uint16
}

type allowedACE struct {
	Type, Flags byte
	Size        uint16
	Mask        uint32
	SidStart    uint32
}

// accessList reads path's owner, whether its access list is protected (inherits nothing from
// its parent), and its entries as "SID type flags mask", sorted.
func accessList(t *testing.T, path string) (owner string, protected bool, entries []string) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var ownerSID *syscall.SID
	var dacl *aclHeader
	var sd uintptr
	if r, _, _ := procGetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(p)), testSEFileObject,
		ownerSecurityInformation|daclSecurityInfo, uintptr(unsafe.Pointer(&ownerSID)), 0,
		uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		t.Fatalf("%s: %v", path, syscall.Errno(r))
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	if owner, err = ownerSID.String(); err != nil {
		t.Fatal(err)
	}
	var control uint16
	var revision uint32
	if r, _, e := procGetSecurityDescriptorControl.Call(sd, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision))); r == 0 {
		t.Fatalf("%s: %v", path, e)
	}
	if dacl == nil {
		t.Fatalf("%s has no access list, so anyone may open it", path)
	}
	for i := range uint32(dacl.Count) {
		var ace *allowedACE
		if r, _, e := procGetAce.Call(uintptr(unsafe.Pointer(dacl)), uintptr(i), uintptr(unsafe.Pointer(&ace))); r == 0 {
			t.Fatalf("%s: %v", path, e)
		}
		sid, err := (*syscall.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry(sid, ace.Type, ace.Flags, ace.Mask))
	}
	slices.Sort(entries)
	return owner, control&seDaclProtected != 0, entries
}

func entry(sid string, typ, flags byte, mask uint32) string {
	return fmt.Sprintf("%s type %d flags %#x mask %#x", sid, typ, flags, mask)
}

// thisUser returns this process's user SID and its token's default owner, the owner Windows
// gives what it makes.
func thisUser(t *testing.T) (me, defaultOwner string) {
	t.Helper()
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if me, err = user.User.Sid.String(); err != nil {
		t.Fatal(err)
	}
	var n uint32
	syscall.GetTokenInformation(token, syscall.TokenOwner, nil, 0, &n) // fails with the size in n
	buf := make([]uintptr, int(n)/int(unsafe.Sizeof(uintptr(0)))+1)
	if err := syscall.GetTokenInformation(token, syscall.TokenOwner, (*byte)(unsafe.Pointer(&buf[0])), n, &n); err != nil {
		t.Fatal(err)
	}
	if defaultOwner, err = (*struct{ Owner *syscall.SID })(unsafe.Pointer(&buf[0])).Owner.String(); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(buf)
	return me, defaultOwner
}

// The credentials folder, a new record's file and the file after a save each have a protected
// access list (nothing inherited from the parent) that lets in only this user and SYSTEM, with
// full access; the folder's entries pass on to what is made in it. A folder that already
// exists, open to Everyone, is made private by the next save, and a file already in it loses
// the entry it inherited.
func TestFileBackendWindowsAccessList(t *testing.T) {
	me, defaultOwner := thisUser(t)
	const (
		allowed     = 0          // ACCESS_ALLOWED_ACE_TYPE
		inheritable = 0x1 | 0x2  // OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE; an inherited entry also has 0x10
		fullAccess  = 0x001F01FF // FILE_ALL_ACCESS
	)
	want := func(flags byte) string {
		w := []string{entry(me, allowed, flags, fullAccess), entry(system, allowed, flags, fullAccess)}
		slices.Sort(w)
		return strings.Join(w, "\n")
	}
	private := func(what, path string, folder bool) {
		t.Helper()
		owner, protected, entries := accessList(t, path)
		if owner != me && owner != defaultOwner {
			t.Errorf("%s: owner %s, want this user %s or the default owner %s", what, owner, me, defaultOwner)
		}
		if !protected {
			t.Errorf("%s: its access list is not protected, so it takes entries from its parent", what)
		}
		flags := byte(0)
		if folder {
			flags = inheritable
		}
		if got := strings.Join(entries, "\n"); got != want(flags) {
			t.Errorf("%s: access list\n%s\nwant\n%s", what, got, want(flags))
		}
	}
	record := Record{ID: "acct", Extension: provID, State: StateActive, Account: route.Account{Models: []route.ModelEntry{{Name: "m"}}},
		Credential: protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}

	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Create(record)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "acct.json")
	private("new folder", dir, true)
	private("new file", file, false)
	if _, err := store.Update("acct", rec.Registration, rec.Version, func(r *Record) { r.Label = "x" }); err != nil {
		t.Fatal(err)
	}
	private("file after a save", file, false)

	existing := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", existing, "/grant", "*"+everyone+":(OI)(CI)R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	left := filepath.Join(existing, "left.txt")
	if err := os.WriteFile(left, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	everyoneIn := func(path string) bool {
		_, _, entries := accessList(t, path)
		return slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, everyone+" ") })
	}
	if !everyoneIn(left) {
		t.Fatal("setup: the file in the open folder did not inherit Everyone's entry")
	}
	store, err = Open(FileBackend{Dir: existing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	private("existing folder after a save", existing, true)
	private("new file in the existing folder", filepath.Join(existing, "acct.json"), false)
	if everyoneIn(left) {
		_, _, entries := accessList(t, left)
		t.Errorf("file already in the folder still lets Everyone in after a save:\n%s", strings.Join(entries, "\n"))
	}
}
