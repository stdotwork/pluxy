package cred

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	testAdvapi32                     = syscall.NewLazyDLL("advapi32.dll")
	procGetNamedSecurityInfo         = testAdvapi32.NewProc("GetNamedSecurityInfoW")
	procGetSecurityDescriptorControl = testAdvapi32.NewProc("GetSecurityDescriptorControl")
	procGetAce                       = testAdvapi32.NewProc("GetAce")
	procLookupPrivilegeValue         = testAdvapi32.NewProc("LookupPrivilegeValueW")
	procAdjustTokenPrivileges        = testAdvapi32.NewProc("AdjustTokenPrivileges")
	procDescriptorToString           = testAdvapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
)

const (
	testSEFileObject         = 1
	ownerSecurityInformation = 0x1
	daclSecurityInfo         = 0x4
	seDaclProtected          = 0x1000
	everyone                 = "S-1-1-0"
	system                   = "S-1-5-18"
	administrators           = "S-1-5-32-544"
)

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

const (
	aceAllowedType = 0
	inheritable    = 0x1 | 0x2
	inherited      = 0x10
	fullAccess     = 0x001F01FF
)

func wantList(t *testing.T, flags byte) string {
	me, _ := thisUser(t)
	w := []string{entry(me, aceAllowedType, flags, fullAccess), entry(system, aceAllowedType, flags, fullAccess)}
	slices.Sort(w)
	return strings.Join(w, "\n")
}

func checkPrivate(t *testing.T, what, path string, folder bool) {
	t.Helper()
	me, defaultOwner := thisUser(t)
	owner, protected, entries := accessList(t, path)
	if owner != me && owner != defaultOwner && owner != system {
		t.Errorf("%s: owner %s, want this user %s, the default owner %s or SYSTEM", what, owner, me, defaultOwner)
	}
	if !protected {
		t.Errorf("%s: its access list is not protected, so it takes entries from its parent", what)
	}
	flags := byte(0)
	if folder {
		flags = inheritable
	}
	if got := strings.Join(entries, "\n"); got != wantList(t, flags) {
		t.Errorf("%s: access list\n%s\nwant\n%s", what, got, wantList(t, flags))
	}
}

func TestFileBackendWindowsAccessList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Create(recordFor("acct"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "acct.json")
	checkPrivate(t, "new folder", dir, true)
	checkPrivate(t, "new file", file, false)
	if _, err := store.Update("acct", rec.Registration, rec.Version, func(r *Record) { r.Label = "x" }); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, "file after a save", file, false)

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
	checkPrivate(t, "existing folder after opening", existing, true)
	if _, _, entries := accessList(t, left); strings.Join(entries, "\n") != wantList(t, inherited) {
		t.Errorf("file already in the folder after opening: access list\n%s\nwant\n%s", strings.Join(entries, "\n"), wantList(t, inherited))
	}
	if _, err := store.Create(recordFor("acct")); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, "new file in the existing folder", filepath.Join(existing, "acct.json"), false)
}

func icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v: %s", args, err, out)
	}
}

func setOwner(t *testing.T, path, sid string) {
	t.Helper()
	if out, err := exec.Command("icacls", path, "/setowner", "*"+sid).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("icacls /setowner: %v: %s", err, out)
		}
		t.Skipf("cannot give a file to another account here (icacls /setowner: %v: %s)", err, out)
	}
	if owner, _, _ := accessList(t, path); owner != sid {
		t.Fatalf("setup: the owner of %s is %s, want %s", path, owner, sid)
	}
}

func refused(t *testing.T, dir, inside, want string, wrong func(error) bool) {
	t.Helper()
	_, protected, before := accessList(t, inside)
	accepted := func(err error) bool {
		return err != nil && strings.HasPrefix(err.Error(), "credential store: ") && wrong(err)
	}
	if err := (FileBackend{Dir: dir}).Save(recordFor("acct")); !accepted(err) {
		t.Errorf("save into %s: error %v, want a credential store error saying it %s", dir, err, want)
	}
	if _, err := Open(FileBackend{Dir: dir}); !accepted(err) {
		t.Errorf("opening the store on %s: error %v, want a credential store error saying it %s", dir, err, want)
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
	t.Cleanup(func() { os.Remove(link) })
	refused(t, link, target, "is a link", says("is a link"))
}

func TestFileBackendWindowsRefusesAnotherOwner(t *testing.T) {
	const localService = "S-1-5-19"
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setOwner(t, dir, localService)
	refused(t, dir, dir, "is owned by NT AUTHORITY\\LOCAL SERVICE (S-1-5-19) and how to take it over", func(err error) bool {
		return strings.Contains(err.Error(), `is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19), not by this user`) &&
			strings.Contains(err.Error(), "takeown /f") && strings.Contains(err.Error(), "Administrators group")
	})
}

func withoutPrivileges(t *testing.T, names ...string) {
	t.Helper()
	const adjustPrivileges, query = 0x20, 0x8
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

func TestFileBackendWindowsFailedReset(t *testing.T) {
	me, _ := thisUser(t)
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setOwner(t, dir, system)
	icacls(t, dir, "/inheritance:r", "/grant:r", "*"+me+":(OI)(CI)M")
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

func descriptor(t *testing.T, path string) string {
	t.Helper()
	const groupInfo = 0x2
	info := uintptr(ownerSecurityInformation | groupInfo | daclSecurityInfo)
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var sd uintptr
	if r, _, _ := procGetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(p)), testSEFileObject, info, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&sd))); r != 0 {
		t.Fatalf("%s: %v", path, syscall.Errno(r))
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	var text *uint16
	if r, _, e := procDescriptorToString.Call(sd, 1, info, uintptr(unsafe.Pointer(&text)), 0); r == 0 {
		t.Fatalf("%s: %v", path, e)
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(text)))
	return syscall.UTF16ToString(unsafe.Slice(text, 1<<16))
}

func TestFileBackendWindowsProtectedButOpen(t *testing.T) {
	me, _ := thisUser(t)
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	icacls(t, dir, "/inheritance:r", "/grant:r", "*"+me+":(OI)(CI)F", "*"+system+":(OI)(CI)F", "*"+everyone+":(OI)(CI)R")
	icacls(t, dir, "/remove:g", "*"+administrators)
	if _, protected, entries := accessList(t, dir); !protected || !slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, everyone+" ") }) {
		t.Fatalf("setup: want a protected list that lets Everyone in, got protected=%v\n%s", protected, strings.Join(entries, "\n"))
	}
	if _, err := Open(FileBackend{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, "protected folder open to Everyone, after opening", dir, true)
}

func TestFileBackendWindowsLeavesAPrivateFolder(t *testing.T) {
	me, _ := thisUser(t)
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	icacls(t, dir, "/inheritance:r", "/grant:r", "*"+me+":(OI)(CI)F")
	icacls(t, dir, "/remove:g", "*"+system, "*"+administrators)
	if _, protected, entries := accessList(t, dir); !protected || strings.Join(entries, "\n") != entry(me, aceAllowedType, inheritable, fullAccess) {
		t.Fatalf("setup: want a protected list of this user's entry alone, got protected=%v\n%s", protected, strings.Join(entries, "\n"))
	}
	before := descriptor(t, dir)
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(recordFor("acct")); err != nil {
		t.Fatal(err)
	}
	if after := descriptor(t, dir); after != before {
		t.Errorf("the private folder's descriptor changed:\n%s\nwas\n%s", after, before)
	}
}

func TestFileBackendWindowsAcceptsSystemsFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setOwner(t, dir, system)
	if _, err := Open(FileBackend{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, "SYSTEM's folder after opening", dir, true)
}

func TestFileBackendWindowsSkipsARecordAnotherAccountOwns(t *testing.T) {
	const localService = "S-1-5-19"
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(recordFor("acct")); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "planted.json")
	writeRecordFile(t, planted, recordFor("planted"))
	setOwner(t, planted, localService)
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)
	again, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("planted"); ok {
		t.Error("the record another account owns was loaded")
	}
	if _, ok := again.Get("acct"); !ok {
		t.Error("the store's own record was not loaded")
	}
	if want := "skipped " + planted + `: it is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19)`; !strings.Contains(logged.String(), want) {
		t.Errorf("log:\n%s\nwant a line with %q", logged.String(), want)
	}
}

func TestCreatePrivateWindowsAccessList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := CreatePrivate(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, "created file", path, false)
}

func letsEveryoneIn(t *testing.T, path string) bool {
	t.Helper()
	_, _, entries := accessList(t, path)
	return slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, everyone+" ") })
}

func loadsPrivately(t *testing.T, dir, file string) {
	t.Helper()
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("acct"); !ok {
		t.Error("the record was not loaded")
	}
	checkPrivate(t, "record file after loading", file, false)
	if want := "made " + file + " private"; !strings.Contains(logged.String(), want) {
		t.Errorf("log:\n%s\nwant a line with %q", logged.String(), want)
	}
}

func TestFileBackendWindowsMakesARecordWithItsOwnEntriesPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	icacls(t, dir, "/grant", "*"+everyone+":(OI)(CI)R")
	file := filepath.Join(dir, "acct.json")
	writeRecordFile(t, file, recordFor("acct"))
	icacls(t, file, "/grant", "*"+everyone+":R")
	if _, _, entries := accessList(t, file); !slices.ContainsFunc(entries, func(e string) bool {
		return strings.HasPrefix(e, fmt.Sprintf("%s type %d flags %#x ", everyone, aceAllowedType, 0))
	}) {
		t.Fatalf("setup: the record file has no entry of its own for Everyone:\n%s", strings.Join(entries, "\n"))
	}
	loadsPrivately(t, dir, file)
}

func TestFileBackendWindowsMakesAMovedInRecordPrivate(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(recordFor("other")); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	icacls(t, open, "/grant", "*"+everyone+":(OI)(CI)R")
	writeRecordFile(t, filepath.Join(open, "acct.json"), recordFor("acct"))
	file := filepath.Join(dir, "acct.json")
	if err := os.Rename(filepath.Join(open, "acct.json"), file); err != nil {
		t.Fatal(err)
	}
	if _, protected, _ := accessList(t, file); protected || !letsEveryoneIn(t, file) {
		t.Fatalf("setup: want the moved record to inherit nothing new and keep Everyone's entry, protected=%v", protected)
	}
	loadsPrivately(t, dir, file)
}
