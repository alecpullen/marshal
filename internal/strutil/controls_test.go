package strutil

import "testing"

func TestShowControls(t *testing.T) {
	cases := []struct{ in, want string }{
		{"apt-get update", "apt-get update"},
		{"line one\n\tline two", "line one\n\tline two"},
		{"rm -rf ~/work #\x1b[2K\rapt-get update", `rm -rf ~/work #\x1b[2K\rapt-get update`},
		{"a\x00b\x7fc", `a\x00b\x7fc`},
		{"a\u0085b\u009bc", `a\u0085b\u009bc`},
		{"x\u202eevil\u202c", "x" + `\u202e` + "evil" + `\u202c`},
		{"naïve 日本 ✓", "naïve 日本 ✓"},
	}
	for _, c := range cases {
		if got := ShowControls(c.in); got != c.want {
			t.Errorf("ShowControls(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := ShowControls(ShowControls(c.in)); got != c.want {
			t.Errorf("ShowControls is not idempotent on %q: %q", c.in, got)
		}
	}
}

func TestFirstDisplayControl(t *testing.T) {
	if r, ok := FirstDisplayControl("ls\n\t-la"); ok {
		t.Fatalf("newline and tab are layout, got %U", r)
	}
	if r, ok := FirstDisplayControl("ls \u2066x\x1b"); !ok || r != 0x2066 {
		t.Fatalf("got %U %v, want the isolate first", r, ok)
	}
}
