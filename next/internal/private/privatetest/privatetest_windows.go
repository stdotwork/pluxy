package privatetest

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var (
	advapi32                         = syscall.NewLazyDLL("advapi32.dll")
	procGetNamedSecurityInfo         = advapi32.NewProc("GetNamedSecurityInfoW")
	procGetSecurityDescriptorControl = advapi32.NewProc("GetSecurityDescriptorControl")
	procGetAce                       = advapi32.NewProc("GetAce")
)

const (
	Everyone       = "S-1-1-0"
	System         = "S-1-5-18"
	LocalService   = "S-1-5-19"
	Administrators = "S-1-5-32-544"
)

const (
	Inheritable = 0x1 | 0x2
	Inherited   = 0x10
)

const (
	seFileObject    = 1
	ownerInfo       = 0x1
	daclInfo        = 0x4
	seDaclProtected = 0x1000
	aceAllowedType  = 0
	fullAccess      = 0x001F01FF
)

type aclHeader struct {
	Revision, _ byte
	Size        uint16
	Count       uint16
	_           uint16
}

type aceHeader struct {
	Type, Flags byte
	Size        uint16
	Mask        uint32
	SidStart    uint32
}

func AccessList(t *testing.T, path string) (owner string, protected bool, entries []string) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var ownerSID *syscall.SID
	var dacl *aclHeader
	var sd uintptr
	if r, _, _ := procGetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(p)), seFileObject, ownerInfo|daclInfo,
		uintptr(unsafe.Pointer(&ownerSID)), 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
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
		var ace *aceHeader
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

func ThisUser(t *testing.T) (me, defaultOwner string) {
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

func WantList(t *testing.T, flags byte) string {
	t.Helper()
	me, _ := ThisUser(t)
	w := []string{entry(me, aceAllowedType, flags, fullAccess), entry(System, aceAllowedType, flags, fullAccess)}
	slices.Sort(w)
	return strings.Join(w, "\n")
}

func CheckOwner(t *testing.T, what, path string) {
	t.Helper()
	me, defaultOwner := ThisUser(t)
	if owner, _, _ := AccessList(t, path); owner != me && owner != defaultOwner && owner != System {
		t.Errorf("%s: owner %s, want this user %s, the default owner %s or SYSTEM", what, owner, me, defaultOwner)
	}
}

func CheckPrivate(t *testing.T, what, path string, folder bool) {
	t.Helper()
	CheckOwner(t, what, path)
	_, protected, entries := AccessList(t, path)
	if !protected {
		t.Errorf("%s: its access list is not protected, so it takes entries from its parent", what)
	}
	flags := byte(0)
	if folder {
		flags = Inheritable
	}
	if got := strings.Join(entries, "\n"); got != WantList(t, flags) {
		t.Errorf("%s: access list\n%s\nwant\n%s", what, got, WantList(t, flags))
	}
}

func CheckInherited(t *testing.T, what, path string, folder bool) {
	t.Helper()
	CheckOwner(t, what, path)
	flags := byte(Inherited)
	if folder {
		flags |= Inheritable
	}
	if _, _, entries := AccessList(t, path); strings.Join(entries, "\n") != WantList(t, flags) {
		t.Errorf("%s: access list\n%s\nwant\n%s", what, strings.Join(entries, "\n"), WantList(t, flags))
	}
}

func Icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v: %s", args, err, out)
	}
}

func OpenFolder(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	Icacls(t, dir, "/grant", "*"+Everyone+":(OI)(CI)R")
}

func WriteOpenFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !LetsEveryoneIn(t, path) {
		_, _, entries := AccessList(t, path)
		t.Fatalf("setup: %s did not take Everyone's entry from its folder:\n%s", path, strings.Join(entries, "\n"))
	}
}

func LetsEveryoneIn(t *testing.T, path string) bool {
	t.Helper()
	_, _, entries := AccessList(t, path)
	return slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, Everyone+" ") })
}

func SetOwner(t *testing.T, path, sid string) {
	t.Helper()
	if out, err := exec.Command("icacls", path, "/setowner", "*"+sid).CombinedOutput(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("icacls /setowner: %v: %s", err, out)
		}
		t.Skipf("cannot give a file to another account here (icacls /setowner: %v: %s)", err, out)
	}
	if owner, _, _ := AccessList(t, path); owner != sid {
		t.Fatalf("setup: the owner of %s is %s, want %s", path, owner, sid)
	}
}

func Junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	t.Cleanup(func() { os.Remove(link) })
}

func Names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
