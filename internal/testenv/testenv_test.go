package testenv

import (
	"os"
	"testing"
)

func TestSanitizeXDGBlanksOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/somewhere")
	t.Setenv("MARSHAL_DATA_DIR", "/tmp/elsewhere")

	SanitizeXDG()

	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR"} {
		if got := os.Getenv(key); got != "" {
			t.Fatalf("%s = %q after SanitizeXDG, want empty", key, got)
		}
	}
}
