//go:build unix

package fleetprofile

import (
	"fmt"
	"os"
	"testing"
)

func TestTheFileMustBePrivate(t *testing.T) {
	body := "[hosts.attic]\nssh = \"me@attic\"\n"

	t.Run("0644", func(t *testing.T) {
		path := writeFleetMode(t, body, 0o644)
		_, err := Load(path)
		if err == nil {
			t.Fatal("a file of mode 0644 loaded, want a refusal")
		}
		want := "fleet.toml at " + path + " is mode 0644: run chmod 600 " + path
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
	})

	for _, mode := range []os.FileMode{0o600, 0o400} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			if _, err := Load(writeFleetMode(t, body, mode)); err != nil {
				t.Fatalf("mode %04o was refused: %v", mode, err)
			}
		})
	}
}
