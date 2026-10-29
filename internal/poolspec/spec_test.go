package poolspec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var allowed = []string{"/srv/media", "/mnt/disk1/"}

func parse(attrs map[string]string) (Spec, error) {
	return Parse(Input{Attributes: attrs, AllowedHostPaths: allowed})
}

func TestParseMediaLayout(t *testing.T) {
	s, err := parse(map[string]string{
		KeyBranches: `[{"type":"hostPath","path":"/srv/media/local/"},{"type":"pvcRef","volumeName":"remote","mode":"NC"}]`,
		KeyOptions:  "category.create=pfrd, func.getattr=newest,cache.files=off,dropcacheonclose=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Branch{{Type: TypeHostPath, Path: "/srv/media/local", Mode: ModeRW}, {Type: TypePVCRef, VolumeName: "remote", Mode: ModeNC}}
	if !reflect.DeepEqual(s.Branches, want) {
		t.Errorf("branches: %+v", s.Branches)
	}
	if got := s.MountOptions(); got != "auto_unmount,category.create=pfrd,func.getattr=newest,cache.files=off,dropcacheonclose=false" {
		t.Errorf("options: %s", got)
	}
	arg, err := BranchArg([]string{"/srv/media/local", "/var/lib/kubelet/pods/u/volumes/kubernetes.io~csi/pv/mount"}, s.Branches)
	if err != nil || arg != "/srv/media/local=RW:/var/lib/kubelet/pods/u/volumes/kubernetes.io~csi/pv/mount=NC" {
		t.Errorf("branch arg: %s %v", arg, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct {
		attrs map[string]string
		want  string
	}{
		"no branches":     {map[string]string{}, "branches is required"},
		"empty list":      {map[string]string{KeyBranches: "[]"}, "at least one"},
		"bad json":        {map[string]string{KeyBranches: "{"}, "branches"},
		"unknown field":   {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media","driverHint":"x"}]`}, "driverHint"},
		"bad mode":        {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media","mode":"rw"}]`}, `mode "rw"`},
		"outside allow":   {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/etc"}]`}, `"/etc" is not under`},
		"prefix trick":    {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media2"}]`}, "not under"},
		"dotdot":          {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media/../../etc"}]`}, "not under"},
		"relative":        {map[string]string{KeyBranches: `[{"type":"hostPath","path":"srv"}]`}, "absolute"},
		"colon":           {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media/a:b"}]`}, "contains"},
		"glob":            {map[string]string{KeyBranches: `[{"type":"hostPath","path":"/srv/media/*"}]`}, "contains"},
		"ref with path":   {map[string]string{KeyBranches: `[{"type":"pvcRef","path":"/x","volumeName":"a"}]`}, "takes volumeName"},
		"bad volume name": {map[string]string{KeyBranches: `[{"type":"emptyDirRef","volumeName":"../x"}]`}, "not a valid"},
		"unknown type":    {map[string]string{KeyBranches: `[{"type":"nfs"}]`}, "unknown type"},
		"option":          {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "log.file=/x"}, `"log.file" is not allowed`},
		"reserved":        {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "branches=/", KeyAllowAllOptions: "true"}, "set by the driver"},
		"auto_unmount":    {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "auto_unmount"}, "set by the driver"},
		"bad func":        {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "func.evil=ff"}, "not allowed"},
		"space":           {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "fsname=a b"}, "whitespace"},
		"inline unrestricted": {map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyAllowAllOptions: "true",
			keyEphemeral: "true"}, "not allowed for inline"},
	}
	for name, c := range cases {
		_, err := parse(c.attrs)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestUnrestricted(t *testing.T) {
	s, err := parse(map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`,
		KeyOptions: "log.file=/tmp/x,link_cow=true", KeyAllowAllOptions: "true"})
	if err != nil || !s.Unrestricted || len(s.Options) != 2 {
		t.Errorf("got %+v %v", s, err)
	}
}

func TestUnderscoreKeys(t *testing.T) {
	if _, err := parse(map[string]string{KeyBranches: `[{"type":"csiRef","volumeName":"a"}]`, KeyOptions: "link_cow=true,flush_on_close=never"}); err != nil {
		t.Error(err)
	}
}

func TestAllowedRoot(t *testing.T) {
	if r := AllowedRoot("/mnt/disk1/x", allowed); r != "/mnt/disk1" {
		t.Errorf("got %q", r)
	}
	if r := AllowedRoot("/", []string{"/"}); r != "/" {
		t.Errorf("root: got %q", r)
	}
	if r := AllowedRoot("/mnt/disk10", allowed); r != "" {
		t.Errorf("got %q", r)
	}
}
