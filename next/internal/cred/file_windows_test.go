package cred

import (
	"errors"
	"fmt"
	"io/fs"
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
	procLookupPrivilegeValue         = testAdvapi32.NewProc("LookupPrivilegeValueW")
	procAdjustTokenPrivileges        = testAdvapi32.NewProc("AdjustTokenPrivileges")
)

const (
	testSEFileObject         = 1 // SE_FILE_OBJECT
	ownerSecurityInformation = 0x1
	daclSecurityInfo         = 0x4
	seDaclProtected          = 0x1000
	everyone                 = "S-1-1-0"
	system                   = "S-1-5-18"
	administrators           = "S-1-5-32-544"
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
// exists, open to Everyone, is made private when the store is opened, with no save, and a file
// already in it then takes only this user's and SYSTEM's entries from it.
func TestFileBackendWindowsAccessList(t *testing.T) {
	me, defaultOwner := thisUser(t)
	const (
		allowed     = 0          // ACCESS_ALLOWED_ACE_TYPE
		inheritable = 0x1 | 0x2  // OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE
		inherited   = 0x10       // INHERITED_ACE
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
	record := windowsRecord()

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
	icacls(t, existing, "/grant", "*"+everyone+":(OI)(CI)R")
	left := filepath.Join(existing, "left.txt")
	if err := os.WriteFile(left, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, entries := accessList(t, left); !slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, everyone+" ") }) {
		t.Fatalf("setup: the file in the open folder did not inherit Everyone's entry:\n%s", strings.Join(entries, "\n"))
	}
	store, err = Open(FileBackend{Dir: existing})
	if err != nil {
		t.Fatal(err)
	}
	private("existing folder after opening", existing, true)
	if _, _, entries := accessList(t, left); strings.Join(entries, "\n") != want(inherited) {
		t.Errorf("file already in the folder after opening: access list\n%s\nwant\n%s", strings.Join(entries, "\n"), want(inherited))
	}
	if _, err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	private("new file in the existing folder", filepath.Join(existing, "acct.json"), false)
}

func windowsRecord() Record {
	return Record{ID: "acct", Extension: provID, State: StateActive, Account: route.Account{Models: []route.ModelEntry{{Name: "m"}}},
		Credential: protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}
}

func icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v: %s", args, err, out)
	}
}

// refused checks that a save into dir and opening the store on dir both fail with an error
// that wrong accepts, and leave the folder inside (dir, or the folder a link at dir points to)
// as it was: no file in it, and the same access list.
func refused(t *testing.T, dir, inside, want string, wrong func(error) bool) {
	t.Helper()
	_, protected, before := accessList(t, inside)
	if err := (FileBackend{Dir: dir}).Save(windowsRecord()); err == nil || !wrong(err) {
		t.Errorf("save into %s: error %v, want one saying it %s", dir, err, want)
	}
	if _, err := Open(FileBackend{Dir: dir}); err == nil || !wrong(err) {
		t.Errorf("opening the store on %s: error %v, want one saying it %s", dir, err, want)
	}
	if entries, err := os.ReadDir(inside); err != nil || len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s holds %v (%v) after the refused save, want nothing", inside, names, err)
	}
	if _, p, after := accessList(t, inside); p != protected || !slices.Equal(after, before) {
		t.Errorf("the access list of %s changed:\n%s\nwas\n%s", inside, strings.Join(after, "\n"), strings.Join(before, "\n"))
	}
}

func says(text string) func(error) bool {
	return func(err error) bool { return strings.Contains(err.Error(), text) }
}

// A credentials folder that is a link (here a junction) to another folder is refused, not made
// private through the link.
func TestFileBackendWindowsRefusesALink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "credentials")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	t.Cleanup(func() { os.Remove(link) }) // the junction only, before the folder is removed
	refused(t, link, target, "is a link", says("is a link"))
}

// A credentials folder another account owns is refused, though this user may change its list:
// the owner could open it again. Giving a folder to another account takes the restore
// privilege, which an elevated administrator holds (CI's hosted runners run elevated) and
// icacls turns on; without it the test is skipped, except on CI.
func TestFileBackendWindowsRefusesAnotherOwner(t *testing.T) {
	const localService = "S-1-5-19"
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", dir, "/setowner", "*"+localService).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("icacls /setowner: %v: %s", err, out)
		}
		t.Skipf("cannot give a folder to another account here (icacls /setowner: %v: %s)", err, out)
	}
	if owner, _, _ := accessList(t, dir); owner != localService {
		t.Fatalf("setup: the folder's owner is %s, want %s", owner, localService)
	}
	refused(t, dir, dir, "is owned by another account", says("owned by another account"))
}

// withoutPrivileges turns the named privileges off in this process's token until the test ends,
// when each is set back as it was. A privilege the token does not hold is left alone.
func withoutPrivileges(t *testing.T, names ...string) {
	t.Helper()
	const adjustPrivileges, query = 0x20, 0x8 // TOKEN_ADJUST_PRIVILEGES, TOKEN_QUERY
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	var token syscall.Token
	if err := syscall.OpenProcessToken(process, adjustPrivileges|query, &token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { token.Close() })
	type privileges struct {
		Count      uint32
		Low        uint32
		High       int32
		Attributes uint32
	}
	for _, name := range names {
		n, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		off := privileges{Count: 1}
		if r, _, e := procLookupPrivilegeValue.Call(0, uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&off.Low))); r == 0 {
			t.Fatalf("%s: %v", name, e)
		}
		var was privileges
		var size uint32
		if r, _, e := procAdjustTokenPrivileges.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&off)), unsafe.Sizeof(was),
			uintptr(unsafe.Pointer(&was)), uintptr(unsafe.Pointer(&size))); r == 0 {
			t.Fatalf("%s: %v", name, e)
		}
		t.Cleanup(func() {
			if was.Count != 0 {
				procAdjustTokenPrivileges.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&was)), 0, 0, 0)
			}
		})
	}
}

// When the folder's list cannot be changed, the save fails before it writes anything. The
// folder is given to SYSTEM, an owner the store accepts, so this user no longer has the owner's
// own right to change its list, and its list gives this user Modify (M): everything but changing
// the list or the owner, so a save that went on after the failed change could still write its
// record. Giving a folder to SYSTEM takes the restore privilege, as in
// TestFileBackendWindowsRefusesAnotherOwner. An administrator's backup, restore and
// take-ownership privileges would get past the list when they are on (a folder is opened with
// backup semantics), so they are turned off for the test.
func TestFileBackendWindowsFailedReset(t *testing.T) {
	me, _ := thisUser(t)
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", dir, "/setowner", "*"+system).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("icacls /setowner: %v: %s", err, out)
		}
		t.Skipf("cannot give a folder to SYSTEM here (icacls /setowner: %v: %s)", err, out)
	}
	icacls(t, dir, "/inheritance:r", "/grant:r", "*"+me+":(OI)(CI)M")
	// After that, CI's runner still listed SYSTEM and Administrators with full access as the
	// folder's own entries, and an elevated administrator could change the list through
	// Administrators'.
	icacls(t, dir, "/remove:g", "*"+system, "*"+administrators)
	withoutPrivileges(t, "SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	const writeDAC = 0x00040000
	if h, err := syscall.CreateFile(p, writeDAC, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0); err == nil {
		syscall.CloseHandle(h)
		list, _ := exec.Command("icacls", dir).CombinedOutput()
		priv, _ := exec.Command("whoami", "/priv").CombinedOutput()
		t.Fatalf("setup: this user can still change the folder's access list:\n%s\n%s", list, priv)
	}
	refused(t, dir, dir, "is denied", func(err error) bool { return errors.Is(err, fs.ErrPermission) })
}
