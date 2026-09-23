// internal/tools/policy/su_payload_test.go — TestSuInlinePayload
package policy

import "testing"

func TestSuInlinePayload(t *testing.T) {
	parse := func(cmd string) stage {
		stages, err := parseStages(cmd)
		if err != nil || len(stages) != 1 {
			t.Fatalf("parseStages(%q) = %v, %v", cmd, stages, err)
		}
		return stages[0]
	}
	cases := []struct {
		cmd  string
		want string
		ok   bool
	}{
		{"su -c 'git push'", "git push", true},
		{"su root -c 'git push origin main'", "git push origin main", true},
		{"su -c 'rm -rf /etc'", "rm -rf /etc", true},
		{"su root", "", false},
		{"git push", "", false},
	}
	for _, tc := range cases {
		got, ok := suInlinePayload(parse(tc.cmd))
		if ok != tc.ok || got != tc.want {
			t.Fatalf("suInlinePayload(%q) = %q,%v want %q,%v", tc.cmd, got, ok, tc.want, tc.ok)
		}
	}
}