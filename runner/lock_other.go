//go:build !unix

package runner

import "os"

// Windows has no flock. The runner is deployed on Linux and the lock exists to
// keep two Unix processes off one working copy, so rather than pretend, this
// build takes the file and relies on the in-process guard alone.
func tryLock(file *os.File) (bool, error) {
	_ = file
	return true, nil
}

func unlock(file *os.File) error {
	_ = file
	return nil
}
