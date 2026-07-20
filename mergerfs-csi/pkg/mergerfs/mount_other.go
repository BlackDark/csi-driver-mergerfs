//go:build !linux

package mergerfs

import (
	"os"
	"path/filepath"
)

// isMountPoint on non-Linux uses a conservative check: target exists as dir.
// Real FUSE mount detection is Linux-only; tests inject fake Mounter.
func isMountPoint(target string) (bool, error) {
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.IsDir() {
		return false, nil
	}
	// Marker file written by fake runner in tests is not used here.
	// Without /proc/mountinfo we cannot know; assume not mounted.
	_ = filepath.Clean(target)
	return false, nil
}
