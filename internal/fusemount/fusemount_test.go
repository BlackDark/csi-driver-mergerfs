package fusemount

import (
	"strings"
	"testing"
)

func TestData(t *testing.T) {
	cases := map[string]struct {
		o    Opts
		want string
	}{
		"plain": {Opts{UID: 1000, GID: 2000}, "fd=7,rootmode=40000,user_id=1000,group_id=2000,allow_other"},
		"all": {Opts{UID: 1, GID: 1, DefaultPermissions: true, MaxRead: 4096, BlkSize: 512},
			"fd=7,rootmode=40000,user_id=1,group_id=1,allow_other,default_permissions,max_read=4096,blksize=512"},
	}
	for name, c := range cases {
		if got := Data(7, c.o); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestParseOpts(t *testing.T) {
	// What mergerfs 2.42 passes with -o auto_unmount,fsname=media.
	o, err := ParseOpts(`rw,nosuid,nodev,allow_other,default_permissions,fsname=me\,dia,auto_unmount,subtype=mergerfs`, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := Opts{Source: "me,dia", UID: 10, GID: 20, DefaultPermissions: true}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}
	o, err = ParseOpts("ro,noexec,noatime,max_read=131072", 1, 1)
	if err != nil || !o.ReadOnly || !o.NoExec || !o.NoAtime || o.MaxRead != 131072 || o.Source != "mergerfs" {
		t.Errorf("got %+v, %v", o, err)
	}
}

func TestParseOptsRejects(t *testing.T) {
	for _, s := range []string{"suid", "dev", "context=x", "user=bob", "blkdev", "max_read=-1", "fd=3", "rootmode=0", "user_id=0"} {
		if _, err := ParseOpts("rw,"+s, 1, 1); err == nil || !strings.Contains(err.Error(), strings.SplitN(s, "=", 2)[0]) {
			t.Errorf("%s: got %v", s, err)
		}
	}
}
