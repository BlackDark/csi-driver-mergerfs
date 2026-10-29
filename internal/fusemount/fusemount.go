// Package fusemount opens /dev/fuse and mounts it for a Mount Pod (ADR 0001). The
// node plugin calls it when the fusermount shim in the Mount Pod asks for a mount.
package fusemount

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// FSType is the type every pool mount gets, whatever subtype mergerfs asks for.
const FSType = "fuse.mergerfs"

// rootMode is S_IFDIR in octal, as the kernel expects it in the mount data.
const rootMode = "40000"

// ErrUnsupported is returned on platforms without Linux FUSE mount(2).
var ErrUnsupported = errors.New("fusemount: only supported on linux")

// Opts are the kernel-side mount settings.
type Opts struct {
	Source             string // shown in the mount table (mergerfs fsname)
	ReadOnly           bool
	NoExec, NoAtime    bool
	UID, GID           int // the Mount Pod's uid/gid: the FUSE connection owner
	DefaultPermissions bool
	MaxRead, BlkSize   int // 0: kernel default
}

// Data builds the mount(2) data string. allow_other is always set: without it only
// UID could access the mount and workload pods would get EACCES.
func Data(fd int, o Opts) string {
	parts := []string{
		"fd=" + strconv.Itoa(fd),
		"rootmode=" + rootMode,
		"user_id=" + strconv.Itoa(o.UID),
		"group_id=" + strconv.Itoa(o.GID),
		"allow_other",
	}
	if o.DefaultPermissions {
		parts = append(parts, "default_permissions")
	}
	if o.MaxRead > 0 {
		parts = append(parts, "max_read="+strconv.Itoa(o.MaxRead))
	}
	if o.BlkSize > 0 {
		parts = append(parts, "blksize="+strconv.Itoa(o.BlkSize))
	}
	return strings.Join(parts, ",")
}

// flagOpts are mount flags the shim may pass; suid and dev are refused, nosuid and
// nodev are always set.
var flagOpts = map[string]func(*Opts){
	"rw": func(o *Opts) { o.ReadOnly = false }, "ro": func(o *Opts) { o.ReadOnly = true },
	"nosuid": nil, "nodev": nil, "exec": func(o *Opts) { o.NoExec = false },
	"noexec": func(o *Opts) { o.NoExec = true }, "atime": func(o *Opts) { o.NoAtime = false },
	"noatime": func(o *Opts) { o.NoAtime = true }, "relatime": nil, "norelatime": nil,
	"nodiratime": nil, "diratime": nil, "lazytime": nil, "nolazytime": nil,
	"async": nil, "strictatime": nil, "nostrictatime": nil,
}

// ignoredOpts are accepted but have no kernel effect here: the plugin owns
// allow_other, uid/gid and unmounting (auto_unmount is how mergerfs is made to use
// the shim at all; see ADR 0001).
var ignoredOpts = map[string]bool{"allow_other": true, "auto_unmount": true, "large_read": true}

// ParseOpts maps the -o string libfuse passes to fusermount onto Opts. Anything not
// on the allowlist is an error naming the option, so the mount fails instead of
// silently getting other semantics.
func ParseOpts(s string, uid, gid int) (Opts, error) {
	o := Opts{UID: uid, GID: gid}
	for _, opt := range splitOpts(s) {
		key, val, hasVal := strings.Cut(opt, "=")
		switch {
		case opt == "":
		case !hasVal && ignoredOpts[key]:
		case !hasVal && key == "default_permissions":
			o.DefaultPermissions = true
		case hasVal && (key == "fsname" || key == "subtype"):
			if key == "fsname" {
				o.Source = val
			}
		case hasVal && (key == "max_read" || key == "blksize"):
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return Opts{}, fmt.Errorf("fusemount: invalid %s %q", key, val)
			}
			if key == "max_read" {
				o.MaxRead = n
			} else {
				o.BlkSize = n
			}
		default:
			set, ok := flagOpts[opt]
			if !ok {
				return Opts{}, fmt.Errorf("fusemount: option %q not allowed", opt)
			}
			if set != nil {
				set(&o)
			}
		}
	}
	if o.Source == "" {
		o.Source = "mergerfs"
	}
	return o, nil
}

// splitOpts splits on commas not escaped with a backslash (libfuse escapes them in
// fsname) and unescapes.
func splitOpts(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}
