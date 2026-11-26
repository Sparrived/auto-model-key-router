//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package routetrace

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openOwnerKey(path string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	if create {
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL
	}
	return openPrivate(path, flags)
}

func openPrivate(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG ||
		st.Mode&07777 != 0600 || st.Uid != uint32(unix.Geteuid()) || st.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errUnsafeKey
	}
	return os.NewFile(uintptr(fd), path), nil
}

func publishOwnerKey(tmp, path string) error {
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func lockInstance(dir string) (*os.File, error) {
	f, err := openPrivate(filepath.Join(dir, "route-trace.lock"), unix.O_NONBLOCK|unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
