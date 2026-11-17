package cred

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// On Windows, Go applies only the read-only bit of a mode, so a new folder or file would take
// its parent's access list, which may let other local users read it. The credentials folder and
// each file are instead given a protected access list (nothing inherited from their parents)
// that lets in only this user and SYSTEM, with full access, the folder's entries inherited by
// what is made in it. The owner is left as Windows sets it: this user, or the token's default
// owner (the Administrators group when elevated). A folder that exists already is checked
// through a handle to it, as the app checks its own private folders
// (app/src-tauri/src/private_windows.rs, make_private_dir): a link, a file or a folder another
// account owns is refused, and a folder whose list is not private gets the private list.
//
// The security calls go through advapi32.dll, which the standard library's syscall package
// registers as a system DLL, so it is loaded from System32 only.

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procStringToSD                = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSecurityDescriptorDacl = advapi32.NewProc("GetSecurityDescriptorDacl")
	procGetDescriptorControl      = advapi32.NewProc("GetSecurityDescriptorControl")
	procGetSecurityInfo           = advapi32.NewProc("GetSecurityInfo")
	procSetSecurityInfo           = advapi32.NewProc("SetSecurityInfo")
	procGetAceEntry               = advapi32.NewProc("GetAce")
)

const (
	sddlRevision1     = 1
	seFileObject      = 1
	ownerInfo         = 0x1        // OWNER_SECURITY_INFORMATION
	daclInfo          = 0x4        // DACL_SECURITY_INFORMATION
	protectedDaclInfo = 0x80000000 // PROTECTED_DACL_SECURITY_INFORMATION
	daclProtected     = 0x1000     // SE_DACL_PROTECTED
	readControl       = 0x00020000
	writeDAC          = 0x00040000
	readAttributes    = 0x80 // FILE_READ_ATTRIBUTES
	aceAllowed        = 0    // ACCESS_ALLOWED_ACE_TYPE
	aceDenied         = 1    // ACCESS_DENIED_ACE_TYPE
	systemSID         = "S-1-5-18"
)

// winACL is the start of an ACL; winACE is an ACCESS_ALLOWED_ACE or ACCESS_DENIED_ACE (other
// kinds share its first eight bytes).
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

// tokenSIDs returns this process's user and its token's default owner, the owner Windows gives
// what this process makes.
func tokenSIDs() (user, owner string, err error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", "", fmt.Errorf("credential store: %w", err)
	}
	defer token.Close()
	u, err := token.GetTokenUser()
	if err != nil {
		return "", "", fmt.Errorf("credential store: %w", err)
	}
	if user, err = u.User.Sid.String(); err != nil {
		return "", "", fmt.Errorf("credential store: %w", err)
	}
	var n uint32
	syscall.GetTokenInformation(token, syscall.TokenOwner, nil, 0, &n) // fails with the size in n
	buf := make([]uintptr, int(n)/int(unsafe.Sizeof(uintptr(0)))+1)    // aligned for the pointer it holds
	if err := syscall.GetTokenInformation(token, syscall.TokenOwner, (*byte)(unsafe.Pointer(&buf[0])),
		uint32(len(buf))*uint32(unsafe.Sizeof(uintptr(0))), &n); err != nil {
		return "", "", fmt.Errorf("credential store: %w", err)
	}
	owner, err = (*struct{ Owner *syscall.SID })(unsafe.Pointer(&buf[0])).Owner.String()
	runtime.KeepAlive(buf)
	if err != nil {
		return "", "", fmt.Errorf("credential store: %w", err)
	}
	return user, owner, nil
}

// privateDescriptor returns a security descriptor whose access list is the one described
// above, for a folder (its entries are inherited by what is made in it) or a file. Free it
// with syscall.LocalFree.
func privateDescriptor(folder bool) (uintptr, error) {
	me, _, err := tokenSIDs()
	if err != nil {
		return 0, err
	}
	inherit := ""
	if folder {
		inherit = "OICI" // object inherit, container inherit
	}
	// D:P is a protected access list; FA is full access; SY is SYSTEM.
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
		return 0, fmt.Errorf("credential store: security descriptor: %w", e)
	}
	return sd, nil
}

// makeDir makes dir, and any missing parents, with the private access list given to dir as it
// is made. A dir that exists already is checked and made private by keepPrivate.
func makeDir(dir string) error {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(filepath.Dir(dir), dirMode); err != nil {
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
	err = syscall.CreateDirectory(path, &sa)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return keepPrivate(dir)
}

// keepPrivate checks the folder dir, which must exist, and makes it private when it is not:
// the chmod 0700 of unix. dir itself is opened, never what a link there points to, and every
// check and change goes through that handle. A link (a symbolic link or a junction), a file, or
// a folder owned by another account than this user, the token's default owner or SYSTEM is
// refused: that owner could open the folder's list again, and plant records. A folder whose
// list is already private (protected, its allow entries only this user's and SYSTEM's) is left
// as it is. Setting the list also passes its entries on to the files already in the folder that
// inherit theirs (records saved before this rule), so they lose what they inherited before.
func keepPrivate(dir string) error {
	refuse := func(why string) error {
		return fmt.Errorf("credential store: %s %s; it must be a folder of this user's own", dir, why)
	}
	path, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(path, readControl|writeDAC|readAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return &os.PathError{Op: "open the credentials folder to make it private", Path: dir, Err: err}
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return &os.PathError{Op: "stat", Path: dir, Err: err}
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return refuse("is a link")
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return refuse("is not a folder")
	}
	me, defaultOwner, err := tokenSIDs()
	if err != nil {
		return err
	}
	var owner *syscall.SID
	var dacl *winACL
	var sd uintptr
	if r, _, _ := procGetSecurityInfo.Call(uintptr(h), seFileObject, ownerInfo|daclInfo,
		uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		return &os.PathError{Op: "read access list", Path: dir, Err: syscall.Errno(r)}
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	if owner == nil {
		return refuse("has no owner")
	}
	o, err := owner.String()
	if err != nil {
		return err
	}
	if o != me && o != defaultOwner && o != systemSID {
		return refuse("is owned by another account (" + o + ")")
	}
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
		return fmt.Errorf("credential store: security descriptor: %w", e)
	}
	if r, _, _ := procSetSecurityInfo.Call(uintptr(h), seFileObject, daclInfo|protectedDaclInfo, 0, 0, list, 0); r != 0 {
		return &os.PathError{Op: "set access list", Path: dir, Err: syscall.Errno(r)}
	}
	return nil
}

// isPrivate reports whether the descriptor sd, with its access list dacl, is private: the list
// exists and is protected, and every allow entry is this user's (me) or SYSTEM's. Deny entries
// take nothing away; any other kind of entry is not one this checks, so the list is not private.
func isPrivate(sd uintptr, dacl *winACL, me string) (bool, error) {
	var control uint16
	var revision uint32
	if r, _, e := procGetDescriptorControl.Call(sd, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision))); r == 0 {
		return false, fmt.Errorf("credential store: security descriptor: %w", e)
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

// createTemp creates a new temporary file in dir, named as tmpGlob says, with the private
// access list from the moment it exists: no one else can open it while it is written. A rename
// in the same folder keeps the file's access list, so the record it becomes has it too.
func createTemp(dir string) (*os.File, error) {
	sd, err := privateDescriptor(false)
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	for range 100 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, ".tmp-"+hex.EncodeToString(b[:]))
		path, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return nil, err
		}
		// The access and sharing os.OpenFile uses for a new file opened for reading and writing.
		h, err := syscall.CreateFile(path, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, &sa, syscall.CREATE_NEW,
			syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if errors.Is(err, syscall.ERROR_FILE_EXISTS) {
			continue
		}
		if err != nil {
			return nil, &os.PathError{Op: "createtemp", Path: name, Err: err}
		}
		return os.NewFile(uintptr(h), name), nil
	}
	return nil, &os.PathError{Op: "createtemp", Path: filepath.Join(dir, tmpGlob), Err: os.ErrExist}
}
