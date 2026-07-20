package branches

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	TypeHostPath    = "hostPath"
	TypePVCRef      = "pvcRef"
	TypeEmptyDirRef = "emptyDirRef"
	TypeCSIRef      = "csiRef"
)

// Branch describes one mergerfs branch source from volume context JSON.
type Branch struct {
	Type       string `json:"type"`
	Path       string `json:"path,omitempty"`
	VolumeName string `json:"volumeName,omitempty"`
	DriverHint string `json:"driverHint,omitempty"`
}

// Parse unmarshals a JSON array of branch objects.
func Parse(raw string) ([]Branch, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("branches: empty")
	}
	var bs []Branch
	if err := json.Unmarshal([]byte(raw), &bs); err != nil {
		return nil, fmt.Errorf("branches: parse: %w", err)
	}
	if len(bs) == 0 {
		return nil, fmt.Errorf("branches: empty array")
	}
	for i, b := range bs {
		if err := validate(b); err != nil {
			return nil, fmt.Errorf("branches[%d]: %w", i, err)
		}
	}
	return bs, nil
}

func validate(b Branch) error {
	switch b.Type {
	case TypeHostPath:
		if b.Path == "" {
			return fmt.Errorf("hostPath requires path")
		}
		if !filepath.IsAbs(b.Path) {
			return fmt.Errorf("hostPath path must be absolute")
		}
	case TypePVCRef, TypeEmptyDirRef, TypeCSIRef:
		if b.VolumeName == "" {
			return fmt.Errorf("%s requires volumeName", b.Type)
		}
		if err := sanitizePathElement(b.VolumeName, "volumeName"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown type %q", b.Type)
	}
	return nil
}

// sanitizePathElement rejects empty, "/", "\", "..", or multi-component names.
func sanitizePathElement(s, field string) error {
	if s == "" {
		return fmt.Errorf("%s empty", field)
	}
	if strings.Contains(s, "/") || strings.Contains(s, `\`) || strings.Contains(s, "..") {
		return fmt.Errorf("%s contains illegal path characters: %q", field, s)
	}
	if filepath.Base(s) != s {
		return fmt.Errorf("%s must be a single path element: %q", field, s)
	}
	return nil
}

// Resolver maps branch specs to host paths under kubelet.
type Resolver struct {
	KubeletPodsPath string
}

// Resolve returns the absolute host path for a branch given pod UID.
func (r Resolver) Resolve(b Branch, podUID string) (string, error) {
	switch b.Type {
	case TypeHostPath:
		p := filepath.Clean(b.Path)
		if strings.Contains(p, ":") {
			return "", fmt.Errorf("branch path must not contain ':': %q", p)
		}
		return p, nil
	case TypePVCRef, TypeCSIRef:
		if err := sanitizePathElement(podUID, "podUID"); err != nil {
			return "", fmt.Errorf("%s requires valid pod UID: %w", b.Type, err)
		}
		if err := sanitizePathElement(b.VolumeName, "volumeName"); err != nil {
			return "", err
		}
		p := filepath.Join(r.KubeletPodsPath, podUID, "volumes", "kubernetes.io~csi", b.VolumeName, "mount")
		if strings.Contains(p, ":") {
			return "", fmt.Errorf("branch path must not contain ':': %q", p)
		}
		return p, nil
	case TypeEmptyDirRef:
		if err := sanitizePathElement(podUID, "podUID"); err != nil {
			return "", fmt.Errorf("emptyDirRef requires valid pod UID: %w", err)
		}
		if err := sanitizePathElement(b.VolumeName, "volumeName"); err != nil {
			return "", err
		}
		// emptyDir has no /mount suffix (unlike csi/pvc).
		p := filepath.Join(r.KubeletPodsPath, podUID, "volumes", "kubernetes.io~empty-dir", b.VolumeName)
		if strings.Contains(p, ":") {
			return "", fmt.Errorf("branch path must not contain ':': %q", p)
		}
		return p, nil
	default:
		return "", fmt.Errorf("unknown type %q", b.Type)
	}
}

// ResolveAll resolves every branch in order.
func (r Resolver) ResolveAll(bs []Branch, podUID string) ([]string, error) {
	out := make([]string, 0, len(bs))
	for i, b := range bs {
		p, err := r.Resolve(b, podUID)
		if err != nil {
			return nil, fmt.Errorf("branches[%d]: %w", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}
