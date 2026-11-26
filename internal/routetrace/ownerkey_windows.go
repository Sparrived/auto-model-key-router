package routetrace

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openOwnerKey(path string, create bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var attrs *windows.SecurityAttributes
	access, disposition := uint32(windows.GENERIC_READ|windows.READ_CONTROL), uint32(windows.OPEN_EXISTING)
	if create {
		// 用受保护的 DACL 原子创建；绝不向一个可继承公共权限的文件写 key。
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
		if err != nil {
			return nil, err
		}
		attrs = &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		access, disposition = access|windows.GENERIC_WRITE, windows.CREATE_NEW
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, attrs, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if !plainWindowsFile(h) || !privateWindowsACL(h, user.User.Sid) {
		_ = f.Close()
		if create {
			_ = os.Remove(path)
		}
		return nil, errUnsafeKey
	}
	return f, nil
}

func plainWindowsFile(h windows.Handle) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(h, &info) == nil && info.NumberOfLinks == 1 &&
		info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) == 0
}

func privateWindowsACL(h windows.Handle, user *windows.SID) bool {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && sid.IsValid() && (sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || defaulted || dacl == nil || dacl.AceCount == 0 {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil || ace.Header.AceSize < 16 {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return false
			}
		case windows.ACCESS_DENIED_ACE_TYPE:
			// 拒绝条目不能扩大访问范围。
		default:
			return false
		}
	}
	return true
}

func publishOwnerKey(tmp, path string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func lockInstance(dir string) (*os.File, error) {
	path := filepath.Join(dir, "route-trace.lock")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if !plainWindowsFile(h) {
		_ = f.Close()
		return nil, errUnsafeKey
	}
	return f, nil
}
