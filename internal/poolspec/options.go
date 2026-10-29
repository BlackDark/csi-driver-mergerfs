package poolspec

import (
	"fmt"
	"strings"
)

// allowedOptions are mergerfs 2.42 options a volume may set (keys with '_' are
// matched as '-', like mergerfs does). Left out on purpose: what moves the pool or
// its branches (branches, srcmounts, mount, mountpoint, config), writes files or
// logs (log.file, debug, pid), changes identities (uid, gid, umask, allow-idmap) or
// needs privileges a Mount Pod lacks (scheduling-priority, proxy-ioprio, readahead,
// passthrough.*). allowAllOptions lifts this list on PersistentVolumes.
var allowedOptions = map[string]bool{
	"async-read": true, "branches-mount-timeout": true, "branches-mount-timeout-fail": true,
	"cache.attr": true, "cache.entry": true, "cache.files": true, "cache.files.process-names": true,
	"cache.negative-entry": true, "cache.readdir": true, "cache.statfs": true, "cache.symlinks": true,
	"cache.writeback": true, "category.action": true, "category.create": true, "category.search": true,
	"direct-io-allow-mmap": true, "dropcacheonclose": true, "export-support": true, "flush-on-close": true,
	"follow-symlinks": true, "fsname": true, "fuse-msg-size": true, "handle-killpriv": true,
	"handle-killpriv-v2": true, "ignorepponrename": true, "inodecalc": true, "kernel-permissions-check": true,
	"link-cow": true, "link-exdev": true, "minfreespace": true, "moveonenospc": true, "never-forget-nodes": true,
	"nfsopenhack": true, "noforget": true, "nullrw": true, "parallel-direct-writes": true, "posix-acl": true,
	"process-thread-count": true, "process-thread-queue-depth": true, "read-thread-count": true,
	"remember-nodes": true, "rename-exdev": true, "security-capability": true, "statfs": true,
	"statfs-ignore": true, "symlinkify": true, "symlinkify-timeout": true, "threads": true, "xattr": true,
}

// reservedOptions belong to the driver even with allowAllOptions: the pool's
// branches, mountpoint and FUSE handoff (auto_unmount routes mounting to the shim).
var reservedOptions = map[string]bool{
	"branches": true, "srcmounts": true, "mount": true, "mountpoint": true, "config": true,
	"auto-unmount": true, "allow-other": true, "default-permissions": true,
}

// optionUnsafe would break the -o string or the Mount Pod argv.
const optionUnsafe = ", \t\n\\\"'"

func normalizeKey(k string) string { return strings.ReplaceAll(k, "_", "-") }

// parseOptions splits "k=v,k2" into validated mergerfs -o entries.
func parseOptions(raw string, unrestricted bool) ([]string, error) {
	var out []string
	for _, opt := range strings.Split(raw, ",") {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		if strings.ContainsAny(opt, optionUnsafe) {
			return nil, invalid("%s: %q contains whitespace, quotes or backslashes", KeyOptions, opt)
		}
		key, _, _ := strings.Cut(opt, "=")
		k := normalizeKey(key)
		switch {
		case reservedOptions[k]:
			return nil, invalid("%s: %q is set by the driver", KeyOptions, key)
		case allowedOptions[k], strings.HasPrefix(k, "func.") && validFuncKey(k):
		case unrestricted:
		default:
			return nil, invalid("%s: %q is not allowed (see docs/reference/volume-parameters.md; %s on a PersistentVolume lifts the list)",
				KeyOptions, key, KeyAllowAllOptions)
		}
		out = append(out, opt)
	}
	return out, nil
}

// validFuncKey accepts func.<name> for the mergerfs functions with a policy.
func validFuncKey(k string) bool {
	switch strings.TrimPrefix(k, "func.") {
	case "access", "chmod", "chown", "create", "getattr", "getxattr", "link", "listxattr", "mkdir",
		"mknod", "open", "readdir", "readlink", "removexattr", "rename", "rmdir", "setxattr", "symlink",
		"truncate", "unlink", "utimens":
		return true
	}
	return false
}

// MountOptions is the -o value of the Mount Pod's mergerfs: auto_unmount first (it
// makes libfuse call the fusermount shim instead of opening /dev/fuse, ADR 0001),
// then the volume's options.
func (s Spec) MountOptions() string {
	return strings.Join(append([]string{"auto_unmount"}, s.Options...), ",")
}

// BranchArg joins resolved branch paths with their modes into mergerfs' branch list.
func BranchArg(paths []string, bs []Branch) (string, error) {
	if len(paths) != len(bs) {
		return "", fmt.Errorf("poolspec: %d paths for %d branches", len(paths), len(bs))
	}
	parts := make([]string, len(paths))
	for i, p := range paths {
		if strings.ContainsAny(p, pathUnsafe) {
			return "", fmt.Errorf("poolspec: branch path %q contains one of %q", p, pathUnsafe)
		}
		parts[i] = p + "=" + bs[i].Mode
	}
	return strings.Join(parts, ":"), nil
}
