package db

import "testing"

func TestBuildValues(t *testing.T) {
	cases := []struct {
		n, cols int
		want    string
	}{
		{0, 3, ""},
		{1, 2, "(?,?)"},
		{3, 2, "(?,?),(?,?),(?,?)"},
		{2, 1, "(?),(?)"},
	}
	for _, c := range cases {
		if got := buildValues(c.n, c.cols); got != c.want {
			t.Errorf("buildValues(%d,%d)=%q, want %q", c.n, c.cols, got, c.want)
		}
	}
}
