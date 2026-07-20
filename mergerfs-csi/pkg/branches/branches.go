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
	default:
		return fmt.Errorf("unknown type %q", b.Type)
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
		return filepath.Clean(b.Path), nil
	case TypePVCRef, TypeCSIRef:
		if podUID == "" {
			return "", fmt.Errorf("%s requires pod UID", b.Type)
		}
		vol := b.VolumeName
		return filepath.Join(r.KubeletPodsPath, podUID, "volumes", "kubernetes.io~csi", vol, "mount"), nil
	case TypeEmptyDirRef:
		if podUID == "" {
			return "", fmt.Errorf("emptyDirRef requires pod UID")
		}
		return filepath.Join(r.KubeletPodsPath, podUID, "volumes", "kubernetes.io~empty-dir", b.VolumeName), nil
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
