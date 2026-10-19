package routing

import (
	"testing"

	"marshal/internal/interaction"

	"github.com/pelletier/go-toml/v2"
)

// TestCustomAgentInteractionPresenceRoundTrip pins the presence semantics of
// the optional interaction fields: an absent mode/auto stays nil (inherit),
// an explicit false stays a non-nil false, and the existing Context and
// Temperature fields survive a TOML round trip unchanged.
func TestCustomAgentInteractionPresenceRoundTrip(t *testing.T) {
	t.Run("absent fields stay nil", func(t *testing.T) {
		var a CustomAgent
		if err := toml.Unmarshal([]byte("name = \"x\"\npreset = \"p\"\n"), &a); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if a.Mode != nil {
			t.Errorf("Mode = %v, want nil (inherit)", *a.Mode)
		}
		if a.Auto != nil {
			t.Errorf("Auto = %v, want nil (inherit)", *a.Auto)
		}
	})

	t.Run("explicit false is preserved", func(t *testing.T) {
		var a CustomAgent
		if err := toml.Unmarshal([]byte("name = \"x\"\npreset = \"p\"\nmode = \"ask\"\nauto = false\n"), &a); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if a.Mode == nil || *a.Mode != interaction.ModeAsk {
			t.Fatalf("Mode = %v, want ask", a.Mode)
		}
		if a.Auto == nil {
			t.Fatal("Auto = nil, want explicit false")
		}
		if *a.Auto {
			t.Error("Auto = true, want false")
		}
	})

	t.Run("context and temperature survive round trip", func(t *testing.T) {
		temp := 0.7
		in := CustomAgent{
			Name:        "x",
			Preset:      "p",
			Mode:        modePtr(interaction.ModeAct),
			Auto:        boolPtr(false),
			Context:     ContextBudget{MaxRepoContextTokens: 1234},
			Temperature: &temp,
		}
		raw, err := toml.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out CustomAgent
		if err := toml.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Mode == nil || *out.Mode != interaction.ModeAct {
			t.Errorf("Mode = %v, want act", out.Mode)
		}
		if out.Auto == nil || *out.Auto {
			t.Errorf("Auto = %v, want explicit false", out.Auto)
		}
		if out.Context.MaxRepoContextTokens != 1234 {
			t.Errorf("Context.MaxRepoContextTokens = %d, want 1234", out.Context.MaxRepoContextTokens)
		}
		if out.Temperature == nil || *out.Temperature != 0.7 {
			t.Errorf("Temperature = %v, want 0.7", out.Temperature)
		}
	})
}

func modePtr(m interaction.Mode) *interaction.Mode { return &m }

func boolPtr(b bool) *bool { return &b }
