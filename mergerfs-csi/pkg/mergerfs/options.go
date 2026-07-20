package mergerfs

import (
	"fmt"
	"regexp"
	"strings"
)

var safeOpts = regexp.MustCompile(`^[a-zA-Z0-9=,.\/:_-]+$`)

// SanitizeOptions rejects shell metacharacters; empty is allowed (caller may use default).
func SanitizeOptions(opts string) (string, error) {
	opts = strings.TrimSpace(opts)
	if opts == "" {
		return "", nil
	}
	if !safeOpts.MatchString(opts) {
		return "", fmt.Errorf("mergerfs options contain unsafe characters: %q", opts)
	}
	return opts, nil
}

// BuildArgs builds mergerfs argv (without binary path): -f [-o opts] branches target.
func BuildArgs(opts string, branches []string, target string) ([]string, error) {
	if len(branches) == 0 {
		return nil, fmt.Errorf("no branches")
	}
	if target == "" {
		return nil, fmt.Errorf("empty target")
	}
	sanitized, err := SanitizeOptions(opts)
	if err != nil {
		return nil, err
	}
	args := []string{"-f"}
	if sanitized != "" {
		args = append(args, "-o", sanitized)
	}
	args = append(args, strings.Join(branches, ":"), target)
	return args, nil
}
