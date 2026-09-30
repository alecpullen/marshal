package inspector

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/changedfiles"
)

// contextNow is the injected clock every Context fixture uses, so generated-at
// and attempt times are deterministic.
var contextNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// packFixture is a populated Current-pack scope: two sections, the first of
// them truncated by the pack's own budget.
func packFixture() ContextData {
	return ContextData{
		Now: contextNow,
		Pack: ContextPack{
			Known:           true,
			GeneratedAt:     contextNow.Add(-90 * time.Second),
			EstimatedTokens: 11_000,
			MaxTokens:       32_000,
			Truncated:       true,
			Sections: []ContextSection{
				{
					Title:  "internal/app/tui/model.go",
					Kind:   "file_snippet",
					Source: "internal/app/tui/model.go",
					// Priority 100 is the pin used by @file references, so the
					// detail can say the reader asked for this one by name.
					Priority:         100,
					EstimatedTokens:  8_400,
					Content:          "package tui\n",
					ContentTruncated: true,
					OmittedTokens:    3_100,
				},
				{
					Title:           "repo map",
					Kind:            "repo_map",
					Source:          "repository",
					EstimatedTokens: 2_600,
					Content:         "internal/\n",
				},
			},
		},
	}
}

// requestFixture is a populated Last-request scope, including an outcome, an
// option set, and a truncated field.
func requestFixture() ContextData {
	d := ContextData{
		Now: contextNow,
		Pack: ContextPack{
			Known:           true,
			GeneratedAt:     contextNow.Add(-2 * time.Minute),
			EstimatedTokens: 9_800,
			MaxTokens:       32_000,
			Sections: []ContextSection{
				{Title: "system", Kind: "repo_card", EstimatedTokens: 7_800},
				{Title: "repo map", Kind: "repo_map", EstimatedTokens: 2_000},
			},
		},
	}
	d.Request = ContextRequest{
		Known:      true,
		AttemptID:  7,
		At:         contextNow.Add(-3 * time.Second),
		Provider:   "ollama",
		Model:      "qwen2.5-coder:14b",
		Generation: "gen-2",
		Status:     "completed",
		Messages: []ContextMessage{
			{Role: "system", Content: "You are Marshal."},
			{Role: "user", Content: "explain the patch tool", Truncated: true, OmittedBytes: 4_096},
			{
				Role: "assistant",
				ToolCalls: []ContextToolCall{
					{ID: "call_1", Name: "file.read", Args: `{"path":"a.go"}`},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "package a\n"},
		},
		Tools: []ContextTool{
			{Name: "file.read", Description: "read a file"},
			{Name: "shell.run", Description: "run a command"},
		},
		Thinking:       "high",
		Streaming:      true,
		MaxTokens:      8_192,
		HasMaxTokens:   true,
		Temperature:    0.2,
		HasTemperature: true,
		ToolChoice:     "file.read",
		Truncated:      true,
		OmittedBytes:   4_096,
		PackTokens:     9_800,
		PackWindow:     32_000,
		PackTruncated:  false,
		PackSections:   2,
		PackKnown:      true,
	}
	return d
}

func contextModel(t *testing.T, d ContextData) *Model {
	t.Helper()
	m := New()
	m.Resize(90, 24)
	m.SetContext(d)
	return m
}

// --- scope selection -----------------------------------------------------

// The Context tab opens on the CURRENT PACK. It is the scope that always has
// an answer — the pack exists before the first request — so a reader who opens
// the tab in a fresh session sees content rather than an empty state they have
// to interpret.
func TestContextDefaultsToTheCurrentPack(t *testing.T) {
	m := contextModel(t, packFixture())
	if got := m.ContextScope(); got != ContextScopePack {
		t.Fatalf("ContextScope() = %q, want %q", got, ContextScopePack)
	}
}

func TestContextScopeSelectionRoundTrips(t *testing.T) {
	m := contextModel(t, requestFixture())

	m.SetContextScope(ContextScopeRequest)
	if got := m.ContextScope(); got != ContextScopeRequest {
		t.Fatalf("ContextScope() = %q after selecting the request", got)
	}
	m.SetContextScope(ContextScopePack)
	if got := m.ContextScope(); got != ContextScopePack {
		t.Fatalf("ContextScope() = %q after selecting the pack again", got)
	}
}

// An unknown scope is refused rather than stored: a scope the renderer does not
// know how to draw would show an empty panel with nothing to explain it.
func TestContextRefusesAnUnknownScope(t *testing.T) {
	m := contextModel(t, packFixture())
	m.SetContextScope(ContextScope("nonsense"))
	if got := m.ContextScope(); got != ContextScopePack {
		t.Fatalf("ContextScope() = %q after an unknown scope, want the previous %q", got, ContextScopePack)
	}
}

// --- current pack --------------------------------------------------------

// The pack scope reports the facts the plan names: when it was generated, each
// section's source and kind, its ESTIMATED tokens, and the pack's own budget
// and truncation.
func TestContextPackShowsGeneratedTimeSectionsAndBudget(t *testing.T) {
	m := contextModel(t, packFixture())
	view := stripANSIForTest(m.viewContext())

	for _, want := range []string{
		"current pack",
		"internal/app/tui/model.go", // section title
		"file_snippet",              // kind
		"repo_map",                  // second section's kind
		"11k",                       // estimated tokens
		"32k",                       // pack budget
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the pack scope does not show %q:\n%s", want, view)
		}
	}
	// The generated time is rendered relative to the injected clock, so a
	// reader can tell a pack built a moment ago from one built an hour ago.
	if !strings.Contains(view, "1m") && !strings.Contains(view, "ago") {
		t.Errorf("the pack scope does not say when the pack was generated:\n%s", view)
	}
}

// A pack that has never been built and a pack that is genuinely empty are
// different facts. Presenting "not built yet" as "0 sections" claims the agent
// is carrying nothing when in fact nothing has been assembled.
func TestContextDistinguishesAnEmptyPackFromNoPack(t *testing.T) {
	never := contextModel(t, ContextData{})
	neverView := stripANSIForTest(never.viewContext())
	if !strings.Contains(strings.ToLower(neverView), "has not been built") {
		t.Errorf("an unbuilt pack is not labelled as such:\n%s", neverView)
	}

	empty := contextModel(t, ContextData{Pack: ContextPack{Known: true}})
	emptyView := stripANSIForTest(empty.viewContext())
	if !strings.Contains(strings.ToLower(emptyView), "empty") {
		t.Errorf("an empty pack is not labelled as such:\n%s", emptyView)
	}
	if emptyView == neverView {
		t.Error("an empty pack and an unbuilt pack render identically")
	}
}

// Truncation is inferred from the section's own Content/Full relationship —
// recorded by the caller, never guessed at render time. A section whose text
// was cut says so and says how much is missing; one that was not cut does not.
func TestContextPackLabelsTruncatedSectionsOnly(t *testing.T) {
	m := contextModel(t, packFixture())
	m.OpenContextRowByLabel("internal/app/tui/model.go")

	if !m.ContextDetailTruncated() {
		t.Error("a truncated section is not marked truncated in its detail")
	}
	// The amount missing is disclosed in the LIST row, not spliced into the
	// body: the body is a copy source, and a marker appended to it would land
	// in the reader's file as content that was never in their repository.
	m.contextDetail().Top()
	if view := stripANSIForTest(m.viewContext()); !strings.Contains(view, "3k") {
		t.Errorf("the truncated section does not say how much is missing:\n%s", view)
	}
	// The detail still tells the two kinds of "more" apart.
	if detail := stripANSIForTest(m.contextDetail().View(m.contextDetail().Label())); !strings.Contains(detail, "not shown") {
		t.Errorf("the detail does not disclose that the rest is unreachable:\n%s", detail)
	}

	// The untruncated section must not carry the same label: a warning on
	// every row is a warning nobody reads.
	m.OpenContextRowByLabel("repo map")
	if m.ContextDetailTruncated() {
		t.Error("an untruncated section is marked truncated")
	}
}

// --- last conversation request -------------------------------------------

// The request scope reports the attempt itself: time, outcome, model and
// provider, the message roles present, the tool surface, the options that
// actually went out, and the metadata caps.
func TestContextRequestShowsAttemptFactsAndCaps(t *testing.T) {
	m := contextModel(t, requestFixture())
	m.SetContextScope(ContextScopeRequest)
	view := stripANSIForTest(m.viewContext())

	for _, want := range []string{
		"last conversation request",
		"completed",
		"ollama",
		"qwen2.5-coder:14b",
		"system",
		"user",
		"assistant",
		"tool",
		"file.read",
		"shell.run",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the request scope does not show %q:\n%s", want, view)
		}
	}
	// The caps are disclosed: a bounded body presented as complete is the
	// failure the caps exist to prevent.
	if !strings.Contains(view, "truncated") && !strings.Contains(view, "omitted") {
		t.Errorf("the request scope does not disclose its caps:\n%s", view)
	}
}

// A failed attempt keeps its snapshot and is marked as failed with the reason.
// Dropping it would leave the reader with a stale successful request and no
// way to see what the failure was about.
func TestContextRequestMarksAFailedAttempt(t *testing.T) {
	d := requestFixture()
	d.Request.Status = "failed"
	d.Request.Err = "connection refused"
	m := contextModel(t, d)
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(view, "failed") {
		t.Errorf("a failed attempt is not marked as failed:\n%s", view)
	}
	if !strings.Contains(view, "connection refused") {
		t.Errorf("a failed attempt does not report why:\n%s", view)
	}
}

// Before the first request — and after loading an old session, whose requests
// were never held in memory — there is nothing to show, and the panel says so
// in as many words rather than rendering an empty request.
func TestContextRequestHasAUsefulEmptyState(t *testing.T) {
	m := contextModel(t, ContextData{Pack: ContextPack{Known: true}})
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(strings.ToLower(view), "no request") {
		t.Errorf("the empty request state does not explain itself:\n%s", view)
	}
	if m.ContextRowCount() != 0 {
		t.Errorf("an absent request offers %d selectable rows", m.ContextRowCount())
	}
}

// The pack budget and the model's context window are different numbers about
// the same request, and the snapshot holds both. The request scope must label
// the pack figure as the pack's own estimate, so neither can be read as the
// other.
func TestContextRequestKeepsThePackBudgetSeparateFromTheWindow(t *testing.T) {
	d := requestFixture()
	// A pack budget that is clearly NOT the model window: if the render mixed
	// them, the label and the number would disagree and this catches it.
	d.Request.PackTokens = 9_800
	d.Request.PackWindow = 32_000
	m := contextModel(t, d)
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(strings.ToLower(view), "context pack") {
		t.Errorf("the pack figure is not labelled as the context pack:\n%s", view)
	}
	if strings.Contains(strings.ToLower(view), "context window") {
		t.Errorf("the request scope calls a pack figure a context window:\n%s", view)
	}
}

// Metadata the runtime did not record is reported as unknown. Inventing a
// provider, a model, or a timestamp would put a fabricated fact beside real
// ones, where nothing on screen distinguishes them.
func TestContextDoesNotInventUnknownMetadata(t *testing.T) {
	d := requestFixture()
	d.Request.Provider = ""
	d.Request.Model = ""
	d.Request.Generation = ""
	m := contextModel(t, d)
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if strings.Contains(view, "ollama") || strings.Contains(view, "qwen") {
		t.Errorf("the request scope invented route metadata:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "not recorded") {
		t.Errorf("the request scope does not say the route was not recorded:\n%s", view)
	}
}

// --- redaction -----------------------------------------------------------

// A secret in the request body is masked in the display AND in the copy it
// yields. One without the other is worse than neither: the panel would look
// safe while the clipboard carried the credential.
func TestContextRequestIsRedactedForDisplayAndCopy(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnopqrstuvwxyz012345"
	d := requestFixture()
	d.Request.Messages[0].Content = "You are Marshal. OPENAI_API_KEY=" + secret
	d.Request.Messages[1].Content = "the key is " + secret
	d.Request.Tools[0].Description = "reads a file; token=" + secret
	m := contextModel(t, d)
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if strings.Contains(view, secret) {
		t.Fatalf("the request scope rendered a secret:\n%s", view)
	}

	// Every copy path out of the panel must be redacted too.
	for _, i := range contextRowIndexes(m) {
		if !m.OpenContextRow(i) {
			continue
		}
		text, _, _, ok := m.CaptureContextCopy()
		if !ok {
			continue
		}
		if strings.Contains(text, secret) {
			t.Fatalf("copying row %d yielded an unredacted secret:\n%s", i, text)
		}
	}
}

// Redaction is disclosed. A reader who sees a masked value must be able to tell
// that the app did it, rather than believing the request itself contained a
// placeholder.
func TestContextLabelsRedactedContent(t *testing.T) {
	d := requestFixture()
	d.Request.Messages[0].Content = "OPENAI_API_KEY=sk-live-abcdefghijklmnopqrstuvwxyz012345"
	m := contextModel(t, d)
	m.SetContextScope(ContextScopeRequest)

	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(strings.ToLower(view), "redact") {
		t.Errorf("redacted content is not labelled as redacted:\n%s", view)
	}

	// Ordinary code must NOT be labelled: a redaction warning on every request
	// teaches the reader to ignore it.
	clean := contextModel(t, requestFixture())
	clean.SetContextScope(ContextScopeRequest)
	if strings.Contains(strings.ToLower(stripANSIForTest(clean.viewContext())), "redact") {
		t.Error("a request with no secret is labelled as redacted")
	}
}

// --- child scope ---------------------------------------------------------

// A child's context is an explicit scope, and leaving it restores the root's
// own selection rather than resetting the tab. The reader chose "Last request"
// before they went into the child; coming back must not silently move them.
func TestContextChildScopeTransitionsAndReturns(t *testing.T) {
	m := contextModel(t, packFixture())
	m.SetContextScope(ContextScopeRequest)

	child := packFixture()
	child.ScopeLabel = "agent #3 explore"
	child.Child = true
	m.SetChildContext("agent #3 explore", child)

	if !m.InChildContext() {
		t.Fatal("SetChildContext did not enter the child scope")
	}
	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(view, "agent #3 explore") {
		t.Errorf("the child scope does not name the child:\n%s", view)
	}

	if !m.ClearChildContext() {
		t.Fatal("ClearChildContext reported nothing to do")
	}
	if m.InChildContext() {
		t.Fatal("the child scope survived ClearChildContext")
	}
	if got := m.ContextScope(); got != ContextScopeRequest {
		t.Fatalf("ContextScope() = %q after leaving the child, want the previous %q", got, ContextScopeRequest)
	}
}

// A child scope shows the CHILD's data, never the root's. Showing the root's
// pack under a child's name is indistinguishable on screen from the child's.
func TestContextChildScopeShowsTheChildsOwnData(t *testing.T) {
	m := contextModel(t, packFixture())

	child := ContextData{
		ScopeLabel: "agent #4 summarise",
		Child:      true,
		Pack: ContextPack{
			Known:           true,
			EstimatedTokens: 512,
			MaxTokens:       4_000,
			Sections:        []ContextSection{{Title: "child-only-section", EstimatedTokens: 512}},
		},
	}
	m.SetChildContext("agent #4 summarise", child)

	view := stripANSIForTest(m.viewContext())
	if !strings.Contains(view, "child-only-section") {
		t.Errorf("the child scope does not show the child's section:\n%s", view)
	}
	if strings.Contains(view, "internal/app/tui/model.go") {
		t.Errorf("the child scope leaked the root's pack:\n%s", view)
	}
}

// The child heading names BOTH the child and the scope. A heading that
// replaced one with the other would leave the reader unable to tell whether
// they were looking at the child's pack or its last request — the one question
// the scope selector exists to answer.
func TestContextChildHeadingKeepsTheScopeName(t *testing.T) {
	m := contextModel(t, packFixture())
	m.SetChildContext("agent #5 review", packFixture())

	pack := stripANSIForTest(m.viewContext())
	for _, want := range []string{"agent #5 review", "current pack"} {
		if !strings.Contains(pack, want) {
			t.Errorf("the child pack heading does not name %q:\n%s", want, pack)
		}
	}

	m.SetContextScope(ContextScopeRequest)
	request := stripANSIForTest(m.viewContext())
	for _, want := range []string{"agent #5 review", "last conversation request"} {
		if !strings.Contains(request, want) {
			t.Errorf("the child request heading does not name %q:\n%s", want, request)
		}
	}
}

// Leaving the child scope with nothing to leave reports false, so a key
// handler can fall through to its other meanings instead of swallowing Esc.
func TestContextLeavingTheRootScopeReportsNothingToDo(t *testing.T) {
	m := contextModel(t, packFixture())
	if m.ClearChildContext() {
		t.Error("ClearChildContext claimed work at the root scope")
	}
}

// --- snapshot updates ----------------------------------------------------

// A new snapshot updates the tab's badges WITHOUT replacing the detail the
// reader is studying. Yanking a section out from under someone who is reading
// it is the failure; telling them it changed underneath them is the fix.
func TestContextNewSnapshotDoesNotReplaceTheOpenDetail(t *testing.T) {
	m := contextModel(t, packFixture())
	if !m.OpenContextRowByLabel("repo map") {
		t.Fatal("could not open the repo-map section")
	}
	before := m.contextDetail().Content()
	if before == "" {
		t.Fatal("the opened detail is empty, so this test proves nothing")
	}

	updated := packFixture()
	updated.Pack.EstimatedTokens = 20_000
	updated.Pack.Sections[1].Content = "internal/\ncmd/\nweb/\n"
	m.SetContext(updated)

	if got := m.contextDetail().Content(); got != before {
		t.Fatalf("the open detail was replaced by a new snapshot:\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
	if !m.ContextDetailStale() {
		t.Error("the detail changed underneath the reader with no indication")
	}
	// Re-opening the row is the explicit refresh, and it adopts the new body.
	if !m.OpenContextRowByLabel("repo map") {
		t.Fatal("could not re-open the repo-map section")
	}
	if m.ContextDetailStale() {
		t.Error("the detail still reports stale after an explicit re-open")
	}
	if got := m.contextDetail().Content(); got != updated.Pack.Sections[1].Content {
		t.Fatalf("the refreshed detail = %q, want the new body %q", got, updated.Pack.Sections[1].Content)
	}
}

// --- selection, copy, and paging ----------------------------------------

// Every copy out of the Context tab is the LABELLED, bounded content: the
// label names what was copied, and the truncation state travels with it so a
// prefix is never pasted as though it were whole.
func TestContextCopyIsTheLabelledBoundedContent(t *testing.T) {
	m := contextModel(t, packFixture())
	if !m.OpenContextRowByLabel("internal/app/tui/model.go") {
		t.Fatal("could not open the truncated section")
	}

	text, label, truncated, ok := m.CaptureContextCopy()
	if !ok {
		t.Fatal("CaptureContextCopy reported nothing to copy for an open section")
	}
	if text != "package tui\n" {
		t.Fatalf("copied text = %q, want the section's content", text)
	}
	if label == "" || !strings.Contains(label, "internal/app/tui/model.go") {
		t.Fatalf("copy label = %q, want it to name the section", label)
	}
	if !truncated {
		t.Error("a truncated section copies without saying it is truncated")
	}
}

// Nothing is open: the copy reports false rather than putting an empty string
// on the clipboard, which looks to the user exactly like a silent failure.
func TestContextCopyReportsNothingWhenNoRowIsOpen(t *testing.T) {
	m := contextModel(t, packFixture())
	if _, _, _, ok := m.CaptureContextCopy(); ok {
		t.Error("CaptureContextCopy claimed content with no row open")
	}
}

// The cursor moves over the rows of the scope on display, and the scope's
// selection is where Enter lands. A cursor that kept its index across a scope
// change would silently re-target Enter at an unrelated row.
func TestContextSelectionIsPerScope(t *testing.T) {
	m := contextModel(t, requestFixture())
	packRows := m.ContextRowCount()
	if packRows == 0 {
		t.Fatal("the pack scope offers no rows")
	}
	m.MoveContextSelection(1)

	m.SetContextScope(ContextScopeRequest)
	reqRows := m.ContextRowCount()
	if reqRows == 0 {
		t.Fatal("the request scope offers no rows")
	}
	if m.ContextCursor() >= reqRows {
		t.Fatalf("the cursor landed outside the request scope's %d rows", reqRows)
	}

	// Leaving and returning keeps each scope's own place.
	m.MoveContextSelection(2)
	requestCursor := m.ContextCursor()
	m.SetContextScope(ContextScopePack)
	if got := m.ContextCursor(); got != 1 {
		t.Fatalf("the pack cursor = %d, want the 1 it was left at", got)
	}
	m.SetContextScope(ContextScopeRequest)
	if got := m.ContextCursor(); got != requestCursor {
		t.Fatalf("the request cursor = %d, want the %d it was left at", got, requestCursor)
	}
}

// Paging keys move the DETAIL when one is open, and report how far it moved, so
// a caller can tell a working key from a swallowed one.
func TestContextPageKeysMoveTheOpenDetail(t *testing.T) {
	d := packFixture()
	var long strings.Builder
	for i := 0; i < 200; i++ {
		long.WriteString("line\n")
	}
	d.Pack.Sections[0].Content = long.String()
	d.Pack.Sections[0].ContentTruncated = false
	m := contextModel(t, d)
	if !m.OpenContextRowByLabel("internal/app/tui/model.go") {
		t.Fatal("could not open the long section")
	}

	m.PageContextDetail(1)
	if m.ContextDetailScroll() == 0 {
		t.Fatal("PageContextDetail did not move the detail")
	}
	m.ContextDetailTop()
	if got := m.ContextDetailScroll(); got != 0 {
		t.Fatalf("ContextDetailTop left the detail at %d", got)
	}
	m.ContextDetailBottom()
	if m.ContextDetailScroll() == 0 {
		t.Fatal("ContextDetailBottom left the detail at the top")
	}
}

// Every rendered row must fit the recorded width, at every supported size.
//
// A row one cell too wide wraps, which shifts every row below it and pushes the
// panel's own chrome off the bottom — the reader loses what they were reading
// because the content had a long name. The bug this pins is specifically that
// strutil.Truncate cuts to N runes and THEN appends the ellipsis, returning N+1
// cells, so a "truncate to width" that returns a row of width+1 looks correct
// until a frame is joined.
func TestContextRowsNeverExceedThePanelWidth(t *testing.T) {
	long := strings.Repeat("a-very-long-unbroken-token ", 40)

	pack := packFixture()
	pack.Pack.Sections[0].Title = long
	pack.Pack.Sections[0].Content = long

	req := requestFixture()
	req.Request.Messages[0].Content = long
	req.Request.Tools[0].Name = long
	req.Request.Tools[0].Description = long

	for _, tc := range []struct {
		name  string
		data  ContextData
		scope ContextScope
	}{
		{"pack", pack, ContextScopePack},
		{"request", req, ContextScopeRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The narrowest panel the rail is allowed to open at is
			// tui.side_panel.min_cols (default 30); the dock gets the whole
			// frame, which is at least 80. Widths below that are not
			// reachable, so asserting on them would be testing a frame
			// nothing can produce.
			for _, width := range []int{30, 31, 40, 80, 120, 200} {
				m := New()
				m.Resize(width, 24)
				m.SetContext(tc.data)
				m.SetContextScope(tc.scope)
				// Open the longest row too, so the detail's own lines are
				// measured rather than only the list's.
				m.OpenContextRow(0)

				out := m.viewContext()
				for i, line := range strings.Split(out, "\n") {
					if w := ansi.StringWidth(line); w > width {
						t.Fatalf("width %d: row %d is %d cells wide: %q", width, i, w, line)
					}
				}
			}
		})
	}
}

// --- tab order -----------------------------------------------------------

// The product's tab bar is Changes, Agents, Context, Overview — Changes first,
// because it is the tab a developer opens the inspector for. Overview is last
// and retained.
func TestTabOrderIsChangesAgentsContextOverview(t *testing.T) {
	want := []Tab{TabChanges, TabAgents, TabContext, TabOverview}
	got := VisibleTabs()
	if len(got) != len(want) {
		t.Fatalf("VisibleTabs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("VisibleTabs() = %v, want %v (position %d)", got, want, i)
		}
	}
	if start := New().SelectedTab(); start != TabChanges {
		t.Fatalf("New() selected %q, want the initial tab %q", start, TabChanges)
	}
}

// --- helpers -------------------------------------------------------------

// contextRowIndexes lists every selectable row index in the scope on display.
func contextRowIndexes(m *Model) []int {
	n := m.ContextRowCount()
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

// --- what Task 7 and 8 already depend on ---------------------------------

// The Context tab must not disturb the other tabs' own state: the four tabs
// share one Model, and a scope change is not navigation.
func TestContextDoesNotDisturbOtherTabs(t *testing.T) {
	m := contextModel(t, requestFixture())
	m.SetChanges(changedfiles.Snapshot{Status: changedfiles.StatusOK})
	m.SetAgents(agentsFixture(1, 2))
	m.SetState(TabChanges, TabState{Scroll: 3})

	m.SetContextScope(ContextScopeRequest)
	m.MoveContextSelection(1)

	if got := m.State(TabChanges).Scroll; got != 3 {
		t.Errorf("the Changes scroll = %d after Context navigation, want 3", got)
	}
	if got := m.AgentIDSelected(); got != 1 {
		t.Errorf("the Agents selection = %d after Context navigation, want 1", got)
	}
}
