package connect

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestNewStartsAtPickTemplate(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	if m.step != stepPickTemplate {
		t.Fatalf("step = %v, want stepPickTemplate", m.step)
	}
	if m.title == "" {
		t.Fatal("title must be set for the pickTemplate step")
	}
}

func TestNewScopedProviderStartsAtPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.step != stepPickModel {
		t.Fatalf("step = %v, want stepPickModel", m.step)
	}
}

func TestEscAtPickTemplateEmitsCancelled(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 27})
	if cmd == nil {
		t.Fatal("expected a cmd emitting CancelledMsg")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("cmd produced %T, want CancelledMsg", msg)
	}
	_ = updated
}
