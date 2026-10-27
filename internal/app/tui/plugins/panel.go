package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/plugins"
	"marshal/internal/strutil"
)

const scopeGlobal = "global"
const scopeProject = "project"

// Panel is the TUI management surface for installed plugins.
type Panel struct {
	homeDir        string
	workDir        string
	projectTrusted bool
	state          *session.State

	filter textfield.Model
	list   *settings.FieldList
	stack  []*settings.Frame

	installSource string
	installScope  string
	installErr    string
	// busy names the async operation in flight ("scanning", "installing") so
	// a slow git clone is not indistinguishable from a dead key binding.
	busy string
	// status is a transient confirmation line, so a completed action is
	// never silent.
	status          string
	scannedContents *plugins.Contents
	scannedName     string
	scannedSource   string
	// scannedScope is the scope the review was produced for. It is captured
	// from the scan result, not read back from installScope: the confirmation
	// must show what was reviewed, and a scope changed after the scan is a
	// different review (Task 2 invalidates it).
	scannedScope string
	// scannedRawSource is the text the user entered, kept beside the
	// normalized source so Confirm can prove the fields above still hold the
	// source the scan actually read.
	scannedRawSource string
	// scannedHash is the hash of the scanned clone after .git removal: the
	// content the user reviewed, and what the install compares against before
	// it commits anything.
	scannedHash   string
	scannedCommit string
	// scanParent is the temporary parent the claimed scan owns: the clone
	// lives in <parent>/clone, and the parent is what gets deleted. Empty when
	// no review is outstanding, or once an install worker has taken it.
	scanParent string
	// scanLease owns the outstanding scan until this panel claims it.
	scanLease *scanLease
	// scanGen identifies the current review. A result whose generation is not
	// this one cannot become the review, however late it arrives.
	scanGen int
	// scanDeps are the external operations a scan performs. Production uses
	// the real plugins package; tests inject delays and failures per panel.
	scanDeps scanDeps
	// reviewDetail is the open full-content view of one executable entry.
	// While it is open the review list is hidden and no key can reach the
	// Confirm action.
	reviewDetail *reviewDetail
	removeArmed  map[removeIdentity]bool
}

// reviewDetail is the pageable, wrapped full-content view of one executable
// entry: a hook command, an MCP server's command plus its individually quoted
// arguments, or an MCP policy. A review row is one line of one column wide, and
// the command that runs on every turn is usually longer than that, so the
// whole entry has to be readable somewhere before the user confirms it.
type reviewDetail struct {
	// rowID is the field ID of the row that opened this view, so Esc returns
	// the cursor to exactly the entry that was being read.
	rowID string
	title string
	// lines are escaped logical lines. Wrapping happens at render time, so a
	// resize re-wraps the text instead of re-splitting it.
	lines []string
	// offset is the first visible display row.
	offset int
	// width and height are the body size of the last render. Update never
	// sees the panel's dimensions, so paging relies on what View recorded.
	width, height int
}

// reviewWrapBreakpoints mirrors the main TUI's wrapping points (the tui
// package cannot be imported here: it imports this one).
const reviewWrapBreakpoints = "/-:"

// removeIdentity is the confirmation key for a removal: the entry's scope plus
// its lockfile name. The same name can be installed in both scopes, so a
// name-only key could confirm a deletion the user never armed, on an entry
// they were not looking at.
type removeIdentity struct {
	scope string
	name  string
}

// removalIdentity derives the confirmation key from the entry's lockfile
// provenance, so two entries sharing a name never share a confirmation.
func removalIdentity(e scopedEntry) removeIdentity {
	return removeIdentity{scope: e.Scope, name: e.Name}
}

var _ dock.Panel = (*Panel)(nil)

// The panel disposes its outstanding scan when the dock releases it, because a
// released panel never receives the scan's result message.
var _ dock.CloseAware = (*Panel)(nil)

// scanDeps are the external operations one scan performs. Production uses the
// plugins package directly; tests inject delays, failures and observations per
// panel, so no package-global hook is needed to exercise a race.
type scanDeps struct {
	NewInstaller func() (*plugins.Installer, error)
	Clone        func(ctx context.Context, inst *plugins.Installer, source, ref, dest string) (string, error)
	Scan         func(dir string) (plugins.Contents, error)
	Hash         func(dir string) (string, error)
}

// defaultScanDeps is the production scan: a real clone, a real scan, and the
// same hash the install and startup verification use.
func defaultScanDeps() scanDeps {
	return scanDeps{
		NewInstaller: plugins.NewInstaller,
		Clone: func(ctx context.Context, inst *plugins.Installer, source, ref, dest string) (string, error) {
			return inst.Clone(ctx, source, ref, dest)
		},
		Scan: plugins.ScanPlugin,
		Hash: plugins.HashDir,
	}
}

// NewPanel builds a plugins panel.
func NewPanel(homeDir, workDir string, projectTrusted bool, state *session.State) *Panel {
	return newPanelWithScanDeps(homeDir, workDir, projectTrusted, state, defaultScanDeps())
}

// newPanelWithScanDeps builds a panel whose scans run through deps. It is the
// only seam a test needs: everything else about the panel is production code.
func newPanelWithScanDeps(homeDir, workDir string, projectTrusted bool, state *session.State, deps scanDeps) *Panel {
	p := &Panel{
		homeDir:        homeDir,
		workDir:        workDir,
		projectTrusted: projectTrusted,
		state:          state,
		removeArmed:    make(map[removeIdentity]bool),
		installScope:   scopeGlobal,
		scanDeps:       deps,
	}
	p.filter = textfield.New()
	p.filter.SetVirtualCursor(true)
	p.filter.Focus()
	p.list = settings.NewFieldList(p.buildFields)
	return p
}

func (p *Panel) activeList() *settings.FieldList {
	if len(p.stack) > 0 {
		return settings.FrameList(p.stack[len(p.stack)-1])
	}
	return p.list
}

func (p *Panel) globalStoreDir() string {
	home, _ := os.UserHomeDir()
	if p.homeDir != "" {
		home = p.homeDir
	}
	return plugins.GlobalStoreDir(home)
}

func (p *Panel) projectStoreDir() string {
	return plugins.ProjectStoreDir(p.workDir)
}

func (p *Panel) globalLockPath() string {
	home, _ := os.UserHomeDir()
	if p.homeDir != "" {
		home = p.homeDir
	}
	return plugins.GlobalLockPath(home)
}

func (p *Panel) projectLockPath() string {
	return plugins.ProjectLockPath(p.workDir)
}

type scopedEntry struct {
	plugins.LockEntry
	Scope string
}

func (p *Panel) lockEntries() []scopedEntry {
	var entries []scopedEntry
	if lf, err := plugins.ReadLockfile(p.globalLockPath()); err == nil {
		for _, e := range lf.Plugins {
			e := e
			entries = append(entries, scopedEntry{LockEntry: e, Scope: scopeGlobal})
		}
	}
	if p.projectTrusted {
		if lf, err := plugins.ReadLockfile(p.projectLockPath()); err == nil {
			for _, e := range lf.Plugins {
				e := e
				entries = append(entries, scopedEntry{LockEntry: e, Scope: scopeProject})
			}
		}
	}
	return entries
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func (p *Panel) detailFrame(e scopedEntry) *settings.Frame {
	return settings.NewFrame(e.Name, func() []*settings.Field {
		var fields []*settings.Field

		name := settings.NewField("detail.name", "Name", settings.KindScalar)
		settings.SetFieldGetStr(name, func() string { return e.Name })
		fields = append(fields, name)

		src := settings.NewField("detail.source", "Source", settings.KindScalar)
		settings.SetFieldGetStr(src, func() string { return e.Source })
		fields = append(fields, src)

		commit := settings.NewField("detail.commit", "Commit", settings.KindScalar)
		settings.SetFieldGetStr(commit, func() string { return e.Commit })
		fields = append(fields, commit)

		scope := settings.NewField("detail.scope", "Scope", settings.KindScalar)
		settings.SetFieldGetStr(scope, func() string { return e.Scope })
		fields = append(fields, scope)

		update := settings.NewField("action.update", "Update", settings.KindAction)
		settings.SetFieldDesc(update, "check for a newer version")
		settings.SetFieldAct(update, func() tea.Cmd {
			// The previous review (and its clone) belongs to the entry the user
			// was looking at, not to this install.
			p.invalidateReview()
			p.installSource = e.Source
			p.installScope = e.Scope
			p.stack = append(p.stack, p.installFrame())
			return nil
		})
		fields = append(fields, update)

		remove := settings.NewField("action.remove", "Remove", settings.KindAction)
		settings.SetFieldDesc(remove, "delete this plugin from disk")
		id := removalIdentity(e)
		settings.SetFieldActLabel(remove, func() string {
			if p.removeArmed[id] {
				return "↵ confirm remove"
			}
			return "↵ remove"
		})
		// Leaving the row (arrow keys call the row's disarm) or the detail
		// frame must drop the armed confirmation, so it can never be carried
		// into a different selection or a later visit.
		settings.SetFieldDisarm(remove, func() { delete(p.removeArmed, id) })
		settings.SetFieldAct(remove, func() tea.Cmd {
			if !p.removeArmed[id] {
				p.removeArmed[id] = true
				return nil
			}
			delete(p.removeArmed, id)
			return p.runRemove(e)
		})
		fields = append(fields, remove)

		return fields
	})
}

type removeResultMsg struct {
	Name  string
	Scope string
	Err   error
}

type installResultMsg struct {
	Name  string
	Scope string
	Err   error
}

type scanResultMsg struct {
	// gen and lease bind the result to the frame that asked for it: a result
	// whose lease or generation is no longer current cannot become a review.
	gen          int
	lease        *scanLease
	RawSource    string
	Name         string
	Source       string
	Scope        string
	Commit       string
	ReviewedHash string
	Contents     plugins.Contents
	CloneDir     string
	Err          error
}

// OwnsMsg claims this panel's async result messages so the dock host
// delivers them. Without it the model cannot name these unexported types
// and drops them, so an install, scan, or remove completes while the UI
// shows nothing at all. See dock.MessageOwner.
func (p *Panel) OwnsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case installResultMsg, removeResultMsg, scanResultMsg:
		return true
	}
	return false
}

// fail records an error and surfaces it on the visible list. Storing it only
// on p.installErr rendered nowhere, so failures were silent.
func (p *Panel) fail(msg string) {
	p.installErr = msg
	p.activeList().ErrMsg = msg
}

func (p *Panel) runRemove(e scopedEntry) tea.Cmd {
	return func() tea.Msg {
		var storeDir, lockPath string
		if e.Scope == scopeGlobal {
			storeDir = p.globalStoreDir()
			lockPath = p.globalLockPath()
		} else {
			storeDir = p.projectStoreDir()
			lockPath = p.projectLockPath()
		}
		if err := os.RemoveAll(filepath.Join(storeDir, e.Name)); err != nil {
			return removeResultMsg{Err: err}
		}
		lf, err := plugins.ReadLockfile(lockPath)
		if err != nil {
			return removeResultMsg{Err: err}
		}
		lf.Remove(e.Name)
		if err := lf.Write(lockPath); err != nil {
			return removeResultMsg{Err: err}
		}
		return removeResultMsg{Name: e.Name, Scope: e.Scope}
	}
}

func (p *Panel) installFrame() *settings.Frame {
	return settings.NewFrame("Install plugin", func() []*settings.Field {
		var fields []*settings.Field

		source := settings.NewField("install.source", "Source", settings.KindScalar)
		settings.SetFieldDesc(source, "github:owner/repo, https://..., or local path")
		settings.SetFieldGetStr(source, func() string { return p.installSource })
		settings.SetFieldSetStr(source, func(v string) error {
			p.installSource = v
			// A review belongs to the exact source that was scanned. Editing it
			// invalidates the review — and disposes of its clone — rather than
			// leaving a Confirm that would install something else.
			if p.busy != "installing" {
				p.invalidateReview()
			}
			return nil
		})
		fields = append(fields, source)

		scope := settings.NewField("install.scope", "Scope", settings.KindEnum)
		options := []string{scopeGlobal}
		if p.projectTrusted {
			options = append(options, scopeProject)
		}
		settings.SetFieldOptions(scope, func() []string { return options })
		settings.SetFieldGetStr(scope, func() string { return p.installScope })
		settings.SetFieldSetStr(scope, func(v string) error {
			p.installScope = v
			// The scope is part of the review: a destination changed after the
			// scan is a different install.
			if p.busy != "installing" {
				p.invalidateReview()
			}
			return nil
		})
		fields = append(fields, scope)

		scan := settings.NewField("install.scan", "Scan", settings.KindAction)
		settings.SetFieldDesc(scan, "fetch and review plugin contents")
		settings.SetFieldActLabel(scan, func() string {
			if p.busy == "scanning" {
				return "scanning…"
			}
			return "↵ scan"
		})
		settings.SetFieldAct(scan, func() tea.Cmd {
			return p.runScan()
		})
		fields = append(fields, scan)

		if p.scannedContents != nil {
			// The review is a claim about one exact source and scope, so both
			// are shown as their own rows rather than left implicit in the
			// editable fields above. The source is the normalized clone URL
			// the scan actually fetched, not the text the user typed.
			reviewedSource := settings.NewField("confirm.reviewed_source", "Reviewed source", settings.KindScalar)
			settings.SetFieldGetStr(reviewedSource, func() string { return strutil.ShowControls(p.scannedSource) })
			fields = append(fields, reviewedSource)

			reviewedScope := settings.NewField("confirm.reviewed_scope", "Reviewed scope", settings.KindScalar)
			settings.SetFieldGetStr(reviewedScope, func() string { return p.scannedScope })
			fields = append(fields, reviewedScope)

			// Passive content header and rows.
			fields = append(fields, settings.NewField("confirm.passive", "Passive content", settings.KindHeader))
			if len(p.scannedContents.Skills) == 0 && len(p.scannedContents.Commands) == 0 {
				fields = append(fields, settings.NewField("confirm.passive.none", "(none)", settings.KindScalar))
			}
			for _, s := range p.scannedContents.Skills {
				f := settings.NewField("confirm.skill."+s.Name, s.Name, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return s.Description })
				fields = append(fields, f)
			}
			for _, c := range p.scannedContents.Commands {
				f := settings.NewField("confirm.cmd."+c.Name, "/"+c.Name, settings.KindScalar)
				settings.SetFieldGetStr(f, func() string { return c.Description })
				fields = append(fields, f)
			}

			// Executable content header and rows. Each row is an action that
			// opens the entry's complete text: one row can never hold a
			// command long enough to judge, and a truncated command is exactly
			// what a user must not be asked to authorise.
			fields = append(fields, settings.NewField("confirm.exec", "Executable content", settings.KindHeader))
			if len(p.scannedContents.Hooks) == 0 && len(p.scannedContents.MCPServers) == 0 && len(p.scannedContents.MCPPolicies) == 0 {
				fields = append(fields, settings.NewField("confirm.exec.none", "(none)", settings.KindScalar))
			}
			for _, h := range p.scannedContents.Hooks {
				h := h
				fields = append(fields, p.executableRow(
					"confirm.hook."+h.Event,
					h.Event,
					hookReviewTitle(h.Event),
					hookReviewLines(h),
				))
			}
			// Sorted, because map order reshuffled the list between renders
			// of the same data — on the screen whose whole purpose is letting
			// the user read and trust exactly these entries.
			for _, n := range sortedKeys(p.scannedContents.MCPServers) {
				n := n
				srv := p.scannedContents.MCPServers[n]
				fields = append(fields, p.executableRow(
					"confirm.mcp."+n,
					n,
					"mcp server · "+n,
					mcpReviewLines(n, srv),
				))
			}
			// MCP policies gated the "(none)" row above but were never
			// displayed, so a plugin carrying only policies asked the user to
			// confirm executable content the screen refused to show.
			for _, n := range sortedKeys(p.scannedContents.MCPPolicies) {
				n := n
				policy := p.scannedContents.MCPPolicies[n]
				fields = append(fields, p.executableRow(
					"confirm.mcppolicy."+n,
					n,
					"mcp policy · "+n,
					policyReviewLines(n, policy),
				))
			}

			confirm := settings.NewField("install.confirm", "Confirm install", settings.KindAction)
			settings.SetFieldDesc(confirm, "write plugin to disk and update lockfile")
			settings.SetFieldActLabel(confirm, func() string {
				if p.busy == "installing" {
					return "installing…"
				}
				return "↵ install"
			})
			settings.SetFieldAct(confirm, func() tea.Cmd {
				return p.runInstallFromScan()
			})
			fields = append(fields, confirm)
		}

		return fields
	})
}

// executableRow builds one review row for an executable entry. The row is an
// action, not a passive value: Enter opens the entry's complete text, because a
// single terminal row can neither hold nor safely render a command the user has
// to judge for themselves.
func (p *Panel) executableRow(id, title, detailTitle string, lines []string) *settings.Field {
	f := settings.NewField(id, strutil.ShowControls(title), settings.KindAction)
	settings.SetFieldActLabel(f, func() string { return "↵ review" })
	settings.SetFieldDesc(f, previewLine(lines))
	settings.SetFieldAct(f, func() tea.Cmd {
		p.reviewDetail = &reviewDetail{rowID: id, title: detailTitle, lines: lines}
		return nil
	})
	return f
}

// previewLine is the escaped, truncated first content line of a detail body.
// It is a hint on the review row; the detail view is what the user decides on,
// because a preview can only ever show a fragment of a command.
func previewLine(lines []string) string {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		line = strutil.ShowControls(line)
		if r := []rune(line); len(r) > 72 {
			return string(r[:71]) + "…"
		}
		return line
	}
	return ""
}

func hookReviewTitle(event string) string { return "hook · " + strutil.ShowControls(event) }

func hookReviewLines(h config.HookConfig) []string {
	return []string{
		"event:", "  " + strutil.ShowControls(h.Event),
		"command:", "  " + strutil.ShowControls(h.Command),
	}
}

func mcpReviewLines(name string, srv config.MCPServerConfig) []string {
	lines := []string{"server:", "  " + strutil.ShowControls(name)}
	if srv.Command != "" {
		lines = append(lines, "command:", "  "+strutil.ShowControls(srv.Command))
	}
	if srv.URL != "" {
		lines = append(lines, "url:", "  "+strutil.ShowControls(srv.URL))
	}
	lines = append(lines, fmt.Sprintf("arguments (%d):", len(srv.Args)))
	for i, arg := range srv.Args {
		lines = append(lines, fmt.Sprintf("  [%d] %s", i, quoteArg(arg)))
	}
	return lines
}

// quoteArg renders one MCP argument with visible boundaries. A space or a quote
// inside an argument must not read as the boundary between two arguments, and a
// control byte must be shown rather than executed by the terminal.
func quoteArg(arg string) string { return strutil.ShowControls(strconv.Quote(arg)) }

func policyReviewLines(name, policy string) []string {
	return []string{
		"policy:", "  " + strutil.ShowControls(name),
		"decision:", "  " + strutil.ShowControls(policy),
	}
}

// runScan starts a scan for the entered source and scope. Everything the review
// will claim — the generation, the exact entered source and the scope — is
// captured here, before any work starts, so a later edit cannot be mistaken for
// what was scanned.
func (p *Panel) runScan() tea.Cmd {
	if p.busy != "" {
		return nil
	}
	raw := strings.TrimSpace(p.installSource)
	if raw == "" {
		p.fail("enter a source first")
		return nil
	}
	// A new scan supersedes any previous review, including its clone.
	p.invalidateReview()
	p.scanGen++
	gen := p.scanGen
	scope := p.installScope
	p.busy = "scanning"
	p.installErr = ""

	ctx, cancel := context.WithCancel(context.Background())
	lease := newScanLease(cancel)
	p.scanLease = lease
	deps := p.scanDeps
	workDir := p.workDir
	return func() tea.Msg {
		return scanPlugin(ctx, deps, lease, gen, raw, scope, workDir)
	}
}

// scanPlugin performs one scan and owns the lease: every failure closes it, so
// a clone that never becomes a review is deleted whether or not this function's
// message is ever delivered.
func scanPlugin(ctx context.Context, deps scanDeps, lease *scanLease, gen int, rawSource, scope, workDir string) tea.Msg {
	failed := func(err error) scanResultMsg {
		lease.Close()
		return scanResultMsg{gen: gen, lease: lease, Err: err}
	}

	cloneURL, name, err := plugins.NormalizeSource(rawSource)
	if err != nil {
		return failed(err)
	}
	// Every derived name is validated, including the github: branch: a name is
	// a single directory inside the plugin store.
	if !plugins.ValidName(name) {
		return failed(fmt.Errorf("invalid plugin name %q derived from %q", name, rawSource))
	}
	// Validate the clone source against the workspace root before cloning,
	// matching the CLI and ACP install paths.
	if err := plugins.ValidateCloneSource(cloneURL, workDir); err != nil {
		return failed(err)
	}
	inst, err := deps.NewInstaller()
	if err != nil {
		return failed(err)
	}
	tmpDir, err := os.MkdirTemp("", "marshal-plugin-tui-*")
	if err != nil {
		return failed(err)
	}
	// The lease owns the parent from here on: a cancellation or a result that
	// is never delivered still has an owner that deletes it.
	if !lease.Publish(tmpDir) {
		return scanResultMsg{gen: gen, lease: lease, Err: context.Canceled}
	}
	cloneDest := filepath.Join(tmpDir, "clone")
	commit, err := deps.Clone(ctx, inst, cloneURL, "", cloneDest)
	if err != nil {
		return failed(err)
	}
	_ = os.RemoveAll(filepath.Join(cloneDest, ".git"))
	contents, err := deps.Scan(cloneDest)
	if err != nil {
		return failed(err)
	}
	// The hash covers exactly what was reviewed: .git is gone, so a re-clone of
	// the same commit hashes the same.
	hash, err := deps.Hash(cloneDest)
	if err != nil {
		return failed(err)
	}
	return scanResultMsg{
		gen:          gen,
		lease:        lease,
		RawSource:    rawSource,
		Name:         name,
		Source:       cloneURL,
		Scope:        scope,
		Commit:       commit,
		ReviewedHash: hash,
		Contents:     contents,
		CloneDir:     cloneDest,
	}
}

// clearReview drops the review state without touching the entered source or
// scope. The clone is not deleted here: the caller either already disposed of
// it or transferred it to an install worker.
func (p *Panel) clearReview() {
	p.scannedContents = nil
	p.scannedName = ""
	p.scannedSource = ""
	p.scannedRawSource = ""
	p.scannedScope = ""
	p.scannedHash = ""
	p.scannedCommit = ""
	p.scanParent = ""
	p.reviewDetail = nil
}

// invalidateReview abandons the current review. It advances the generation so a
// late result cannot attach, cancels and disposes of the outstanding scan or
// claimed clone, and drops the review state. It never blocks on a worker: the
// lease cancels the clone and deletes what the lease still owns.
func (p *Panel) invalidateReview() {
	p.scanGen++
	if p.scanLease != nil {
		p.scanLease.Close()
		p.scanLease = nil
	}
	if p.scanParent != "" {
		os.RemoveAll(p.scanParent)
	}
	// An install worker owns its clone now: editing the fields above must not
	// make the panel look idle while it is still writing.
	if p.busy != "installing" {
		p.busy = ""
	}
	p.clearReview()
}

// OnClose is the dock's permanent-release notification. The panel may never
// receive another message, so it disposes of the outstanding scan here instead
// of waiting for a result that will not arrive. See dock.CloseAware.
func (p *Panel) OnClose() { p.invalidateReview() }

// resetInstall clears the install/scan working state, discarding the scan's
// temporary clone.
//
// The clone outlives runScan (the confirm step copies from it), so it cannot
// be cleaned up with a deferred RemoveAll the way skills.Install does. Every
// path that abandons the review — Esc, panel close, replacement, or reopening
// the install frame — has to dispose of it, which invalidateReview does.
func (p *Panel) resetInstall() {
	p.invalidateReview()
	p.installSource = ""
	p.installScope = scopeGlobal
}

// storePaths resolves the store and lockfile paths for a scope.
func (p *Panel) storePaths(scope string) (storeDir, lockPath string) {
	if scope == scopeProject {
		return p.projectStoreDir(), p.projectLockPath()
	}
	return p.globalStoreDir(), p.globalLockPath()
}

func (p *Panel) runInstallFromScan() tea.Cmd {
	if p.busy != "" {
		return nil
	}
	if p.scannedContents == nil || p.scanParent == "" {
		p.fail("scan a source first")
		return nil
	}
	// A review belongs to the exact source and scope that were scanned: if the
	// fields above changed since, Confirm would install something the user
	// never reviewed.
	if strings.TrimSpace(p.installSource) != p.scannedRawSource || p.installScope != p.scannedScope {
		p.invalidateReview()
		p.fail("the source or scope changed since the scan; scan again")
		return nil
	}
	p.busy = "installing"
	p.installErr = ""
	name := p.scannedName
	scope := p.scannedScope
	source := p.scannedSource
	commit := p.scannedCommit
	reviewed := p.scannedHash
	cloneDest := filepath.Join(p.scanParent, "clone")
	// The worker owns the clone from here: clearing the panel's reference
	// before it starts is what stops a later panel close from deleting files
	// the worker is still copying.
	parent := p.scanParent
	p.scanParent = ""
	p.scanLease = nil
	storeDir, lockPath := p.storePaths(scope)
	return func() tea.Msg {
		// The worker always cleans its own clone, on every outcome — including
		// when this message is never delivered.
		defer os.RemoveAll(parent)
		// The install is staged and rolled back by the plugins package: a copy,
		// hash, rename or lockfile failure must leave the plugin that is
		// already installed, and its lockfile entry, exactly as they were.
		if err := plugins.InstallReviewed(context.Background(), plugins.ReviewedInstall{
			Name:         name,
			Source:       source,
			Commit:       commit,
			ReviewedHash: reviewed,
			CloneDir:     cloneDest,
			StoreDir:     storeDir,
			LockPath:     lockPath,
		}); err != nil {
			return installResultMsg{Err: err}
		}
		return installResultMsg{Name: name, Scope: scope}
	}
}

// sortedKeys returns a map's keys in a stable order, so trust-review screens
// render the same list the same way every time.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (p *Panel) buildFields() []*settings.Field {
	query := strings.TrimSpace(p.filter.Value())
	entries := p.lockEntries()

	fields := []*settings.Field{
		settings.NewField("header.installed", "Installed plugins", settings.KindHeader),
	}

	for _, e := range entries {
		e := e
		f := settings.NewField("plugin."+e.Name, e.Name, settings.KindDrill)
		settings.SetFieldDesc(f, fmt.Sprintf("%s · %s", e.Scope, e.Source))
		settings.SetFieldSummary(f, func() string { return shortCommit(e.Commit) })
		settings.SetFieldBuild(f, func() *settings.Frame { return p.detailFrame(e) })
		fields = append(fields, f)
	}

	if len(entries) == 0 {
		none := settings.NewField("none", "No plugins installed", settings.KindHeader)
		settings.SetFieldTitle(none, "(none)")
		fields = append(fields, none)
	}

	install := settings.NewField("action.install", "＋ Install plugin", settings.KindAction)
	settings.SetFieldDesc(install, "install a plugin from a git URL or local path")
	settings.SetFieldAct(install, func() tea.Cmd {
		p.resetInstall()
		p.stack = append(p.stack, p.installFrame())
		return nil
	})
	fields = append(fields, install)

	if query == "" {
		return fields
	}

	filtered := []*settings.Field{}
	for _, f := range fields {
		if f.Kind == settings.KindHeader {
			continue
		}
		hay := settings.FieldID(f) + " " + settings.FieldTitle(f) + " " + settings.FieldDesc(f)
		if len(fuzzy.Rank(query, []string{hay})) > 0 {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// The full-content view owns the keyboard while it is open: no key,
		// Enter included, may reach the review list's actions.
		if p.reviewDetail != nil {
			return p.updateReviewDetail(msg)
		}
		if msg.Code == tea.KeyEscape {
			// Leaving the frame — or the panel — drops any armed removal, so
			// a confirmation cannot carry over to another selection or a
			// later visit.
			p.activeList().DisarmCurrent()
			if len(p.stack) > 0 {
				p.stack = p.stack[:len(p.stack)-1]
				// Leaving the install frame abandons any outstanding scan.
				if len(p.stack) == 0 {
					p.invalidateReview()
				}
				return nil
			}
			p.invalidateReview()
			return func() tea.Msg { return settings.BrowserClosedMsg{} }
		}
		l := p.activeList()
		switch msg.String() {
		case "up", "down":
			return settings.FieldListUpdate(l, msg)
		case "enter":
			cmd := settings.FieldListUpdate(l, msg)
			if f := settings.FieldListTakePushRequest(l); f != nil {
				p.stack = append(p.stack, f)
			}
			return cmd
		default:
			if len(p.stack) > 0 || l.Editing() {
				return settings.FieldListUpdate(l, msg)
			}
			var cmd tea.Cmd
			p.filter, cmd = p.filter.Update(msg)
			p.list.Refresh()
			return cmd
		}
	case scanResultMsg:
		// Only the current scan's result may become this frame's review: the
		// lease and generation must still be the ones the worker was started
		// with. Anything else owns its own clone and is disposed of here, so a
		// late result from an abandoned frame cannot attach to a new one.
		if msg.lease == nil || msg.lease != p.scanLease || msg.gen != p.scanGen {
			if msg.lease != nil {
				msg.lease.Close()
			}
			return nil
		}
		p.busy = ""
		if msg.Err != nil {
			msg.lease.Close()
			p.scanLease = nil
			p.fail(msg.Err.Error())
			return nil
		}
		if strings.TrimSpace(p.installSource) != msg.RawSource || p.installScope != msg.Scope {
			// The fields above the review changed while the clone ran, so this
			// scan is for an install the user is no longer looking at.
			p.invalidateReview()
			p.fail("the source or scope changed while scanning; scan again")
			return nil
		}
		parent, ok := msg.lease.Claim()
		if !ok {
			p.scanLease = nil
			return nil
		}
		// The claim transfers the clone's ownership to this panel: from here
		// it belongs to the review, and only an install worker can take it.
		p.scanLease = nil
		p.scanParent = parent
		p.scannedContents = &msg.Contents
		p.scannedName = msg.Name
		p.scannedSource = msg.Source
		p.scannedRawSource = msg.RawSource
		p.scannedScope = msg.Scope
		p.scannedHash = msg.ReviewedHash
		p.scannedCommit = msg.Commit
		p.activeList().Refresh()
		return nil
	case installResultMsg:
		p.busy = ""
		if msg.Err != nil {
			p.fail(msg.Err.Error())
			// The worker already removed its clone, so the review now points
			// at nothing. Clear it rather than leave a Confirm action that
			// would copy from a deleted directory.
			p.clearReview()
			p.activeList().Refresh()
			return nil
		}
		p.status = fmt.Sprintf("installed %s (%s)", msg.Name, msg.Scope)
		// The install worker removed the clone; drop the review so nothing
		// tries to remove it a second time.
		p.resetInstall()
		settings.FieldListRefresh(p.list)
		if p.state != nil {
			p.state.AddMessage(session.RoleSystem, "Restart Marshal to activate the plugin change.", session.ContentTypePlain)
		}
		return nil
	case removeResultMsg:
		p.busy = ""
		if msg.Err != nil {
			p.fail(msg.Err.Error())
			return nil
		}
		p.status = fmt.Sprintf("removed %s (%s)", msg.Name, msg.Scope)
		// Pop detail frame and refresh the list.
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		p.list.Refresh()
		if p.state != nil {
			p.state.AddMessage(session.RoleSystem, "Restart Marshal to activate the plugin change.", session.ContentTypePlain)
		}
		return nil
	}
	return nil
}

func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 4 {
		return ""
	}
	pw := layout.PanelWidth(width)
	if p.reviewDetail != nil {
		return p.viewReviewDetail(pw, maxHeight)
	}
	l := p.activeList()
	settings.FieldListSetSize(l, pw-3, maxHeight-4)
	header := "/ " + p.filter.View()
	if len(p.stack) > 0 {
		header = settings.FrameTitle(p.stack[len(p.stack)-1])
	}
	body := header + "\n" + l.View()
	footer := fmt.Sprintf("%d plugins", len(settings.FieldListRows(l)))
	switch {
	case p.busy != "":
		footer += " · " + p.busy + "…"
	case p.status != "":
		footer += " · " + p.status
	}
	content := body + "\n" + chrome.TertiaryStyle().Render(footer)
	return chrome.PanelWithHints("Plugins", "", content, pw, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
}

// viewReviewDetail draws the open executable-content detail in place of the
// review list. The panel frame stays, so the surface the user is on does not
// change shape under them, and the entry's complete text is wrapped and paged
// rather than truncated.
func (p *Panel) viewReviewDetail(pw, maxHeight int) string {
	d := p.reviewDetail
	// The list view spends four rows on the panel's chrome and footer; the
	// detail reserves one more for the entry's own title line.
	bodyHeight := max(maxHeight-5, 1)
	d.width = max(pw-3, 1)
	d.height = bodyHeight
	lines := make([]string, 0, bodyHeight+1)
	lines = append(lines, d.title)
	lines = append(lines, d.window(bodyHeight)...)
	content := strings.Join(lines, "\n")
	return chrome.PanelWithHints("Plugins", "esc back · PgUp/PgDn", content, pw,
		min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
}

// updateReviewDetail consumes every key while the full-content view is open.
// None of them reaches the review list, so Enter here can never install: only
// Esc (back to the review) and PgUp/PgDn (page) do anything.
func (p *Panel) updateReviewDetail(msg tea.KeyPressMsg) tea.Cmd {
	d := p.reviewDetail
	switch msg.String() {
	case "esc":
		p.reviewDetail = nil
		p.focusRow(d.rowID)
		return nil
	case "pgup", "ctrl+u":
		d.page(-1)
		return nil
	case "pgdown", "ctrl+d":
		d.page(1)
		return nil
	}
	return nil
}

// focusRow parks the active list's cursor on id, so returning from a detail
// view lands on the entry the user was reading. A row that is gone (the review
// was invalidated, the frame changed) leaves the cursor where it is.
func (p *Panel) focusRow(id string) {
	for i, row := range settings.FieldListRows(p.activeList()) {
		if settings.FieldID(row) == id {
			p.activeList().SetCursor(i)
			return
		}
	}
}

// page moves the view by one visible page in direction (-1 up, +1 down),
// clamping to the wrapped content. It uses the size the last render recorded:
// Update never sees the panel's dimensions.
func (d *reviewDetail) page(direction int) {
	if d.width < 1 || d.height < 1 {
		return
	}
	rows := d.rows(d.width)
	if len(rows) == 0 {
		return
	}
	d.offset += direction * max(reviewBodyRows(len(rows), d.height), 1)
	d.clampOffset(len(rows), d.height)
}

// window returns at most height display rows from the current offset, with a
// position indicator when rows are omitted.
func (d *reviewDetail) window(height int) []string {
	if height <= 0 {
		return nil
	}
	rows := d.rows(d.width)
	total := len(rows)
	d.clampOffset(total, height)
	body := reviewBodyRows(total, height)
	end := min(d.offset+body, total)
	out := make([]string, 0, height)
	out = append(out, rows[d.offset:end]...)
	if height > 1 && total > body {
		out = append(out, chrome.TertiaryStyle().Render(reviewIndicator(d.offset, end, total)))
	}
	return out
}

// clampOffset pins offset to the range of representable windows.
func (d *reviewDetail) clampOffset(total, height int) {
	maxOffset := max(total-reviewBodyRows(total, height), 0)
	d.offset = min(max(d.offset, 0), maxOffset)
}

// rows wraps the escaped logical lines to the rendered width. Long lines wrap
// and are never truncated: the whole point of this view is that every byte of
// what will run is readable.
func (d *reviewDetail) rows(width int) []string {
	if width < 1 {
		width = 1
	}
	rows := make([]string, 0, len(d.lines))
	for _, line := range d.lines {
		// Split before wrapping so an embedded newline in a logical line is a
		// line break, not part of the last row.
		for _, logical := range strings.Split(line, "\n") {
			rows = append(rows, strings.Split(ansi.Wrap(logical, width, reviewWrapBreakpoints), "\n")...)
		}
	}
	return rows
}

// reviewBodyRows is how many content rows a window of height shows. When the
// content already fits, every row is content; otherwise one row is reserved
// for the position indicator.
func reviewBodyRows(total, height int) int {
	switch {
	case height <= 0:
		return 0
	case height == 1:
		return 1
	case total <= height:
		return total
	default:
		return height - 1
	}
}

// reviewIndicator labels the visible rows, 1-based and inclusive, against the
// total. It contains only generated counts, never user-supplied content.
func reviewIndicator(start, end, total int) string {
	return fmt.Sprintf("rows %d–%d of %d · PgUp/PgDn", start+1, end, total)
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
