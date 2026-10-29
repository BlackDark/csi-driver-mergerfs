// Package poolspec parses a volume's attributes into a Pool Spec: the branches and
// mergerfs options of one pool (docs/reference/volume-parameters.md). Pure: no I/O.
package poolspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Volume attribute keys.
const (
	KeyBranches        = "branches"
	KeyOptions         = "mergerfsOptions"
	KeyAllowAllOptions = "allowAllOptions"
	keyEphemeral       = "csi.storage.k8s.io/ephemeral"
)

// Branch types.
const (
	TypeHostPath    = "hostPath"
	TypePVCRef      = "pvcRef"
	TypeEmptyDirRef = "emptyDirRef"
	TypeCSIRef      = "csiRef"
)

// Branch modes, mergerfs' path=MODE.
const (
	ModeRW = "RW"
	ModeRO = "RO"
	ModeNC = "NC"
)

// ErrInvalid marks user errors; messages name keys and branch indexes.
var ErrInvalid = errors.New("invalid volume attributes")

// Branch is one entry of the branches attribute.
type Branch struct {
	Type       string `json:"type"`
	Path       string `json:"path,omitempty"`       // hostPath
	VolumeName string `json:"volumeName,omitempty"` // *Ref: the consumer pod's volume name
	Mode       string `json:"mode,omitempty"`       // RW (default), RO or NC
}

// Spec is one pool's parsed attributes.
type Spec struct {
	Branches []Branch
	// Options are mergerfs -o entries, validated; never the ones the driver forces.
	Options []string
	// Unrestricted: allowAllOptions skipped the option allowlist.
	Unrestricted bool
}

// Input is what Parse needs besides the attributes.
type Input struct {
	Attributes map[string]string
	// AllowedHostPaths are the admin's hostPath roots (node flag); none disables hostPath.
	AllowedHostPaths []string
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Parse validates the attributes into a Spec.
func Parse(in Input) (Spec, error) {
	attrs := in.Attributes
	bs, err := parseBranches(attrs[KeyBranches], in.AllowedHostPaths)
	if err != nil {
		return Spec{}, err
	}
	s := Spec{Branches: bs}
	switch v := attrs[KeyAllowAllOptions]; v {
	case "", "false":
	case "true":
		if attrs[keyEphemeral] == "true" {
			return Spec{}, invalid("%s is not allowed for inline volumes; set it on a PersistentVolume", KeyAllowAllOptions)
		}
		s.Unrestricted = true
	default:
		return Spec{}, invalid("%s must be true or false", KeyAllowAllOptions)
	}
	s.Options, err = parseOptions(attrs[KeyOptions], s.Unrestricted)
	if err != nil {
		return Spec{}, err
	}
	return s, nil
}

func parseBranches(raw string, allowed []string) ([]Branch, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, invalid("%s is required", KeyBranches)
	}
	var bs []Branch
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bs); err != nil {
		return nil, invalid("%s: %v", KeyBranches, err)
	}
	if len(bs) == 0 {
		return nil, invalid("%s must list at least one branch", KeyBranches)
	}
	for i := range bs {
		if err := validateBranch(&bs[i], allowed); err != nil {
			return nil, invalid("%s[%d]: %v", KeyBranches, i, err)
		}
	}
	return bs, nil
}

func validateBranch(b *Branch, allowed []string) error {
	switch b.Mode {
	case "":
		b.Mode = ModeRW
	case ModeRW, ModeRO, ModeNC:
	default:
		return fmt.Errorf("mode %q: want RW, RO or NC", b.Mode)
	}
	switch b.Type {
	case TypeHostPath:
		if b.VolumeName != "" {
			return errors.New("hostPath takes path, not volumeName")
		}
		return checkHostPath(b, allowed)
	case TypePVCRef, TypeEmptyDirRef, TypeCSIRef:
		if b.Path != "" {
			return fmt.Errorf("%s takes volumeName, not path", b.Type)
		}
		return checkVolumeName(b.VolumeName)
	}
	return fmt.Errorf("unknown type %q", b.Type)
}

// pathUnsafe are characters mergerfs gives meaning in a branch list (':' separates,
// '=' starts the mode, globs expand) or that break the -o string.
const pathUnsafe = ":=,*?[]\\\n"

func checkHostPath(b *Branch, allowed []string) error {
	if !filepath.IsAbs(b.Path) {
		return fmt.Errorf("hostPath %q must be absolute", b.Path)
	}
	if strings.ContainsAny(b.Path, pathUnsafe) {
		return fmt.Errorf("hostPath %q contains one of %q", b.Path, pathUnsafe)
	}
	b.Path = filepath.Clean(b.Path)
	if AllowedRoot(b.Path, allowed) == "" {
		return fmt.Errorf("hostPath %q is not under an allowed host path (chart allowedHostPaths: %v)", b.Path, allowed)
	}
	return nil
}

// AllowedRoot returns the allowed root containing p, or "" when none does.
func AllowedRoot(p string, allowed []string) string {
	for _, root := range allowed {
		root = filepath.Clean(root)
		if p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/") {
			return root
		}
	}
	return ""
}

// checkVolumeName accepts a DNS-1123 label, which every pod volume name is.
func checkVolumeName(n string) error {
	if n == "" || len(n) > 63 {
		return errors.New("volumeName must be a pod volume name")
	}
	for i, c := range n {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' && i > 0 && i < len(n)-1
		if !ok {
			return fmt.Errorf("volumeName %q is not a valid pod volume name", n)
		}
	}
	return nil
}
