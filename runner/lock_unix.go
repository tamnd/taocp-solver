//go:build unix

package runner

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive advisory lock without blocking, so the caller can
// keep answering a cancelled context while it waits. The lock is the same
// flock the shell scripts this replaces used, which is what lets a runner and a
// leftover cron job share one content repository safely.
func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch err {
	case nil:
		return true, nil
	case syscall.EWOULDBLOCK:
		return false, nil
	default:
		return false, err
	}
}

func unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
