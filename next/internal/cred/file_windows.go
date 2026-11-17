package cred

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// On Windows, Go applies only the read-only bit of a mode, so a new folder or file would take
// its parent's access list, which may let other local users read it. The credentials folder and
// each file are instead given a protected access list (nothing inherited from their parents)
// that lets in only this user and SYSTEM, with full access, the folder's entries inherited by
// what is made in it. The owner is left as Windows sets it: this user, or the token's default
// owner (the Administrators group when elevated). This is the rule the app uses for its own
// private files (app/src-tauri/src/private_windows.rs).
//
// The security calls go through advapi32.dll, which the standard library's syscall package
// registers as a system DLL, so it is loaded from System32 only.

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procStringToSD                = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSecurityDescriptorDacl = advapi32.NewProc("GetSecurityDescriptorDacl")
	procSetNamedSecurityInfo      = advapi32.NewProc("SetNamedSecurityInfoW")
)

const (
	sddlRevision1                    = 1
	seFileObject                     = 1
	daclSecurityInformation          = 0x4
	protectedDaclSecurityInformation = 0x80000000
)

// privateDescriptor returns a security descriptor whose access list is the one described
// above, for a folder (its entries are inherited by what is made in it) or a file. Free it
// with syscall.LocalFree.
func privateDescriptor(folder bool) (uintptr, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return 0, fmt.Errorf("credential store: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return 0, fmt.Errorf("credential store: %w", err)
	}
	me, err := user.User.Sid.String()
	if err != nil {
		return 0, fmt.Errorf("credential store: %w", err)
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

// makeDir makes dir, and any missing parents, and gives dir the private access list: at its
// creation when it is new, else in place of the one it has. Setting it in place also passes its
// entries on to the files already in the folder that inherit theirs (records saved before this
// rule), so they lose what they inherited from the old list.
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
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if !st.IsDir() {
		return &os.PathError{Op: "mkdir", Path: dir, Err: syscall.ENOTDIR}
	}
	var present, defaulted int32
	var dacl uintptr
	if r, _, e := procGetSecurityDescriptorDacl.Call(sd, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted))); r == 0 {
		return fmt.Errorf("credential store: security descriptor: %w", e)
	}
	if r, _, _ := procSetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(path)), seFileObject,
		daclSecurityInformation|protectedDaclSecurityInformation, 0, 0, dacl, 0); r != 0 {
		return &os.PathError{Op: "set access list", Path: dir, Err: syscall.Errno(r)}
	}
	return nil
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
