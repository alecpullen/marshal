package usability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/test/usability/harness"
	"marshal/test/usability/screen"
)

const (
	// skillFixtureName is the skill the journey acts on; skillSiblingName is a
	// second, untouched global skill. Removing one must leave the other
	// byte-for-byte intact, which a directory-level delete cannot fake.
	skillFixtureName = "fixture-skill"
	skillSiblingName = "keep-skill"

	// skillPanelTitle is the dock title of the skills panel and skillRootHeader
	// is the first header row of its root list. Both come from the panel's own
	// View, so they are what the user sees rather than internal identifiers.
	skillPanelTitle = "Skills"
	skillRootHeader = "Installed skills"

	// listCursorMarker is the marker the shared list engine draws on the row the
	// cursor is on. The focused row is the only one that carries it.
	listCursorMarker = "▸ "
)

// TestScriptedSkillRemoval drives one skill-management path at 80x24: the
// panel's Remove action must do nothing when cancelled and remove exactly the
// selected skill when confirmed.
//
// Both outcomes are asserted from the test-owned filesystem — the exact bytes
// recorded before Marshal started — and not from the panel's own status line,
// which only reports what the panel believes it did. No model call is needed,
// and any request to the test-owned provider fails the test.
func TestScriptedSkillRemoval(t *testing.T) {
	bin := requireBinary(t)
	env := isolatedHomeEnv(t)

	// The global skill store the panel itself reads: $MARSHAL_CONFIG_DIR/skills,
	// pinned inside the isolated home by the child environment.
	globalSkillsDir := filepath.Join(env.HomeDir, ".config", "marshal", "skills")
	fixtureBytes := writeTestSkill(t, globalSkillsDir, skillFixtureName, "the skill this journey removes")
	siblingBytes := writeTestSkill(t, globalSkillsDir, skillSiblingName, "the skill this journey must leave alone")

	sess, err := harness.New(harness.Config{
		BinaryPath: bin,
		Width:      80,
		Height:     24,
		WorkDir:    t.TempDir(),
		Env:        env.Env,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	defer sess.Close()

	waitForViewport(t, sess, 60*time.Second, "idle composer", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, "❯")
	})

	// Open the panel. Its footer counts the skill rows it discovered, which is
	// how this proves both test-owned skills were read from disk: at 80x24 the
	// list itself only shows a few rows before scrolling.
	if err := sess.Send("/skills"); err != nil {
		t.Fatalf("Send(/skills): %v", err)
	}
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter): %v", err)
	}
	waitForViewport(t, sess, 60*time.Second, "skills panel", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, skillPanelTitle) &&
			strings.Contains(scr.Content, skillRootHeader) &&
			strings.Contains(scr.Content, "2 skills")
	})

	// First pass: arm the removal and then leave the frame. The armed removal
	// must not carry out on its own, and must not survive the escape.
	openSkillDetail(t, sess, skillFixtureName)
	focusRow(t, sess, "Remove", "skill detail frame")
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter) arm removal: %v", err)
	}
	waitForViewport(t, sess, 30*time.Second, "armed removal", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, "confirm remove")
	})
	if err := sess.SendKey("esc"); err != nil {
		t.Fatalf("SendKey(esc) leave the frame: %v", err)
	}
	// Escape pops the detail frame back to the root list with the skill still
	// selected, which is what proves the armed removal was abandoned rather
	// than left pending.
	waitForViewport(t, sess, 30*time.Second, "skills list after escape", func(scr screen.Screen) bool {
		return strings.Contains(focusedRow(scr), skillFixtureName)
	})
	assertSkillBundle(t, globalSkillsDir, skillFixtureName, fixtureBytes, "after a cancelled removal")
	assertSkillBundle(t, globalSkillsDir, skillSiblingName, siblingBytes, "after a cancelled removal")

	// Second pass: the same row, confirmed. Only the selected skill may go.
	openSkillDetail(t, sess, skillFixtureName)
	focusRow(t, sess, "Remove", "skill detail frame")
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter) arm removal: %v", err)
	}
	waitForViewport(t, sess, 30*time.Second, "armed removal", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, "confirm remove")
	})
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter) confirm removal: %v", err)
	}

	// The panel pops back to the refreshed root list once the removal lands, and
	// its footer counts one skill fewer. The footer is always on screen, while
	// the list itself may be scrolled.
	waitForViewport(t, sess, 30*time.Second, "skills list after removal", func(scr screen.Screen) bool {
		return strings.Contains(scr.Content, "1 skill")
	})
	waitForGone(t, filepath.Join(globalSkillsDir, skillFixtureName), 15*time.Second)

	// The sibling is untouched, byte for byte.
	assertSkillBundle(t, globalSkillsDir, skillSiblingName, siblingBytes, "after removing the other skill")

	// Managing skills needs no model: any request to the test-owned provider
	// means the child reached for the network on its own.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests, want 0: skill management must not call a model", n)
	}
}

// openSkillDetail selects a skill's row on the root list and drills into its
// detail frame.
func openSkillDetail(t *testing.T, sess *harness.Session, name string) {
	t.Helper()
	focusRow(t, sess, name, "skills list")
	if err := sess.SendKey("enter"); err != nil {
		t.Fatalf("SendKey(enter) open %s: %v", name, err)
	}
	// The detail frame replaces the root list. Its first row is Name (holding
	// the skill's name), so the cursor row reading "Name" is what proves the
	// frame opened; at 80x24 the later rows sit below the fold and focusRow
	// scrolls to the one it needs.
	waitForViewport(t, sess, 30*time.Second, "skill detail frame", func(scr screen.Screen) bool {
		return strings.Contains(focusedRow(scr), "Name")
	})
}

// focusRow moves the list cursor onto label with the panel's own down key,
// reading the current viewport before and after every press so the test never
// acts on a stale screen or on a guessed number of entries.
func focusRow(t *testing.T, sess *harness.Session, label, where string) {
	t.Helper()
	const maxMoves = 12
	for moves := 0; moves <= maxMoves; moves++ {
		before := focusedRow(snapshotScreen(t, sess))
		if strings.Contains(before, label) {
			return
		}
		if err := sess.SendKey("down"); err != nil {
			t.Fatalf("%s: SendKey(down): %v", where, err)
		}
		if !waitUntil(t, sess, 5*time.Second, func(scr screen.Screen) bool {
			return focusedRow(scr) != before
		}) {
			t.Fatalf("%s: the cursor did not move while looking for %q (focused row: %q)", where, label, before)
		}
	}
	t.Fatalf("%s: no row labelled %q within %d cursor moves", where, label, maxMoves)
}

// focusedRow returns the row the list cursor is drawn on, or "" when the
// viewport shows none.
func focusedRow(scr screen.Screen) string {
	for _, line := range scr.Lines {
		if strings.Contains(line, listCursorMarker) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// waitUntil reports whether pred held within timeout, failing the test on a
// terminal model error instead of treating a broken screen as a false result.
func waitUntil(t *testing.T, sess *harness.Session, timeout time.Duration, pred func(screen.Screen) bool) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var modelErr error
	err := sess.WaitFor(ctx, func(snap harness.Snapshot) bool {
		scr, parseErr := screen.Parse(snap)
		if parseErr != nil {
			modelErr = parseErr
			return true
		}
		return pred(scr)
	})
	if modelErr != nil {
		t.Fatalf("terminal model error: %v", modelErr)
	}
	return err == nil
}

// writeTestSkill creates a global skill the panel will discover and returns the
// exact bytes written, so the assertions can hold the file to them later.
func writeTestSkill(t *testing.T, skillsDir, name, description string) []byte {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create skill dir %s: %v", dir, err)
	}
	// TOML frontmatter: name and description are required, risk defaults.
	bundle := []byte("+++\nname = \"" + name + "\"\ndescription = \"" + description + "\"\n+++\n\n" + description + ".\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), bundle, 0o644); err != nil {
		t.Fatalf("write %s/SKILL.md: %v", name, err)
	}
	return bundle
}

// assertSkillBundle holds a skill's SKILL.md to the bytes recorded before
// Marshal started.
func assertSkillBundle(t *testing.T, skillsDir, name string, want []byte, when string) {
	t.Helper()
	path := filepath.Join(skillsDir, name, "SKILL.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: read %s: %v", when, path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s: %s content = %q, want %q", when, path, got, want)
	}
}

// waitForGone polls until path no longer exists. The removal is performed by
// the panel, so the filesystem is the independent oracle; the bound keeps a
// removal that never lands from hanging the suite.
func waitForGone(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists after %s: the confirmed removal did not reach the filesystem", path, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
