//go:build linux

package mergerfs

import (
	"bufio"
	"os"
	"strings"
)

func isMountPoint(target string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		// Fall back: treat as not mounted if we can't check.
		return false, nil
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), " ")
		if len(fields) < 5 {
			continue
		}
		if fields[4] == target {
			return true, nil
		}
	}
	return false, sc.Err()
}
