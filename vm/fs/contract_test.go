package fs

import (
	stdfs "io/fs"
	"testing"
)

func TestModeBitsKeepsOnlyRequestedBits(t *testing.T) {
	for _, test := range []struct {
		name string
		in   stdfs.FileMode
		want stdfs.FileMode
	}{
		{"权限位原样", 0o644, 0o644},
		{"目录类型位被剥掉", stdfs.ModeDir | 0o755, 0o755},
		{"符号链接类型位被剥掉", stdfs.ModeSymlink | 0o777, 0o777},
		{
			"setuid/setgid/sticky 保留",
			stdfs.ModeDir | stdfs.ModeSetuid | stdfs.ModeSetgid | stdfs.ModeSticky | 0o750,
			stdfs.ModeSetuid | stdfs.ModeSetgid | stdfs.ModeSticky | 0o750,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ModeBits(test.in); got != test.want {
				t.Fatalf("ModeBits(%v) = %v，期望 %v", test.in, got, test.want)
			}
		})
	}
}
