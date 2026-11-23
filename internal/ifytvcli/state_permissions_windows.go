package ifytvcli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	stateAdvapi      = syscall.NewLazyDLL("advapi32.dll")
	convertStateSDDL = stateAdvapi.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getStateSecurity = stateAdvapi.NewProc("GetSecurityInfo")
	getStateACE      = stateAdvapi.NewProc("GetAce")
	getStateVolume   = kernel32.NewProc("GetVolumeInformationByHandleW")
)

func stateUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String()
}

// Apply the descriptor at creation. Tightening an inherited ACL afterwards
// would leave any read handles already opened by another user valid.
func stateAttributes(sid, inheritance string) (*syscall.SecurityAttributes, error) {
	sddl := "O:" + sid + "D:P(A;" + inheritance + ";FA;;;" + sid + ")(A;" + inheritance + ";FA;;;SY)(A;" + inheritance + ";FA;;;BA)"
	text, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return nil, err
	}
	var sa syscall.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	ok, _, callErr := convertStateSDDL.Call(uintptr(unsafe.Pointer(text)), 1, uintptr(unsafe.Pointer(&sa.SecurityDescriptor)), 0)
	if ok == 0 {
		return nil, callErr
	}
	return &sa, nil
}

func ensureStateDir(dir string) error {
	// The user's root and its runs directory may be intentionally shared.
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	sid, err := stateUserSID()
	if err != nil {
		return err
	}
	sa, err := stateAttributes(sid, "OICI")
	if err != nil {
		return err
	}
	defer syscall.LocalFree(syscall.Handle(sa.SecurityDescriptor))
	name, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	err = syscall.CreateDirectory(name, sa)
	created := err == nil
	if !created && !errors.Is(err, os.ErrExist) {
		return err
	}
	const readControlAndAttributes = 0x00020000 | 0x80
	f, err := openStateObject(dir, sid, readControlAndAttributes, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, nil, created)
	if err != nil {
		return err
	}
	return f.Close()
}

func openStateLock(path string) (*os.File, error) {
	return createStateFile(path, syscall.OPEN_ALWAYS)
}

func openStateRecord(path string) (*os.File, error) {
	sid, err := stateUserSID()
	if err != nil {
		return nil, err
	}
	return openStateObject(path, sid, syscall.GENERIC_READ, syscall.OPEN_EXISTING, 0, nil, false)
}

func createStateTemp(dir string) (*os.File, error) {
	for {
		id, err := newKey()
		if err != nil {
			return nil, err
		}
		f, err := createStateFile(filepath.Join(dir, ".run-"+id), syscall.CREATE_NEW)
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
}

func createStateFile(path string, disposition uint32) (*os.File, error) {
	sid, err := stateUserSID()
	if err != nil {
		return nil, err
	}
	sa, err := stateAttributes(sid, "")
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(sa.SecurityDescriptor))
	return openStateObject(path, sid, syscall.GENERIC_READ|syscall.GENERIC_WRITE, disposition, 0, sa, disposition == syscall.CREATE_NEW)
}

// Check the kernel object's security on the same handle used for subsequent
// reads and writes. Legacy private, inherited ACLs can still be opened.
func openStateObject(path, sid string, access, disposition, flags uint32, sa *syscall.SecurityAttributes, requireProtected bool) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	const securityAnonymousSQOS = 0x00100000
	h, err := syscall.CreateFile(name, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, sa, disposition,
		flags|syscall.FILE_FLAG_OPEN_REPARSE_POINT|securityAnonymousSQOS, 0)
	if err != nil {
		return nil, err
	}
	wantDir := flags&syscall.FILE_FLAG_BACKUP_SEMANTICS != 0
	if !privateStateObject(h, sid, wantDir, requireProtected) {
		syscall.CloseHandle(h)
		if disposition == syscall.CREATE_NEW {
			os.Remove(path) // Our empty, uniquely created temporary file.
		}
		return nil, errUnsafeStateACL
	}
	return os.NewFile(uintptr(h), path), nil
}

// Fixed prefixes of Win32 SECURITY_DESCRIPTOR, ACL and ACCESS_ALLOWED_ACE.
// GetSecurityInfo returns them in one allocation released with LocalFree.
type stateSD struct {
	revision, reserved byte
	control            uint16
}

type stateACL struct {
	revision, reserved byte
	size, count, pad   uint16
}

type stateACE struct {
	kind, flags byte
	size        uint16
	mask        uint32
	sidStart    uint32
}

func privateStateObject(h syscall.Handle, user string, directory, requireProtected bool) bool {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil ||
		info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return false
	}
	const persistentACLs = 0x00000008
	var volumeFlags uint32
	ok, _, _ := getStateVolume.Call(uintptr(h), 0, 0, 0, 0, uintptr(unsafe.Pointer(&volumeFlags)), 0, 0)
	if ok == 0 || volumeFlags&persistentACLs == 0 {
		return false
	}
	const fileObject, ownerAndDACL = 1, 0x01 | 0x04
	var sd *stateSD
	var acl *stateACL
	var owner *syscall.SID
	status, _, _ := getStateSecurity.Call(uintptr(h), fileObject, ownerAndDACL, uintptr(unsafe.Pointer(&owner)),
		0, uintptr(unsafe.Pointer(&acl)), 0, uintptr(unsafe.Pointer(&sd)))
	if status != 0 || sd == nil {
		return false
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(sd))))
	const protectedDACL = 0x1000
	if acl == nil || owner == nil || (requireProtected && sd.control&protectedDACL == 0) {
		return false
	}
	const localSystem, administrators, creatorOwner = "S-1-5-18", "S-1-5-32-544", "S-1-3-0"
	trusted := func(sid string) bool { return sid == user || sid == localSystem || sid == administrators }
	ownerSID, err := owner.String()
	if err != nil || !trusted(ownerSID) {
		return false // An object's owner can change its DACL.
	}
	const allow, deny, inheritOnly = 0, 1, 0x08
	for i := uint16(0); i < acl.count; i++ {
		var ace *stateACE
		ok, _, _ := getStateACE.Call(uintptr(unsafe.Pointer(acl)), uintptr(i), uintptr(unsafe.Pointer(&ace)))
		if ok == 0 || ace == nil {
			return false
		}
		if ace.kind == deny {
			continue
		}
		if ace.kind != allow {
			return false // An unfamiliar grant cannot establish privacy.
		}
		sid, err := (*syscall.SID)(unsafe.Pointer(&ace.sidStart)).String()
		if err != nil || !(trusted(sid) || (directory && ace.flags&inheritOnly != 0 && sid == creatorOwner)) {
			return false
		}
	}
	return true
}
