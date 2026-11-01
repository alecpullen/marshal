package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/viewmodel"
)

// nodeCopyText is what `y` puts on the clipboard for a node, ANSI-free. Node
// fields come from the wire index; the text the wire caps or leaves out comes
// from the session.
func (m *Model) nodeCopyText(id viewmodel.NodeID) string {
	w := m.wireNode(id)
	if w == nil {
		return ""
	}
	switch {
	case w.Kind == acpwire.KindTool && w.Tool != nil && len(w.Tool.Calls) > 0:
		res, ok := m.nodeDetail(id)
		if !ok {
			return ""
		}
		var parts []string
		for _, c := range res.Detail.Calls {
			parts = append(parts, callCopyText(c))
		}
		return strings.Join(parts, "\n\n")
	case w.Kind == acpwire.KindStep && w.Step != nil:
		res, ok := m.nodeDetail(id)
		if !ok {
			return ""
		}
		// The narration already opens with the headline. A headless beat has
		// no headline to stand in for it, so it copies its rows only.
		var lines []string
		for _, s := range res.Detail.Narration {
			lines = append(lines, strings.TrimSpace(s))
		}
		for _, key := range w.Children {
			row := m.wire[key]
			if row == nil || row.Tool == nil {
				continue
			}
			for _, c := range row.Tool.Calls {
				lines = append(lines, "- "+row.Tool.Name+" "+c.Target)
			}
		}
		return strings.Join(lines, "\n")
	case w.Completion != nil:
		res, ok := m.nodeDetail(id)
		if !ok {
			return ""
		}
		return completionCopyText(w.Completion, res.Detail.Completion)
	case w.Message != nil:
		return w.Message.Content
	case w.RunEvent != nil, w.Thinking != nil && w.ID != acpwire.LiveThinkingID:
		// A run event or a finished thinking row copies its rendered text; a
		// truncated run event reads its whole body from the session.
		var d *acpwire.NodeDetail
		if w.RunEvent != nil && w.RunEvent.Truncated {
			if res, ok := m.nodeDetail(id); ok {
				d = &res.Detail
			}
		}
		return plainText(renderItem(w, d, true, 120))
	case w.Receipt != nil:
		return plainText(renderReceipt(w.Receipt, 200))
	}
	return ""
}

// completionCopyText is a finished job or subagent: its plain head line, then
// the output a job retained or the report a subagent wrote. A nil d gives no
// body.
func completionCopyText(c *acpwire.WireCompletion, d *acpwire.CompletionDetail) string {
	head := []string{c.ID, c.Label, c.Outcome}
	if c.Measure != "" {
		head = append(head, c.Measure)
	}
	if c.DurationMs > 0 {
		head = append(head, formatElapsed(time.Duration(c.DurationMs)*time.Millisecond))
	}
	var body string
	if d != nil {
		body = d.Output
		if c.Source == acpwire.CompletionSubagent {
			body = d.Report
		}
	}
	if body == "" {
		return strings.Join(head, dimSeparator)
	}
	return strings.Join(head, dimSeparator) + "\n\n" + body
}

// callCopyText is what copy takes from one call: its diff, else its output,
// else its one-line summary.
func callCopyText(c acpwire.CallDetail) string {
	switch {
	case c.Diff != "":
		return c.Diff
	case c.Output != "":
		return c.Output
	}
	return c.Summary
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
	return m.copyText(m.nodeCopyText(m.cursor))
}

// copyText puts text on the clipboard and says how much. Browse mode and the
// inspector both copy through it, so both give the same notices.
//
// A local clipboard helper is preferred because it confirms the write and works
// in terminals without OSC 52 (Terminal.app). It is skipped over SSH, where it
// would write the remote machine's clipboard; then, or when it fails, the copy
// goes to the terminal over OSC 52.
func (m *Model) copyText(raw string) tea.Cmd {
	text := plainText(raw)
	if text == "" {
		return m.setFlash("Nothing to copy")
	}
	if m.localClipboardUsable() {
		w := m.copyWriter
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
			defer cancel()
			return copyDoneMsg{text: text, err: w.Write(ctx, text)}
		}
	}
	return m.copyOverOSC52(text, nil)
}

// copyTimeout bounds a local clipboard write so a hung helper cannot hold the
// command open.
const copyTimeout = 3 * time.Second

// copyDoneMsg reports a finished local clipboard write.
type copyDoneMsg struct {
	text string
	err  error
}

// handleCopyDone reports a local write, or falls back to OSC 52 when it failed.
func (m *Model) handleCopyDone(msg copyDoneMsg) tea.Cmd {
	if msg.err != nil {
		return m.copyOverOSC52(msg.text, msg.err)
	}
	return m.setFlash("Copied " + copyLines(msg.text))
}

// copyOverOSC52 asks the terminal to copy text. localErr is why the local
// helper was not used, when it was tried and failed.
func (m *Model) copyOverOSC52(text string, localErr error) tea.Cmd {
	msg := "Copied " + copyLines(text)
	switch {
	case localErr != nil:
		msg = fmt.Sprintf("Copy sent over OSC 52 (local clipboard failed: %v)", localErr)
	case osc52Unsupported() && !m.osc52Noted:
		m.osc52Noted = true
		msg = "Copy sent (OSC 52); your terminal may not support it"
	}
	return tea.Batch(tea.SetClipboard(text), m.setFlash(msg))
}

// localClipboardUsable reports whether copy should use the local helper: one is
// wired, it resolves, and the session is not remote.
func (m *Model) localClipboardUsable() bool {
	if m.copyWriter == nil || (m.copyRemote != nil && m.copyRemote()) {
		return false
	}
	if av, ok := m.copyWriter.(interface{ Available() bool }); ok && !av.Available() {
		return false
	}
	return true
}

func copyLines(text string) string {
	return pluralCount(strings.Count(text, "\n")+1, "line", "lines")
}

var pathLineRE = regexp.MustCompile(`([A-Za-z0-9_./\-]+\.[A-Za-z0-9]+):(\d+)`)

// openCall is what the open action reads of one call.
type openCall struct {
	tool   string
	args   string   // the whole arguments, as JSON
	files  []string // the files it changed
	output string   // its whole output; "" when it printed nothing
}

// fileIn resolves the file calls are about, under root: a path argument, else
// a changed file, else for a shell call the first path:line in its output.
func fileIn(root string, calls []openCall) (path string, line int) {
	for _, c := range calls {
		var args map[string]any
		if json.Unmarshal([]byte(c.args), &args) == nil {
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
		for _, f := range c.files {
			if abs := absIn(root, f); openable(root, abs) {
				return abs, 0
			}
		}
	}
	for _, c := range calls {
		if !viewmodel.IsShellFamily(c.tool) {
			continue
		}
		for _, mt := range pathLineRE.FindAllStringSubmatch(c.output, -1) {
			if p := absIn(root, mt[1]); openable(root, p) {
				l, _ := strconv.Atoi(mt[2])
				return p, l
			}
		}
	}
	return "", 0
}

// nodeFile resolves the file a node is about: the call's path argument, else
// the first file it changed, else for a shell row the first path:line in its
// output that exists inside the workspace.
func (m *Model) nodeFile(id viewmodel.NodeID) (path string, line int) {
	w := m.wireNode(id)
	if w == nil {
		return "", 0
	}
	root := m.sess.Status().Workspace.ActiveRoot
	var calls []openCall
	gather := func(key string) {
		row := m.wire[key]
		if row == nil || row.Tool == nil {
			return
		}
		// A failed lookup is a stale index entry, not an empty row: the
		// wire still says which files the call changed, so fall through
		// with the changed files and no args or output rather than
		// claiming there is nothing to open.
		res, _ := m.nodeDetail(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: key})
		for i, call := range row.Tool.Calls {
			var cd acpwire.CallDetail
			if i < len(res.Detail.Calls) {
				cd = res.Detail.Calls[i]
			}
			// The wire's output is empty exactly when the call printed
			// nothing, and the detail's then holds the summary.
			output := ""
			if call.Output != "" {
				output = cd.Output
			}
			calls = append(calls, openCall{tool: row.Tool.Name, args: cd.Args, files: call.Files, output: output})
		}
	}
	switch {
	case w.Kind == acpwire.KindTool && w.Tool != nil && len(w.Tool.Calls) > 0:
		gather(w.ID)
	case w.Kind == acpwire.KindStep && w.Step != nil:
		for _, key := range w.Children {
			gather(key)
		}
	}
	return fileIn(root, calls)
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
	return m.openFile(m.nodeFile(m.cursor))
}

func (m *Model) openFile(path string, line int) tea.Cmd {
	if !m.host.SharedFilesystem() {
		return m.setFlash("$EDITOR is unavailable: the files are on the agent's machine")
	}
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
