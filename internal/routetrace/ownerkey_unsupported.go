//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package routetrace

import "os"

func openOwnerKey(string, bool) (*os.File, error) { return nil, errUnsafeKey }
func publishOwnerKey(string, string) error        { return errUnsafeKey }
func lockInstance(string) (*os.File, error)       { return nil, errUnsafeKey }
