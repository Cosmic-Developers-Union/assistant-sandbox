package deployment

import "testing"

func TestSupportStringCoversAllStates(t *testing.T) {
	cases := []struct {
		support Support
		want    string
	}{
		{Unsupported, "unsupported"},
		{Partial, "partial"},
		{Supported, "supported"},
	}
	for _, c := range cases {
		if c.support.String() != c.want {
			t.Fatalf("%d -> %q, want %q", c.support, c.support.String(), c.want)
		}
	}

	if Support(42).String() == "supported" {
		t.Fatal("未知状态不应显示为 supported")
	}
}

func TestZeroValueIsUnsupported(t *testing.T) {

	var m Matrix
	if m.Signals != Unsupported || m.TTY != Unsupported {
		t.Fatal("能力矩阵零值应为全不支持")
	}
	if m.Executors["anything"] != Unsupported {
		t.Fatal("未声明执行器应为不支持")
	}
}
