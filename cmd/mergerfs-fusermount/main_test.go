package main

import "testing"

func TestParseArgs(t *testing.T) {
	cases := map[string]struct {
		args []string
		want request
	}{
		"mount":   {[]string{"-o", "rw,fsname=x", "--", "/mnt"}, request{opts: "rw,fsname=x", mountpoint: "/mnt"}},
		"no dash": {[]string{"/mnt"}, request{mountpoint: "/mnt"}},
		"unmount": {[]string{"-u", "-q", "-z", "--", "/mnt"}, request{unmount: true, mountpoint: "/mnt"}},
	}
	for name, c := range cases {
		got, err := parseArgs(c.args)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v %v", name, got, err)
		}
	}
	for _, bad := range [][]string{{}, {"-o"}, {"-x", "/m"}, {"--", "/a", "/b"}, {"/a", "/b"}} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestRunNeedsCommFD(t *testing.T) {
	env := map[string]string{SocketEnv: "/nonexistent"}
	if err := run([]string{"--", "/mnt"}, func(k string) string { return env[k] }); err == nil {
		t.Error("want error without _FUSE_COMMFD")
	}
}
