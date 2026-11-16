package dartgen

import (
	"bytes"
	"flag"
	"os"
	"strconv"
	"strings"
	"testing"
)

// update rewrites the checked-in generated files. It is test-only, and this
// package is its own test binary, so it does not collide with other
// packages' flags.
var update = flag.Bool("update", false, "rewrite generated files from the current source")

// regeneration is the command that makes a stale generated file current.
const regeneration = "go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update"

// fixture is the shared rules package: the same one tsgen generates from, so
// both modules are held to one fixture.
const fixture = "../tsgen/testdata/rules"

// TestGeneratorRules generates the fixture package and checks every mapping
// rule, then checks that each unsupported construct is a named error.
func TestGeneratorRules(t *testing.T) {
	t.Run("mappings", func(t *testing.T) {
		// 1. Generate the fixture package, comments included.
		first, err := Generate(fixture)
		if err != nil {
			t.Fatalf("Generate(%s): %v", fixture, err)
		}
		if *update {
			if err := os.WriteFile("testdata/rules.dart", first, 0o644); err != nil {
				t.Fatalf("write testdata/rules.dart: %v", err)
			}
		}

		// 2. Compare bytes exactly with the expected module.
		want, err := os.ReadFile("testdata/rules.dart")
		if err != nil {
			t.Fatalf("read testdata/rules.dart: %v", err)
		}
		if !bytes.Equal(first, want) {
			t.Fatalf("generated fixture differs from testdata/rules.dart\n--- got ---\n%s\n--- want ---\n%s", first, want)
		}

		// The module ends with exactly one newline and starts with the
		// header.
		if !bytes.HasSuffix(first, []byte("\n")) || bytes.HasSuffix(first, []byte("\n\n")) {
			t.Errorf("generated module does not end with exactly one newline: %q", tail(first))
		}
		if !bytes.HasPrefix(first, []byte(header)) {
			t.Errorf("generated module does not start with the header:\n%s", first)
		}

		// 3. Two runs produce identical bytes.
		second, err := Generate(fixture)
		if err != nil {
			t.Fatalf("second Generate(%s): %v", fixture, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("Generate is not deterministic")
		}
	})

	// 4. Each unsupported construct fails, naming the file, the construct
	// and the source line. The fixtures are tsgen's, so both generators
	// reject the same package for the same reason.
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
			_, err := Generate(fixture + "/unsupported/" + tc.name)
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

// TestDartContractIsCurrent holds the generated app module to the contract
// package. Run with -update to rewrite it.
func TestDartContractIsCurrent(t *testing.T) {
	generated, err := Generate("..")
	if err != nil {
		t.Fatalf("Generate(..): %v", err)
	}

	const appPath = "../../../../app/lib/src/client/contract.g.dart"
	if *update {
		if err := os.WriteFile(appPath, generated, 0o644); err != nil {
			t.Fatalf("write %s: %v", appPath, err)
		}
		return
	}

	current, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatalf("read %s: %v\nregenerate it with:\n\t%s", appPath, err, regeneration)
	}
	if !bytes.Equal(generated, current) {
		t.Fatalf("%s is stale; regenerate it with:\n\t%s", appPath, regeneration)
	}
}

// tail returns the last few bytes of b for a failure message.
func tail(b []byte) string {
	if len(b) > 20 {
		b = b[len(b)-20:]
	}
	return string(b)
}
