package cred

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
// its parent's access list, which may let other local users read it. The credentials folder and
// each file are instead given a protected access list (nothing inherited from their parents)
// that lets in only this user and SYSTEM, with full access, the folder's entries inherited by
// what is made in it. The owner is left as Windows sets it: this user, or the token's default
// owner (the Administrators group when elevated). The folder is checked through a handle to it,
// as the app checks its own private folders (app/src-tauri/src/private_windows.rs,
// make_private_dir): a link, a file or a folder another account owns is refused, and a folder
// whose list is not private gets the private list. Record files that are links or that another
// account owns are skipped when the records are loaded (file.go, readRecord).
//
// The security calls go through advapi32.dll, and the temporary file is made with ntdll.dll's
// NtCreateFile; the standard library registers both as system DLLs, so they are loaded from
// System32 only.

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procStringToSD                = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSecurityDescriptorDacl = advapi32.NewProc("GetSecurityDescriptorDacl")
	procGetDescriptorControl      = advapi32.NewProc("GetSecurityDescriptorControl")
	procGetSecurityInfo           = advapi32.NewProc("GetSecurityInfo")
	procSetSecurityInfo           = advapi32.NewProc("SetSecurityInfo")
	procGetAceEntry               = advapi32.NewProc("GetAce")
	ntdll                         = syscall.NewLazyDLL("ntdll.dll")
	procNtCreateFile              = ntdll.NewProc("NtCreateFile")
	procNtStatusToDosError        = ntdll.NewProc("RtlNtStatusToDosError")
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
	synchronize       = 0x00100000
	objCaseInsensitve = 0x40       // OBJ_CASE_INSENSITIVE
	fileCreate        = 2          // FILE_CREATE
	fileSyncIO        = 0x20       // FILE_SYNCHRONOUS_IO_NONALERT
	fileNonDirectory  = 0x40       // FILE_NON_DIRECTORY_FILE
	nameCollision     = 0xC0000035 // STATUS_OBJECT_NAME_COLLISION
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
		return 0, fmt.Errorf("security descriptor: %w", e)
	}
	return sd, nil
}

// makeDir makes dir, and any missing parents, with the private access list given to dir as it
// is made. A dir that exists already is left to openChecked.
func makeDir(dir string) error {
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
	if err := syscall.CreateDirectory(path, &sa); err != nil && !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// accountName names the account sid is, with its SID text in brackets, or gives the text alone
// when the name cannot be found.
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

// owner returns the owner of what h is open to, named by accountName, and whether a private file
// or folder may have it: this user, the token's default owner, or SYSTEM.
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

// openChecked opens the folder dir itself, never what a link there points to, after making it
// (makeDir) when create is set, and checks it through that handle, as the chmod 0700 of unix: a
// link (a symbolic link or a junction), a file, or a folder owned by another account than this
// user, the token's default owner or SYSTEM is refused, since that owner could open the folder's
// list again and plant records. A folder whose list is already private (protected, its allow
// entries only this user's and SYSTEM's) is left as it is; any other gets the private list.
// Setting the list also passes its entries on to the files already in the folder that inherit
// theirs (records saved before this rule), so they lose what they inherited before.
func openChecked(dir string, create bool) (*os.File, error) {
	dir = filepath.Clean(dir)
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
	d := os.NewFile(uintptr(h), dir)
	if err := checkDir(h, dir); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func checkDir(h syscall.Handle, dir string) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return &os.PathError{Op: "stat", Path: dir, Err: err}
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is a link; credentials_dir must name the folder itself", dir)
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s is not a folder", dir)
	}
	who, ok, err := owner(h)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !ok {
		return fmt.Errorf("%s is owned by %s, not by this user, so its owner could open it to others; "+
			"a folder made by pluxy run as administrator is owned by the Administrators group, which only a run as "+
			"administrator accepts. Take ownership of the folder and its files from an elevated prompt (for example: "+
			"takeown /f \"%s\" /r), or move the folder away and sign the accounts in again", dir, who, dir)
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

// sameDir reports whether a and b are open to the same folder: the same volume and file index.
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

// recordOwner reports whether the owner of the record file f is one the folder check accepts,
// and names it.
func recordOwner(f *os.File) (bool, string, error) {
	who, ok, err := owner(syscall.Handle(f.Fd()))
	return ok, who, err
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

// createTemp creates a new temporary file in root, named by tempName, with the private access
// list from the moment it exists: no one else can open it while it is written. It is made
// relative to root's own handle, so it lands in the folder root holds. A rename in the same
// folder keeps the file's access list, so the record it becomes has it too. dir is root's path,
// for messages.
func createTemp(root *os.Root, dir string) (*os.File, string, error) {
	d, err := root.Open(".")
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	sd, err := privateDescriptor(false)
	if err != nil {
		return nil, "", err
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	for range 100 {
		name, err := tempName()
		if err != nil {
			return nil, "", err
		}
		u := syscall.StringToUTF16(name)
		us := unicodeString{Length: uint16(2 * (len(u) - 1)), MaximumLength: uint16(2 * (len(u) - 1)), Buffer: &u[0]}
		oa := objectAttributes{RootDirectory: syscall.Handle(d.Fd()), ObjectName: &us, Attributes: objCaseInsensitve,
			SecurityDescriptor: sd}
		oa.Length = uint32(unsafe.Sizeof(oa))
		var h syscall.Handle
		var iosb ioStatusBlock
		// The access and sharing os.OpenFile uses for a new file opened for reading and writing.
		r, _, _ := procNtCreateFile.Call(uintptr(unsafe.Pointer(&h)), syscall.GENERIC_READ|syscall.GENERIC_WRITE|synchronize,
			uintptr(unsafe.Pointer(&oa)), uintptr(unsafe.Pointer(&iosb)), 0, syscall.FILE_ATTRIBUTE_NORMAL,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, fileCreate, fileNonDirectory|fileSyncIO, 0, 0)
		runtime.KeepAlive(u)
		runtime.KeepAlive(&us)
		runtime.KeepAlive(&oa)
		path := filepath.Join(dir, name)
		switch status := uint32(r); {
		case status == nameCollision:
			continue
		case status != 0:
			code, _, _ := procNtStatusToDosError.Call(uintptr(status))
			return nil, "", &os.PathError{Op: "createtemp", Path: path, Err: syscall.Errno(code)}
		}
		return os.NewFile(uintptr(h), path), name, nil
	}
	return nil, "", &os.PathError{Op: "createtemp", Path: filepath.Join(dir, tmpPrefix+"*"), Err: os.ErrExist}
}

// isPrivate reports whether the descriptor sd, with its access list dacl, is private: the list
// exists and is protected, and every allow entry is this user's (me) or SYSTEM's. Deny entries
// take nothing away; any other kind of entry is not one this checks, so the list is not private.
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
