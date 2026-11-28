package private

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// On Windows, Go applies only the read-only bit of a mode, so a new folder or file would take
// its parent's access list, which may let other local users read it.
//
// The security calls go through advapi32.dll, and new files are made with ntdll.dll's
// NtCreateFile; the standard library registers both as system DLLs, so they are loaded from
// System32 only.

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procStringToSD                = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSecurityDescriptorDacl = advapi32.NewProc("GetSecurityDescriptorDacl")
	procGetDescriptorControl      = advapi32.NewProc("GetSecurityDescriptorControl")
	procGetSecurityInfo           = advapi32.NewProc("GetSecurityInfo")
	procSetSecurityInfo           = advapi32.NewProc("SetSecurityInfo")
	procGetNamedSecurityInfo      = advapi32.NewProc("GetNamedSecurityInfoW")
	procSetNamedSecurityInfo      = advapi32.NewProc("SetNamedSecurityInfoW")
	procGetAceEntry               = advapi32.NewProc("GetAce")
	ntdll                         = syscall.NewLazyDLL("ntdll.dll")
	procNtCreateFile              = ntdll.NewProc("NtCreateFile")
	procNtStatusToDosError        = ntdll.NewProc("RtlNtStatusToDosError")
)

const (
	sddlRevision1     = 1
	seFileObject      = 1
	ownerInfo         = 0x1
	daclInfo          = 0x4
	protectedDaclInfo = 0x80000000
	unprotectedDacl   = 0x20000000
	daclProtected     = 0x1000
	readControl       = 0x00020000
	writeDAC          = 0x00040000
	readAttributes    = 0x80
	aceAllowed        = 0
	aceDenied         = 1
	systemSID         = "S-1-5-18"
	synchronize       = 0x00100000
	objCaseInsensitve = 0x40
	fileCreate        = 2
	fileSyncIO        = 0x20
	fileNonDirectory  = 0x40
	nameCollision     = 0xC0000035
)

type winACL struct {
	Revision, _ byte
	Size        uint16
	Count       uint16
	_           uint16
}

type winACE struct {
	Type, Flags byte
	Size        uint16
	Mask        uint32
	SidStart    uint32
}

func tokenSIDs() (user, owner string, err error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", "", err
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return "", "", err
	}
	if user, err = u.User.Sid.String(); err != nil {
		return "", "", err
	}
	var n uint32
	syscall.GetTokenInformation(token, syscall.TokenOwner, nil, 0, &n) // fails with the size in n
	buf := make([]uintptr, int(n)/int(unsafe.Sizeof(uintptr(0)))+1)    // aligned for the pointer it holds
	if err := syscall.GetTokenInformation(token, syscall.TokenOwner, (*byte)(unsafe.Pointer(&buf[0])),
		uint32(len(buf))*uint32(unsafe.Sizeof(uintptr(0))), &n); err != nil {
		return "", "", err
	}
	owner, err = (*struct{ Owner *syscall.SID })(unsafe.Pointer(&buf[0])).Owner.String()
	runtime.KeepAlive(buf)
	if err != nil {
		return "", "", err
	}
	return user, owner, nil
}

func privateDescriptor(folder bool) (uintptr, error) {
	me, _, err := tokenSIDs()
	if err != nil {
		return 0, err
	}
	inherit := ""
	if folder {
		inherit = "OICI"
	}
	sddl := "D:P"
	for _, who := range []string{me, "SY"} {
		sddl += "(A;" + inherit + ";FA;;;" + who + ")"
	}
	text, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return 0, err
	}
	var sd uintptr
	if r, _, e := procStringToSD.Call(uintptr(unsafe.Pointer(text)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return 0, fmt.Errorf("security descriptor: %w", e)
	}
	return sd, nil
}

func makeDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), DirMode); err != nil {
		return err
	}
	sd, err := privateDescriptor(true)
	if err != nil {
		return err
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	path, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err := syscall.CreateDirectory(path, &sa); err != nil && !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

func accountName(sid *syscall.SID, text string) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil || account == "":
		return text
	case domain != "":
		return domain + `\` + account + " (" + text + ")"
	default:
		return account + " (" + text + ")"
	}
}

func owner(h syscall.Handle) (name string, ok bool, err error) {
	me, defaultOwner, err := tokenSIDs()
	if err != nil {
		return "", false, err
	}
	var sid *syscall.SID
	var sd uintptr
	if r, _, _ := procGetSecurityInfo.Call(uintptr(h), seFileObject, ownerInfo, uintptr(unsafe.Pointer(&sid)), 0, 0, 0,
		uintptr(unsafe.Pointer(&sd))); r != 0 {
		return "", false, fmt.Errorf("reading the owner: %w", syscall.Errno(r))
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	if sid == nil {
		return "no one", false, nil
	}
	text, err := sid.String()
	if err != nil {
		return "", false, err
	}
	return accountName(sid, text), text == me || text == defaultOwner || text == systemSID, nil
}

func openChecked(d Dir, create bool) (*os.File, error) {
	dir := filepath.Clean(d.Path)
	if create {
		if err := makeDir(dir); err != nil {
			return nil, err
		}
	}
	path, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(path, readControl|writeDAC|readAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open the folder to make it private", Path: dir, Err: err}
	}
	f := os.NewFile(uintptr(h), dir)
	if err := checkDir(h, dir, d); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func checkDir(h syscall.Handle, dir string, d Dir) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return &os.PathError{Op: "stat", Path: dir, Err: err}
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		if d.Setting != "" {
			return fmt.Errorf("%s is a link; %s must name the folder itself", dir, d.Setting)
		}
		return fmt.Errorf("%s is a link; core uses only a folder there, never a link to one", dir)
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s is not a folder", dir)
	}
	who, ok, err := owner(h)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !ok {
		redo := ""
		if d.Redo != "" {
			redo = " and " + d.Redo
		}
		return fmt.Errorf("%s is owned by %s, not by this user, so its owner could open it to others; "+
			"a folder made by pluxy run as administrator is owned by the Administrators group, which only a run as "+
			"administrator accepts. Take ownership of the folder and its files from an elevated prompt (for example: "+
			"takeown /f \"%s\" /r), or move the folder away%s", dir, who, dir, redo)
	}
	me, _, err := tokenSIDs()
	if err != nil {
		return err
	}
	var dacl *winACL
	var sd uintptr
	if r, _, _ := procGetSecurityInfo.Call(uintptr(h), seFileObject, daclInfo, 0, 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&sd))); r != 0 {
		return &os.PathError{Op: "read access list", Path: dir, Err: syscall.Errno(r)}
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	if ok, err := isPrivate(sd, dacl, me); err != nil || ok {
		return err
	}
	want, err := privateDescriptor(true)
	if err != nil {
		return err
	}
	defer syscall.LocalFree(syscall.Handle(want))
	var present, defaulted int32
	var list uintptr
	if r, _, e := procGetSecurityDescriptorDacl.Call(want, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&defaulted))); r == 0 {
		return fmt.Errorf("security descriptor: %w", e)
	}
	if r, _, _ := procSetSecurityInfo.Call(uintptr(h), seFileObject, daclInfo|protectedDaclInfo, 0, 0, list, 0); r != 0 {
		return &os.PathError{Op: "set access list", Path: dir, Err: syscall.Errno(r)}
	}
	return nil
}

func sameDir(a, b *os.File) (bool, error) {
	var ia, ib syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(a.Fd()), &ia); err != nil {
		return false, err
	}
	if err := syscall.GetFileInformationByHandle(syscall.Handle(b.Fd()), &ib); err != nil {
		return false, err
	}
	return ia.VolumeSerialNumber == ib.VolumeSerialNumber && ia.FileIndexHigh == ib.FileIndexHigh &&
		ia.FileIndexLow == ib.FileIndexLow, nil
}

func fileOwner(f *os.File) (bool, string, error) {
	who, ok, err := owner(syscall.Handle(f.Fd()))
	return ok, who, err
}

func filePrivate(f *os.File) (bool, error) {
	me, _, err := tokenSIDs()
	if err != nil {
		return false, err
	}
	var dacl *winACL
	var sd uintptr
	if r, _, _ := procGetSecurityInfo.Call(f.Fd(), seFileObject, daclInfo, 0, 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&sd))); r != 0 {
		return false, &os.PathError{Op: "read access list", Path: f.Name(), Err: syscall.Errno(r)}
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	return isPrivate(sd, dacl, me)
}

// only src's own entries are copied (Windows leaves out those src inherited and adds those dst's
// folder passes on).
func copyAccess(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	var dacl, sd uintptr
	if r, _, _ := procGetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(from)), seFileObject, daclInfo, 0, 0,
		uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		return &os.PathError{Op: "read access list", Path: src, Err: syscall.Errno(r)}
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	var control uint16
	var revision uint32
	if r, _, e := procGetDescriptorControl.Call(sd, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision))); r == 0 {
		return fmt.Errorf("security descriptor: %w", e)
	}
	info := uintptr(daclInfo | unprotectedDacl)
	if control&daclProtected != 0 {
		info = daclInfo | protectedDaclInfo
	}
	if r, _, _ := procSetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(to)), seFileObject, info, 0, 0, dacl, 0); r != 0 {
		return &os.PathError{Op: "set access list", Path: dst, Err: syscall.Errno(r)}
	}
	return nil
}

type unicodeString struct {
	Length, MaximumLength uint16
	Buffer                *uint16
}

type objectAttributes struct {
	Length             uint32
	RootDirectory      syscall.Handle
	ObjectName         *unicodeString
	Attributes         uint32
	SecurityDescriptor uintptr
	QualityOfService   uintptr
}

type ioStatusBlock struct {
	Status, Information uintptr
}

func Create(root *os.Root, dir, name string) (*os.File, error) {
	if err := checkName(dir, name); err != nil {
		return nil, err
	}
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	sd, err := privateDescriptor(false)
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	u, err := syscall.UTF16FromString(name)
	if err != nil {
		return nil, err
	}
	us := unicodeString{Length: uint16(2 * (len(u) - 1)), MaximumLength: uint16(2 * (len(u) - 1)), Buffer: &u[0]}
	oa := objectAttributes{RootDirectory: syscall.Handle(d.Fd()), ObjectName: &us, Attributes: objCaseInsensitve,
		SecurityDescriptor: sd}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h syscall.Handle
	var iosb ioStatusBlock
	r, _, _ := procNtCreateFile.Call(uintptr(unsafe.Pointer(&h)), syscall.GENERIC_READ|syscall.GENERIC_WRITE|synchronize,
		uintptr(unsafe.Pointer(&oa)), uintptr(unsafe.Pointer(&iosb)), 0, syscall.FILE_ATTRIBUTE_NORMAL,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, fileCreate, fileNonDirectory|fileSyncIO, 0, 0)
	runtime.KeepAlive(u)
	runtime.KeepAlive(&us)
	runtime.KeepAlive(&oa)
	path := filepath.Join(dir, name)
	switch status := uint32(r); {
	case status == nameCollision:
		return nil, &os.PathError{Op: "create", Path: path, Err: os.ErrExist}
	case status != 0:
		code, _, _ := procNtStatusToDosError.Call(uintptr(status))
		return nil, &os.PathError{Op: "create", Path: path, Err: syscall.Errno(code)}
	}
	return os.NewFile(uintptr(h), path), nil
}

func isPrivate(sd uintptr, dacl *winACL, me string) (bool, error) {
	var control uint16
	var revision uint32
	if r, _, e := procGetDescriptorControl.Call(sd, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision))); r == 0 {
		return false, fmt.Errorf("security descriptor: %w", e)
	}
	if control&daclProtected == 0 || dacl == nil {
		return false, nil
	}
	for i := range uint32(dacl.Count) {
		var ace *winACE
		if r, _, _ := procGetAceEntry.Call(uintptr(unsafe.Pointer(dacl)), uintptr(i), uintptr(unsafe.Pointer(&ace))); r == 0 {
			return false, nil
		}
		switch ace.Type {
		case aceAllowed:
			sid, err := (*syscall.SID)(unsafe.Pointer(&ace.SidStart)).String()
			if err != nil || sid != me && sid != systemSID {
				return false, nil
			}
		case aceDenied:
		default:
			return false, nil
		}
	}
	return true, nil
}
