package commands

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

// TestHelpCheatsheetDistinguishesDetachFromExit pins AUD-14: idle Ctrl+C is not
// "quit" in a daemon session — it detaches and leaves the session running,
// while /exit ends it. The cheatsheet is shared with headless clients and
// cannot know the mode, so it must name both routes instead of claiming one.
func TestHelpCheatsheetDistinguishesDetachFromExit(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("help")
	res := cmd.Handler(nil, nil)
	if res.Doc == nil {
		t.Fatalf("/help should return a Doc, got %+v", res)
	}
	footer := res.Doc.Footer
	if strings.Contains(footer, "quit when idle") {
		t.Fatalf("cheatsheet still claims idle Ctrl+C unconditionally quits: %q", footer)
	}
	if !strings.Contains(footer, "/detach") {
		t.Errorf("cheatsheet does not name /detach: %q", footer)
	}
	if !strings.Contains(footer, "/exit") {
		t.Errorf("cheatsheet does not name /exit: %q", footer)
	}
}

// TestExitAndDetachDescriptionsNameTheDifference pins the second half: the two
// routes read as different outcomes in the command list, not two spellings of
// the same one.
func TestExitAndDetachDescriptionsNameTheDifference(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	exit, _ := cmdReg.Lookup("exit")
	detach, _ := cmdReg.Lookup("detach")
	if !strings.Contains(strings.ToLower(exit.Description), "end") {
		t.Errorf("/exit description should say it ends the session: %q", exit.Description)
	}
	if !strings.Contains(strings.ToLower(detach.Description), "running") {
		t.Errorf("/detach description should say the session keeps running: %q", detach.Description)
	}
}
