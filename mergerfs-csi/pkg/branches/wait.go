package branches

import (
	"context"
	"fmt"
	"os"
	"time"
)

// WaitForDirs polls until every path exists as a directory or ctx/timeout expires.
func WaitForDirs(ctx context.Context, paths []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		missing := missingDirs(paths)
		if len(missing) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for branch dirs: %v", missing)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func missingDirs(paths []string) []string {
	var missing []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			missing = append(missing, p)
		}
	}
	return missing
}
