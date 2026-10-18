package progress

import (
	"strings"
	"testing"
)

func TestNormalizeConfigCombinations(t *testing.T) {
	got, err := NormalizeConfig(Config{})
	if err != nil || got.Profile != "generic" || got.Source != "none" {
		t.Fatalf("defaults: %#v %v", got, err)
	}
	for _, profile := range []string{"generic", "tests", "download", "transfer", "custom", "bogus"} {
		for _, source := range []string{"none", "events-v1", "go-test-json", "bogus"} {
			valid := profile != "bogus" && (source == "none" ||
				source == "events-v1" && profile != "generic" ||
				source == "go-test-json" && profile == "tests")
			_, err := NormalizeConfig(Config{Profile: profile, Source: source})
			if (err == nil) != valid {
				t.Fatalf("%s/%s: %v", profile, source, err)
			}
		}
	}
	for _, title := range []string{strings.Repeat("界", 81), string([]byte{0xff})} {
		if _, err := NormalizeConfig(Config{Title: title}); err == nil {
			t.Fatal("accepted invalid title")
		}
	}
}
func TestProgressCleanRemovesControlsAndSecrets(t *testing.T) {
	secret := "AKIA" + "1234567890ABCDEF"
	got := Clean("\x1b[31mred\x1b[0m\x1b]8;;https://example.invalid\x07link\x1b]8;;\x07\x00\u0085\r\n"+secret, 0, false)
	if strings.Contains(got, secret) || strings.ContainsAny(got, "\x1b\x00\u0085\r\n") {
		t.Fatalf("unsafe display: %q", got)
	}
	if !strings.Contains(got, "redlink") {
		t.Fatalf("lost text: %q", got)
	}
	if got := Clean("界界界", 2, false); got != "界界" {
		t.Fatal(got)
	}
	if got := Clean("a\nb\tc", 0, true); got != "a\nb\tc" {
		t.Fatal(got)
	}
}
func TestProgressCloneOwnsAllNestedValues(t *testing.T) {
	total, rate := 10.0, 2.0
	original := &Snapshot{Frame: Frame{Version: 1, Phase: &Phase{ID: "a"},
		Metrics: []Metric{{ID: "bytes", Total: &total, Rate: &rate}},
		Rows:    []Row{{ID: "row", State: "running"}}}}
	copied := Clone(original)
	copied.Phase.ID = "b"
	*copied.Metrics[0].Total = 20
	*copied.Metrics[0].Rate = 4
	copied.Rows[0].State = "done"
	if original.Phase.ID != "a" || total != 10 || rate != 2 || original.Rows[0].State != "running" {
		t.Fatal("clone aliases source")
	}
	if Clone(nil) != nil {
		t.Fatal("nil clone")
	}
}
