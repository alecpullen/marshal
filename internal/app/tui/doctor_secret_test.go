package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

// newDoctorSecretModel builds a model whose user config resolves inside a
// throwaway home, with one provider whose key is missing and no key material
// anywhere else. The caller gets the model and the config file path the repair
// writes to.
func newDoctorSecretModel(t *testing.T) (Model, string) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", filepath.Join(home, ".config", "marshal"))
	t.Setenv("XDG_CONFIG_HOME", "")
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{})
	state.Config.Providers = map[string]config.ProviderConfig{
		"openai": {
			Type:      "openai_compatible",
			BaseURL:   "https://api.openai.com/v1",
			APIKeyEnv: "MISSING_OPENAI_KEY",
		},
	}
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := testModel(state, WithCommandRegistry(reg), WithHomeDir(home),
		WithAuthResolver(newMemAuth()), WithOpenBrowser(func(string) error { return nil }))
	m.state.WorkingDir = work
	m.workDir = work
	m.resize(80, 24)
	m.refreshViewport()
	return m, config.UserConfigPath(home)
}

// openDoctorSecret drives the doctor panel's FixMsg through the real update
// path and returns the model with the masked field up.
func openDoctorSecret(t *testing.T, m Model) Model {
	t.Helper()
	updated, cmd := m.Update(doctorpanel.FixMsg{Provider: "openai"})
	m = drainCmds(asModel(t, updated), cmd)
	if m.doctorSecret == nil {
		t.Fatal("FixMsg did not open the masked key field")
	}
	if m.dock.IsOpen() {
		t.Fatal("FixMsg must close the doctor panel")
	}
	return m
}

// typeIntoDoctorSecret types s into the masked field one rune at a time,
// through the real Update path.
func typeIntoDoctorSecret(m Model, s string) Model {
	for _, r := range s {
		m = sendKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// savedUserConfig returns the user config bytes, failing the test without ever
// rendering their contents.
func savedUserConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved user config: %v", err)
	}
	return data
}

// assertSecretAbsent fails when the secret reaches any surface the user or the
// model can read it back from: the rendered frame, the transcript, the session
// history, or a condensed paste attachment. Failure messages carry only
// lengths and surface names — never the key.
func assertSecretAbsent(t *testing.T, m Model, secret, when string) {
	t.Helper()
	if secret == "" {
		t.Fatal("fixture: the secret under test is empty")
	}
	if view := stripANSI(m.viewString()); strings.Contains(view, secret) {
		t.Fatalf("%s: the rendered frame contains the %d-byte key", when, len(secret))
	}
	if body := stripANSI(m.viewport.View()); strings.Contains(body, secret) {
		t.Fatalf("%s: the transcript contains the %d-byte key", when, len(secret))
	}
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, secret) {
			t.Fatalf("%s: session history contains the %d-byte key", when, len(secret))
		}
	}
	for _, p := range m.pastes {
		if strings.Contains(p.Content, secret) || strings.Contains(p.chipText(), secret) {
			t.Fatalf("%s: a paste attachment contains the %d-byte key", when, len(secret))
		}
	}
}

// TestDoctorSecretMasksTypedAndPastedKey pins the whole secrecy contract of the
// /doctor repair field: typed and pasted key material lands in the field, the
// exact value is what reaches the config file, and no byte of it ever reaches
// the frame, the transcript, the history or a paste attachment.
func TestDoctorSecretMasksTypedAndPastedKey(t *testing.T) {
	t.Run("typed short key", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		m = openDoctorSecret(t, m)

		const key = "sk-typed-0123456789"
		m = typeIntoDoctorSecret(m, key)

		if got := m.doctorSecret.input.Value(); got != key {
			t.Fatalf("masked field holds %d bytes, want %d", len(got), len(key))
		}
		if m.input.Value() != "" {
			t.Fatalf("the chat composer received the key: %d bytes", len(m.input.Value()))
		}
		if len(m.pastes) != 0 {
			t.Fatalf("pastes = %d, want 0", len(m.pastes))
		}
		if masked := m.doctorSecret.input.View(); !strings.Contains(masked, "*") || strings.Contains(masked, key) {
			t.Fatal("the field does not render the key as a mask")
		}
		assertSecretAbsent(t, m, key, "while typing")

		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret != nil {
			t.Fatal("the field stayed open after a successful save")
		}
		assertSecretAbsent(t, m, key, "after saving")

		if data := savedUserConfig(t, cfgPath); !strings.Contains(string(data), key) {
			t.Fatalf("the saved config (%d bytes) does not hold the typed key", len(data))
		}
	})

	t.Run("pasted long key", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		m = openDoctorSecret(t, m)

		// Over the 1,500-byte condensing threshold: a large paste must reach the
		// masked field rather than become a paste attachment.
		key := "sk-" + strings.Repeat("k", 1600)
		if !shouldCondensePaste(key) {
			t.Fatal("fixture: the paste must exceed the condensing threshold")
		}
		updated, cmd := m.Update(tea.PasteMsg{Content: key})
		m = drainCmds(asModel(t, updated), cmd)

		if got := m.doctorSecret.input.Value(); got != key {
			t.Fatalf("masked field holds %d bytes, want %d", len(got), len(key))
		}
		if len(m.pastes) != 0 {
			t.Fatalf("a %d-byte paste was condensed into an attachment", len(key))
		}
		assertSecretAbsent(t, m, key, "while pasting")

		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret != nil {
			t.Fatal("the field stayed open after a successful save")
		}
		assertSecretAbsent(t, m, key, "after saving")

		if data := savedUserConfig(t, cfgPath); !strings.Contains(string(data), key) {
			t.Fatalf("the saved config (%d bytes) does not hold the %d-byte pasted key", len(data), len(key))
		}
	})
}

// TestDoctorSecretPreservesDraft verifies that a repair never costs the user
// their chat draft: saving, cancelling, a failed save and retry, a resize and
// Send now all leave the composer's text, cursor, placeholder, focus and paste
// attachments exactly as they were.
func TestDoctorSecretPreservesDraft(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		var draft composerDraft
		m, draft = seedComposerDraft(t, m)
		m = openDoctorSecret(t, m)
		assertDraftIntact(t, m, draft, "while the key field is open")

		m = typeIntoDoctorSecret(m, "sk-saved-draft")
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret != nil {
			t.Fatal("the field stayed open after saving")
		}
		assertDraftIntact(t, m, draft, "after saving")

		if data := savedUserConfig(t, cfgPath); !strings.Contains(string(data), "sk-saved-draft") {
			t.Fatalf("the saved config (%d bytes) does not hold the key", len(data))
		}

		// The cursor still sits at column 3 of "mid line": the next keystroke
		// lands there rather than at the end of the draft.
		m = sendKey(m, tea.KeyPressMsg{Code: 'X', Text: "X"})
		if got, want := m.input.Value(), strings.Replace(draft.value, "mid line", "midX line", 1); got != want {
			t.Fatal("a keystroke after saving did not land at the preserved cursor")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		var draft composerDraft
		m, draft = seedComposerDraft(t, m)
		m = openDoctorSecret(t, m)

		m = typeIntoDoctorSecret(m, "sk-abandoned")
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.doctorSecret != nil {
			t.Fatal("Esc did not close the key field")
		}
		assertDraftIntact(t, m, draft, "after cancelling")

		if _, err := os.Stat(cfgPath); err == nil {
			if data := savedUserConfig(t, cfgPath); strings.Contains(string(data), "sk-abandoned") {
				t.Fatal("cancelling the repair still wrote the key")
			}
		}

		// A keystroke returns to the preserved cursor, not the end of the draft.
		m = sendKey(m, tea.KeyPressMsg{Code: 'Y', Text: "Y"})
		if got, want := m.input.Value(), strings.Replace(draft.value, "mid line", "midY line", 1); got != want {
			t.Fatal("a keystroke after cancelling did not land at the preserved cursor")
		}
	})

	t.Run("failed save then retry", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		var draft composerDraft
		m, draft = seedComposerDraft(t, m)
		m = openDoctorSecret(t, m)
		m = typeIntoDoctorSecret(m, "sk-retried")

		// A config path under a regular file cannot be created: the save fails
		// before anything is written.
		blocked := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(blocked, []byte("not a dir\n"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		t.Setenv("MARSHAL_CONFIG_DIR", filepath.Join(blocked, "marshal"))

		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret == nil {
			t.Fatal("a failed save must keep the masked field open for retry")
		}
		if got := m.doctorSecret.input.Value(); got != "sk-retried" {
			t.Fatalf("the failed save lost the masked value: %d bytes", len(got))
		}
		assertDraftIntact(t, m, draft, "after a failed save")

		// No error message may echo key material, and the failure must actually
		// be reported: a silent save failure would be invisible to the user.
		reported := false
		for _, msg := range m.state.Messages() {
			if strings.Contains(msg.Content, "sk-retried") {
				t.Fatal("the failure message carries the key")
			}
			if msg.Role == session.RoleSystem && strings.Contains(msg.Content, "Failed to save API key") {
				reported = true
			}
		}
		if !reported {
			t.Fatal("the failed save produced no diagnostic")
		}

		// The path is writable again: the same field retries without a retype.
		if err := os.Remove(blocked); err != nil {
			t.Fatalf("clear blocker: %v", err)
		}
		t.Setenv("MARSHAL_CONFIG_DIR", filepath.Dir(cfgPath))

		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret != nil {
			t.Fatal("the retry with a writable path did not close the field")
		}
		assertDraftIntact(t, m, draft, "after a successful retry")

		if data := savedUserConfig(t, cfgPath); !strings.Contains(string(data), "sk-retried") {
			t.Fatalf("the retried config (%d bytes) does not hold the key", len(data))
		}
	})

	t.Run("resize", func(t *testing.T) {
		m, _ := newDoctorSecretModel(t)
		var draft composerDraft
		m, draft = seedComposerDraft(t, m)
		m = openDoctorSecret(t, m)
		m = typeIntoDoctorSecret(m, "sk-resize")

		m, _ = press(m, tea.WindowSizeMsg{Width: 120, Height: 40})
		if m.doctorSecret == nil {
			t.Fatal("a resize closed the masked field")
		}
		if got := m.doctorSecret.input.Value(); got != "sk-resize" {
			t.Fatalf("a resize lost the masked value: %d bytes", len(got))
		}
		if got, want := m.doctorSecret.input.Width(), max(m.leftWidth-4, 1); got != want {
			t.Fatalf("field width after resize = %d, want %d", got, want)
		}
		assertDraftIntact(t, m, draft, "after a resize")
		if strings.Contains(stripANSI(m.viewString()), "sk-resize") {
			t.Fatal("a resize exposed the key in the frame")
		}

		m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
		assertDraftIntact(t, m, draft, "after resizing back")
	})

	t.Run("send now", func(t *testing.T) {
		f := newSendNowFixture(t, 120, 40, true)
		var draft composerDraft
		f.m, draft = seedComposerDraft(t, f.m)
		f.m = openDoctorSecret(t, f.m)
		f.m = typeIntoDoctorSecret(f.m, "sk-not-steering")

		// Alt+Enter opens the separate Send now composer; it must never treat
		// the key in the field as text to steer the turn with.
		f.send(sendNowKey())
		if len(f.submits) != 0 {
			t.Fatalf("Send now submitted %d request(s) while the key field was up", len(f.submits))
		}
		if f.m.steeringComposer == nil {
			t.Fatal("Send now did not open its own composer over the key field")
		}
		if f.m.doctorSecret == nil {
			t.Fatal("opening Send now closed the key field")
		}
		if got := f.m.doctorSecret.input.Value(); got != "sk-not-steering" {
			t.Fatalf("opening Send now disturbed the masked value: %d bytes", len(got))
		}
		if got := f.m.input.Value(); got != draft.value {
			t.Fatalf("the draft lost %d bytes when Send now opened", len(draft.value)-len(got))
		}
	})
}

// TestDoctorSecretHiddenCannotSubmit pins that a key field the user cannot see
// never reads and never saves a byte: below the supported terminal minimum, and
// behind a full-frame panel, only Escape is honoured so the repair can always
// be abandoned.
func TestDoctorSecretHiddenCannotSubmit(t *testing.T) {
	t.Run("below the terminal minimum", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		m = openDoctorSecret(t, m)

		m, _ = press(m, tea.WindowSizeMsg{Width: 60, Height: 20})
		if m.doctorActionable() {
			t.Fatal("the field claims to be actionable below the terminal minimum")
		}

		m = typeIntoDoctorSecret(m, "sk-hidden-typed")
		updated, cmd := m.Update(tea.PasteMsg{Content: "sk-hidden-pasted-" + strings.Repeat("p", 1600)})
		m = drainCmds(asModel(t, updated), cmd)
		if m.doctorSecret == nil {
			t.Fatal("a hidden field must stay open")
		}
		if got := m.doctorSecret.input.Value(); got != "" {
			t.Fatalf("a hidden field read %d bytes of key material", len(got))
		}
		if len(m.pastes) != 0 {
			t.Fatalf("pastes = %d, want 0", len(m.pastes))
		}

		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret == nil {
			t.Fatal("a hidden field submitted its (empty) value")
		}
		if _, err := os.Stat(cfgPath); err == nil {
			if data := savedUserConfig(t, cfgPath); strings.Contains(string(data), "sk-hidden") {
				t.Fatal("a hidden field saved key material to the config")
			}
		}

		// Escape still cancels: the user can always abandon a repair.
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.doctorSecret != nil {
			t.Fatal("Esc did not cancel a hidden key field")
		}
	})

	t.Run("behind a full-frame panel", func(t *testing.T) {
		m, cfgPath := newDoctorSecretModel(t)
		m = openDoctorSecret(t, m)

		m.openSettingsBrowser("")
		if !m.dock.FullFrameOpen() {
			t.Fatal("fixture: /settings should own the whole frame")
		}
		if m.doctorActionable() {
			t.Fatal("a field behind a full-frame panel claims to be actionable")
		}

		m = typeIntoDoctorSecret(m, "sk-behind-panel")
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.doctorSecret == nil {
			t.Fatal("Enter behind a full-frame panel submitted the field")
		}
		if got := m.doctorSecret.input.Value(); got != "" {
			t.Fatalf("a field behind a panel read %d bytes of key material", len(got))
		}
		if _, err := os.Stat(cfgPath); err == nil {
			if data := savedUserConfig(t, cfgPath); strings.Contains(string(data), "sk-behind-panel") {
				t.Fatal("a field behind a panel saved key material to the config")
			}
		}

		// Back on screen, the field is usable again and cancellation is
		// available.
		m.dock.CloseNow()
		m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
		if !m.doctorActionable() {
			t.Fatal("the field should be actionable again once the panel is closed")
		}
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.doctorSecret != nil {
			t.Fatal("Esc did not cancel the key field")
		}
	})
}
