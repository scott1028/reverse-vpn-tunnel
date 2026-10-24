package netsys

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// LockPath is the per-role lock that keeps a second instance away from the
// setup of a running one.
func LockPath(role string) string {
	return "/var/run/reverse-vpn-tunnel-" + role + ".lock"
}

// Lock takes an exclusive lock on path without waiting. It is held until the
// returned file is closed or the process exits.
func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another instance is running (lock %s held)", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}
