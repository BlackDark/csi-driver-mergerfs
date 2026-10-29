package main

import (
	"io"
	"reflect"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"help":       {[]string{"--help"}, 0},
		"node help":  {[]string{"node", "--help"}, 0},
		"no mode":    {nil, 2},
		"unknown":    {[]string{"bogus"}, 2},
		"ctrl args":  {[]string{"controller", "x"}, 2},
		"node usage": {[]string{"node", "x"}, 2},
	}
	for name, c := range cases {
		if got := run(c.args, io.Discard); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

func TestNodeFlags(t *testing.T) {
	t.Setenv("NODE_ID", "n1")
	t.Setenv("POD_NAMESPACE", "drv")
	f, err := parseNodeFlags([]string{"--mount-image=img", `--mount-tolerations=[{"operator":"Exists"}]`,
		"--allowed-host-paths=/srv/media/, /mnt/disk1"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	p := f.cfg.Pod
	if f.cfg.NodeID != "n1" || p.Namespace != "drv" || p.UID != 65532 || len(p.Tolerations) != 1 ||
		p.KubeletDir != "/var/lib/kubelet" || f.cfg.PublishTimeout.String() != "1m30s" ||
		p.Resources.Requests.Memory().String() != "32Mi" || f.cfg.HealWorkloads {
		t.Errorf("flags: %+v", f.cfg)
	}
	if !reflect.DeepEqual(f.cfg.AllowedHostPaths, []string{"/srv/media", "/mnt/disk1"}) {
		t.Errorf("roots: %v", f.cfg.AllowedHostPaths)
	}
	for _, args := range [][]string{{}, {"--mount-image=img", "--mount-uid=0"}, {"--mount-image=img", "--mount-tolerations=x"},
		{"--mount-image=img", "--mount-resources=x"}, {"--mount-image=img", "--allowed-host-paths=srv"},
		{"--mount-image=img", "--allowed-host-paths=/a:b"}} {
		if _, err := parseNodeFlags(args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
