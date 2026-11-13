package tsgen

import (
	"bytes"
	"flag"
	"os"
	"strconv"
	"strings"
	"testing"
)

// update rewrites the checked-in generated module. It is test-only, and this
// package is its own test binary, so it does not collide with other packages'
// flags.
var update = flag.Bool("update", false, "rewrite generated files from the current source")

// regeneration is the command that makes a stale generated file current.
const regeneration = "go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -update"

// TestGeneratorRules generates the fixture package and checks every mapping
// rule, then checks that each unsupported construct is a named error.
func TestGeneratorRules(t *testing.T) {
	t.Run("mappings", func(t *testing.T) {
		// 1. Generate the fixture package, comments included.
		first, err := Generate("testdata/rules")
		if err != nil {
			t.Fatalf("Generate(testdata/rules): %v", err)
		}
		if *update {
			if err := os.WriteFile("testdata/rules.ts", first, 0o644); err != nil {
				t.Fatalf("write testdata/rules.ts: %v", err)
			}
		}

		// 2. Compare bytes exactly with the expected module.
		want, err := os.ReadFile("testdata/rules.ts")
		if err != nil {
			t.Fatalf("read testdata/rules.ts: %v", err)
		}
		if !bytes.Equal(first, want) {
			t.Fatalf("generated fixture differs from testdata/rules.ts\n--- got ---\n%s\n--- want ---\n%s", first, want)
		}

		// The module ends with exactly one newline.
		if !bytes.HasSuffix(first, []byte("\n")) || bytes.HasSuffix(first, []byte("\n\n")) {
			t.Errorf("generated module does not end with exactly one newline: %q", tail(first))
		}
		if !bytes.HasPrefix(first, []byte(header)) {
			t.Errorf("generated module does not start with the header:\n%s", first)
		}

		// 3. Two runs produce identical bytes.
		second, err := Generate("testdata/rules")
		if err != nil {
			t.Fatalf("second Generate(testdata/rules): %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("Generate is not deterministic")
		}
	})

	// 4. Each unsupported construct fails, naming the file, the construct and
	// the source line.
	unsupported := []struct {
		name      string
		fixture   string
		construct string
		line      int
	}{
		{"anonstruct", "anonstruct.go", "anonymous struct", 6},
		{"channel", "channel.go", "channel", 6},
		{"iface", "iface.go", "interface", 11},
		{"embedded", "embedded.go", "embedded field", 11},
		{"badmap", "badmap.go", "map keyed by a non-string", 6},
		{"generic", "generic.go", "generic type", 5},
		{"anytype", "anytype.go", "any", 6},
		{"namedchan", "namedchan.go", "channel", 9},
	}
	for _, tc := range unsupported {
		t.Run("unsupported/"+tc.name, func(t *testing.T) {
			_, err := Generate("testdata/rules/unsupported/" + tc.name)
			if err == nil {
				t.Fatalf("Generate accepted the unsupported construct in %s", tc.fixture)
			}
			msg := err.Error()
			for _, want := range []string{tc.fixture, tc.construct, strconv.Itoa(tc.line)} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not name %q", msg, want)
				}
			}
		})
	}
}

// TestTypeScriptContractIsCurrent holds the generated UI module to the
// contract package. Run with -update to rewrite it.
func TestTypeScriptContractIsCurrent(t *testing.T) {
	generated, err := Generate("..")
	if err != nil {
		t.Fatalf("Generate(..): %v", err)
	}

	const uiPath = "../../../ui/src/lib/contract.ts"
	if *update {
		if err := os.WriteFile(uiPath, generated, 0o644); err != nil {
			t.Fatalf("write %s: %v", uiPath, err)
		}
		return
	}

	current, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatalf("read %s: %v\nregenerate it with:\n\t%s", uiPath, err, regeneration)
	}
	if !bytes.Equal(generated, current) {
		t.Fatalf("%s is stale; regenerate it with:\n\t%s", uiPath, regeneration)
	}
}

// tail returns the last few bytes of b for a failure message.
func tail(b []byte) string {
	if len(b) > 20 {
		b = b[len(b)-20:]
	}
	return string(b)
}
