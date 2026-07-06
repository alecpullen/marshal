package swarm

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantPass bool
		wantOK   bool
	}{
		{"pass", "Ran go test.\nVERDICT: PASS", true, true},
		{"fail", "TestFoo failed at bar.go:42\nVERDICT: FAIL", false, true},
		{"lowercase", "verdict: pass", true, true},
		{"trailing spaces", "VERDICT:  PASS  ", true, true},
		{"no verdict", "tests look fine to me", false, false},
		{"garbage verdict", "VERDICT: MAYBE", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, ok := ParseVerdict(tc.in)
			if pass != tc.wantPass || ok != tc.wantOK {
				t.Fatalf("ParseVerdict(%q) = (%v,%v), want (%v,%v)", tc.in, pass, ok, tc.wantPass, tc.wantOK)
			}
		})
	}
}
