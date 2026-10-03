package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/stack"
	"marshal/internal/tools/registry"
)

// nodeCopyText is what `y` puts on the clipboard for a node, ANSI-free.
func (m *Model) nodeCopyText(n *stack.Node) string {
	switch {
	case n == nil:
		return ""
	case n.Kind == stack.KindTool && len(n.Tools) > 0:
		var parts []string
		for _, ev := range n.Tools {
			parts = append(parts, toolCopyText(ev))
		}
		return strings.Join(parts, "\n\n")
	case n.Kind == stack.KindStep && n.Step != nil:
		// The narration already opens with the headline; the headline alone
		// stands in only for a step that did not narrate.
		var lines []string
		for _, msg := range n.Step.Narration {
			lines = append(lines, strings.TrimSpace(msg.Content))
		}
		if len(lines) == 0 {
			head, _, _ := stepHeadline(n.Step, n.Children)
			lines = append(lines, head)
		}
		for _, row := range n.Children {
			for _, ev := range row.Tools {
				lines = append(lines, "- "+ev.ToolName+" "+toolTarget(ev))
			}
		}
		return strings.Join(lines, "\n")
	case n.Kind == stack.KindTask && n.Task != nil:
		lines := []string{taskTitle(n.Task)}
		for _, st := range n.Children {
			if st.Step == nil {
				continue
			}
			head, _, _ := stepHeadline(st.Step, st.Children)
			lines = append(lines, "- "+head)
		}
		return strings.Join(lines, "\n")
	case n.Kind == stack.KindReceipt && n.Receipt != nil:
		return plainText(renderReceipt(n.Receipt, 200))
	case n.Item != nil && n.Item.Message != nil:
		return n.Item.Message.Content
	case n.Item != nil && n.Item.Subagent != nil:
		return n.Item.Subagent.Summary
	case n.Item != nil:
		return plainText(renderTranscriptItem(*n.Item, true, "", regionView{}, nil, 120))
	}
	return ""
}

// toolCopyText is the result of a call: the diff for an edit, the output for
// anything that has one, else the one-line summary.
func toolCopyText(ev registry.AuditEvent) string {
	if ev.ResultContent != "" {
		return ev.ResultContent
	}
	return ev.ResultSummary
}

// osc52Unsupported reports terminals known not to honour OSC 52.
func osc52Unsupported() bool {
	switch os.Getenv("TERM") {
	case "dumb", "linux":
		return true
	}
	return false
}

// copyCursorNode copies the cursor node over OSC 52 and says how much.
func (m *Model) copyCursorNode() tea.Cmd {
	return m.copyText(m.nodeCopyText(m.currentNode()))
}

// copyText puts text on the clipboard over OSC 52 and says how much. Browse
// mode and the inspector both copy through it, so both give the same notices,
// including the one for terminals known not to honour OSC 52.
func (m *Model) copyText(raw string) tea.Cmd {
	text := plainText(raw)
	if text == "" {
		return m.setFlash("Nothing to copy")
	}
	msg := fmt.Sprintf("Copied %d lines", strings.Count(text, "\n")+1)
	if osc52Unsupported() && !m.osc52Noted {
		m.osc52Noted = true
		msg = "Copy sent (OSC 52); your terminal may not support it"
	}
	return tea.Batch(tea.SetClipboard(text), m.setFlash(msg))
}

var pathLineRE = regexp.MustCompile(`([A-Za-z0-9_./\-]+\.[A-Za-z0-9]+):(\d+)`)

// nodeFile resolves the file a node is about: the call's path argument, else
// the first file it changed, else for a shell row the first path:line in its
// output that exists inside the workspace.
func (m *Model) nodeFile(n *stack.Node) (path string, line int) {
	if n == nil {
		return "", 0
	}
	root := m.state.Workspace().ActiveRoot
	var events []registry.AuditEvent
	switch {
	case len(n.Tools) > 0:
		events = n.Tools
	case n.Kind == stack.KindStep:
		for _, row := range n.Children {
			events = append(events, row.Tools...)
		}
	}
	for _, ev := range events {
		var args map[string]any
		if json.Unmarshal(ev.Args, &args) == nil {
			if p, ok := args["path"].(string); ok && p != "" {
				l := 0
				if f, ok := args["line"].(float64); ok {
					l = int(f)
				}
				if abs := absIn(root, p); openable(root, abs) {
					return abs, l
				}
			}
		}
		for _, f := range ev.FilesChanged {
			if abs := absIn(root, f); openable(root, abs) {
				return abs, 0
			}
		}
	}
	for _, ev := range events {
		if !stack.IsShellFamily(ev.ToolName) {
			continue
		}
		for _, mt := range pathLineRE.FindAllStringSubmatch(ev.ResultContent, -1) {
			if p := absIn(root, mt[1]); openable(root, p) {
				l, _ := strconv.Atoi(mt[2])
				return p, l
			}
		}
	}
	return "", 0
}

// openable is true for a regular file inside the workspace: a directory (a
// search over ".") or a path outside the repo is not something `o` should
// hand to an editor.
func openable(root, p string) bool {
	if !within(root, p) {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func absIn(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// editorCommand builds "$EDITOR file", adding the line for editors that take
// one: +N for most, -g file:N for code.
func editorCommand(editor, path string, line int) *exec.Cmd {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return nil
	}
	bin, args := fields[0], fields[1:]
	if line > 0 {
		switch filepath.Base(bin) {
		case "vi", "vim", "nvim", "nano", "hx", "kak", "emacs", "micro":
			args = append(args, "+"+strconv.Itoa(line), path)
			return exec.Command(bin, args...)
		case "code":
			args = append(args, "-g", fmt.Sprintf("%s:%d", path, line))
			return exec.Command(bin, args...)
		}
	}
	return exec.Command(bin, append(args, path)...)
}

type editorDoneMsg struct{ err error }

// openCursorFile opens the node's file in $EDITOR.
func (m *Model) openCursorFile() tea.Cmd {
	return m.openFile(m.nodeFile(m.currentNode()))
}

func (m *Model) openFile(path string, line int) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return m.setFlash("$EDITOR is not set")
	}
	if path == "" {
		return m.setFlash("No file to open here")
	}
	cmd := editorCommand(editor, path, line)
	if cmd == nil {
		return m.setFlash("$EDITOR is not set")
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{err: err} })
}
