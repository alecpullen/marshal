package settings

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

// fakeEntry is a minimal collectionEntry for testing the generic pane.
type fakeEntry struct{ key string }

func (f fakeEntry) Title() string { return f.key }
func (f fakeEntry) Key() string   { return f.key }

var (
	errEmpty     = fmt.Errorf("name cannot be empty")
	errDuplicate = fmt.Errorf("entry already exists")
)

func fakeSpec(s *state) collectionSpec {
	return collectionSpec{
		heading:   "Fake",
		keyPrompt: "New entry name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.Providers))
			for k := range s.cfg.Providers {
				out = append(out, fakeEntry{key: k})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return errEmpty
			}
			if _, ok := s.cfg.Providers[key]; ok {
				return errDuplicate
			}
			s.cfg.Providers[key] = config.ProviderConfig{}
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			entry := s.cfg.Providers[key]
			form := newSectionForm(
				huh.NewInput().Key("Base URL").Title("Base URL").Value(&entry.BaseURL),
			)
			return form, func() { s.cfg.Providers[key] = entry }
		},
		delete: func(s *state, key string) { delete(s.cfg.Providers, key) },
	}
}

func TestCollectionPaneAddEntry(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // clamp
	p.Update(tea.KeyPressMsg{Code: rune('a'), Text: "a"})
	if !p.adding {
		t.Fatal("a should open the name prompt")
	}
	p.Update(tea.KeyPressMsg{Code: rune('f'), Text: "f"})
	p.Update(tea.KeyPressMsg{Code: rune('s'), Text: "s"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := st.cfg.Providers["fs"]; !ok {
		t.Fatal("enter on the name prompt should add the entry")
	}
	if p.adding {
		t.Fatal("prompt should close after a successful add")
	}
}

func TestCollectionPaneDuplicateRejected(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"fs": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: rune('a'), Text: "a"})
	p.Update(tea.KeyPressMsg{Code: rune('f'), Text: "f"})
	p.Update(tea.KeyPressMsg{Code: rune('s'), Text: "s"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.adding {
		t.Fatal("duplicate add should keep the prompt open")
	}
	if !strings.Contains(stripANSI(p.View(60)), "already exists") {
		t.Error("duplicate error should render")
	}
}

func TestCollectionPaneDeleteEntry(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}, "b": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: rune('d'), Text: "d"})
	if _, ok := st.cfg.Providers["a"]; ok {
		t.Fatal("d should delete the focused entry")
	}
}

func TestCollectionPaneEscClosesFormNotOverlay(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit form
	if p.form == nil {
		t.Fatal("enter should open the edit form")
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if p.form != nil {
		t.Fatal("esc should close the sub-form")
	}
	if p.HasInnerFocus() {
		t.Fatal("after closing the form the pane should not report inner focus")
	}
}

func TestCollectionPaneSubFormCommit(t *testing.T) {
	// Slow huh-driven test (~4s) because the sub-form commit path
	// walks the huh form state machine end-to-end.
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}

	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{"a": {}}
	p := newCollectionPane(st, fakeSpec(st))
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.Update(tea.KeyPressMsg{Code: rune('x'), Text: "x"})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // submit
	if p.form != nil {
		t.Fatal("submit should close the form")
	}
	if got := st.cfg.Providers["a"].BaseURL; got != "x" {
		t.Fatalf("submit should commit to the working copy, got %q", got)
	}
}
