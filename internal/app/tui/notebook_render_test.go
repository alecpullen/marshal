package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

func TestNotebookNarrationRendererKeepsSourceMappingsAtTerminalWidths(t *testing.T) {
	block := conversation.Block{
		ID: "narration:current", Kind: conversation.BlockNarration,
		Members: []string{"narration:current", "audit:one", "audit:two"},
		Text:    "Checking `the implementation` before changing it.\n\nThis is the full public narration.",
		Children: []conversation.Block{
			{ID: "narration:current", Kind: conversation.BlockMessage, Members: []string{"narration:current"}, Text: "Checking `the implementation` before changing it.\n\nThis is the full public narration."},
			{ID: "audit:one", Kind: conversation.BlockTool, Members: []string{"audit:one"}, Text: "Read a **long path** /workspace/source/internal/app/tui/notebook_render.go and found a useful result."},
			{ID: "audit:two", Kind: conversation.BlockTool, Members: []string{"audit:two"}, Text: "Second result with wide characters: 界面 and a\ttab."},
		},
	}
	for _, tc := range []struct {
		name       string
		width      int
		monochrome bool
	}{
		{"narrow", 80, false}, {"wide", 120, false}, {"extra-wide", 200, false}, {"monochrome", 80, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.monochrome {
				previous := theme.Current()
				theme.Reload(theme.LoadFor(true, ""))
				t.Cleanup(func() { theme.Reload(previous) })
			}
			parts := renderNotebookNarration(block, tc.width, true, false, false, nil)
			contentParts := make([]notebookRenderPart, 0, 3)
			var hasOrderControl bool
			for _, part := range parts {
				if part.OrderControl {
					hasOrderControl = strings.Contains(ansi.Strip(part.Text), "Work order: recorded")
					continue
				}
				contentParts = append(contentParts, part)
			}
			if len(contentParts) != 3 || !hasOrderControl {
				t.Fatalf("rendered parts = %+v, want narration, order control, and two work children", parts)
			}
			if contentParts[0].ID != block.ID || contentParts[1].ID != block.Children[1].ID || contentParts[2].ID != block.Children[2].ID {
				t.Fatalf("source identity changed: %+v", contentParts)
			}
			if block.Text != "Checking `the implementation` before changing it.\n\nThis is the full public narration." || block.Children[1].Text != "Read a **long path** /workspace/source/internal/app/tui/notebook_render.go and found a useful result." {
				t.Fatalf("renderer changed a source copy payload: %+v", block)
			}
			for _, part := range contentParts {
				for _, line := range strings.Split(strings.TrimSuffix(part.Text, "\n"), "\n") {
					if w := ansi.StringWidth(line); w > tc.width {
						t.Fatalf("line width %d exceeds %d: %q", w, tc.width, ansi.Strip(line))
					}
				}
			}
			if !strings.Contains(ansi.Strip(contentParts[0].Text), "Checking") || !strings.Contains(ansi.Strip(contentParts[0].Text), "Work") {
				t.Fatalf("narration disclosure or work label missing: %q", ansi.Strip(contentParts[0].Text))
			}
		})
	}
}

func TestNotebookWorkOrderToggleIsReversibleAndPresentationOnly(t *testing.T) {
	block := conversation.Block{
		ID: "narration:n", Kind: conversation.BlockNarration,
		Members: []string{"narration:n", "audit:a", "audit:b"},
		Children: []conversation.Block{
			{ID: "narration:n", Kind: conversation.BlockMessage, Members: []string{"narration:n"}, Text: "public plan"},
			{ID: "audit:a", Kind: conversation.BlockTool, Members: []string{"audit:a"}, Text: "first", CopyTargets: []conversation.CopyTarget{{Source: conversation.SourceOutput, Text: "first", Label: "Copy output"}}},
			{ID: "audit:b", Kind: conversation.BlockTool, Members: []string{"audit:b"}, Text: "second", CopyTargets: []conversation.CopyTarget{{Source: conversation.SourceOutput, Text: "second", Label: "Copy output"}}},
		},
	}
	reversed := reverseNotebookWorkChildren(block, true)
	if reversed.Children[0].ID != block.ID || reversed.Children[1].ID != "audit:b" || reversed.Children[2].ID != "audit:a" {
		t.Fatalf("reverse order = %+v", reversed.Children)
	}
	restored := reverseNotebookWorkChildren(reversed, true)
	if restored.Children[1].ID != "audit:a" || restored.Children[2].ID != "audit:b" {
		t.Fatalf("second toggle did not restore order: %+v", restored.Children)
	}
	if block.Children[1].ID != "audit:a" || block.Children[1].CopyTargets[0].Text != "first" || block.Children[2].CopyTargets[0].Text != "second" {
		t.Fatal("presentation order mutated source identities or copy payloads")
	}
}

func TestNotebookNewNarrationStaysBelowReadingAnchorAndShowsCount(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "Inspect the whole project.", session.ContentTypePlain)
	user := state.Transcript()[0]
	state.BeginActivityRun(user.Message.ID)
	first := state.BeginActivityResponse()
	first = state.BindActivityNarration(first, strings.Repeat("Reviewing the existing files and preserving the reader position.\n", 45))
	state.AddNarrationMessage(first, strings.Repeat("Reviewing the existing files and preserving the reader position.\n", 45))
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatal(err)
	}
	m := New(state, WithCommandRegistry(reg), WithHomeDir(t.TempDir()))
	m.resize(80, 24)
	m.notebookView = true
	m.refreshViewport()
	firstSource := state.Transcript()[1]
	firstID := conversation.BlockID(firstSource.ViewID)
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatal("fixture does not overflow the viewport")
	}
	m.viewportFollow = false
	row, ok := m.blockStartRow(firstID)
	if !ok {
		t.Fatal("first narration has no rendered block span")
	}
	m.viewport.SetYOffset(row + 5)
	m.captureReadingAnchor()
	before := m.readingAnchor
	if before.Block != firstID {
		t.Fatalf("anchor block = %q, want %q", before.Block, firstID)
	}
	second := state.BeginActivityResponse()
	second = state.BindActivityNarration(second, "Checking one additional result.")
	state.AddNarrationMessage(second, "Checking one additional result.")
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if m.readingAnchor.Block != before.Block || m.readingAnchor.Offset != before.Offset {
		t.Fatalf("new narration moved the reading anchor: before=%+v after=%+v", before, m.readingAnchor)
	}
	latest := state.Transcript()[len(state.Transcript())-1]
	latestRow, ok := m.blockStartRow(conversation.BlockID(latest.ViewID))
	firstRow, _ := m.blockStartRow(firstID)
	if !ok || latestRow <= firstRow {
		t.Fatalf("new narration row %d should remain below first narration row %d", latestRow, firstRow)
	}
	if m.notebookNewActivity != 1 || !strings.Contains(ansi.Strip(m.renderTranscriptFrame()), "1 new activity") {
		t.Fatalf("new activity indication count = %d; transcript hint = %q", m.notebookNewActivity, ansi.Strip(m.renderTranscriptFrame()))
	}
	m.viewportFollow = true // explicit jump to latest resumes current-first follow.
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if m.notebookNewActivity != 0 {
		t.Fatalf("new activity indication survived jump to latest: %d", m.notebookNewActivity)
	}
}

func TestRefreshViewportDispatchesNotebookProjection(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "Please inspect this carefully.", session.ContentTypePlain)
	transcript := state.Transcript()
	boundary := transcript[len(transcript)-1].Message.ID
	state.BeginActivityRun(boundary)
	response := state.BeginActivityResponse()
	response = state.BindActivityNarration(response, "Checking the important path before editing.")
	state.AddNarrationMessage(response, "Checking the important path before editing.")
	state.LogToolCall(registry.AuditEvent{ToolName: "file.read", ResultContent: "Found the exact source content.", Activity: response})
	state.LogToolCall(registry.AuditEvent{ToolName: "file.read", ResultContent: "Found the next related source.", Activity: response})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatal(err)
	}
	m := New(state, WithCommandRegistry(reg), WithHomeDir(t.TempDir()))
	m.resize(80, 24)
	m.notebookView = true
	m.refreshViewport()
	plain := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(plain, "Checking the important path") || !strings.Contains(plain, "Please inspect this carefully") || !strings.Contains(plain, "Read") || !strings.Contains(plain, "Found the exact source content") {
		t.Fatalf("notebook render omitted request, narration, or owned tool output: %q", plain)
	}
	if !m.conversationDocument().Has(conversation.BlockID(transcript[len(transcript)-1].ViewID)) {
		t.Fatal("notebook semantic document does not include the user request")
	}
	doc := m.conversationDocument()
	var childID conversation.BlockID
	m.itemExpanded = map[itemKey]bool{}
	for _, block := range doc.Blocks() {
		if block.Kind == conversation.BlockNarration {
			for _, child := range block.Children {
				if child.Kind == conversation.BlockTool {
					childID = child.ID
					m.itemExpanded[notebookItemKey(state.ScopeID(), block.ID)] = false
					break
				}
			}
		}
	}
	if childID == "" {
		t.Fatal("notebook projection has no owned audit child")
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if strings.Contains(ansi.Strip(m.viewport.GetContent()), "Found the exact source content") {
		t.Fatal("collapsed narration still shows its child output")
	}
	m.find.open = true
	m.find.query = "exact source content"
	m.refreshFind()
	if !m.gotoFindMatch() {
		t.Fatal("find could not navigate to collapsed owned output")
	}
	if !strings.Contains(ansi.Strip(m.viewport.GetContent()), "Found the exact source content") {
		t.Fatal("find did not expand the owning narration")
	}
	if _, ok := m.mappedBlockSpan(childID); !ok {
		t.Fatal("expanded audit child has no mapped selection geometry")
	}
	m.readingAnchor.Block = childID
	copyBlock, ok := m.copyBlock()
	if !ok || copyBlock.ID != childID || len(copyBlock.CopyTargets) == 0 || copyBlock.CopyTargets[0].Source != conversation.SourceOutput || copyBlock.CopyTargets[0].Text != "Found the exact source content." {
		t.Fatalf("copy resolved to the wrong scope: %+v, %v", copyBlock, ok)
	}
	var orderRegion *clickRegion
	for i := range m.clickRegions {
		if m.clickRegions[i].target.orderControl {
			orderRegion = &m.clickRegions[i]
			break
		}
	}
	if orderRegion == nil {
		t.Fatal("narration has no event-order control region")
	}
	firstOutput := strings.Index(ansi.Strip(m.viewport.GetContent()), "Found the exact source content")
	secondOutput := strings.Index(ansi.Strip(m.viewport.GetContent()), "Found the next related source")
	if firstOutput < 0 || secondOutput < 0 || firstOutput > secondOutput {
		t.Fatal("recorded event order is not visible before toggling")
	}
	line := orderRegion.startLine
	frame := m.frameRect().Transcript
	y := frame.Y + m.scrollHintRows() + m.breadcrumbRows() + line - m.viewport.YOffset()
	if _, handled := m.handleTranscriptClick(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft}); !handled {
		t.Fatal("event-order control click was not handled")
	}
	semanticChild, found := m.conversationDocument().LocateMember(string(childID))
	if !found || semanticChild.Block.ID != childID {
		t.Fatalf("order control changed semantic source identity: %+v, %v", semanticChild, found)
	}
	reversedOutput := ansi.Strip(m.viewport.GetContent())
	if strings.Index(reversedOutput, "Found the next related source") > strings.Index(reversedOutput, "Found the exact source content") {
		t.Fatal("event-order control did not reverse presentation")
	}
	for i := range m.clickRegions {
		if m.clickRegions[i].target.orderControl {
			orderRegion = &m.clickRegions[i]
			break
		}
	}
	line = orderRegion.startLine
	y = frame.Y + m.scrollHintRows() + m.breadcrumbRows() + line - m.viewport.YOffset()
	m.handleTranscriptClick(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
	restoredOutput := ansi.Strip(m.viewport.GetContent())
	if strings.Index(restoredOutput, "Found the exact source content") > strings.Index(restoredOutput, "Found the next related source") {
		t.Fatal("second event-order control click did not restore recorded order")
	}
	m.readingAnchor.Block = childID
	copyBlock, ok = m.copyBlock()
	if !ok || copyBlock.ID != childID || copyBlock.CopyTargets[0].Text != "Found the exact source content." {
		t.Fatalf("event-order presentation changed the exact copy payload: %+v, %v", copyBlock, ok)
	}
}

func TestNotebookNarrationCollapsedKeepsHeadlineAndOmitsChildren(t *testing.T) {
	block := conversation.Block{
		ID: "narration:old", Kind: conversation.BlockNarration,
		Members: []string{"narration:old", "audit:old"}, Text: "First line headline.\n\nLong detail.",
		Children: []conversation.Block{{ID: "audit:old", Kind: conversation.BlockTool, Members: []string{"audit:old"}, Text: "owned output"}},
	}
	parts := renderNotebookNarration(block, 80, false, false, false, nil)
	if len(parts) != 1 {
		t.Fatalf("collapsed narration rendered %d parts, want one", len(parts))
	}
	plain := ansi.Strip(parts[0].Text)
	if !strings.Contains(plain, "First line headline") || strings.Contains(plain, "owned output") {
		t.Fatalf("unexpected collapsed rendering: %q", plain)
	}
}
