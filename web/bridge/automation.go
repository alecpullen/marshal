package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Automations is a project's forge automation settings.
type Automations struct {
	ReviewBot *ReviewBotSettings `json:"reviewBot,omitempty"`
	CIFixer   *CIFixerSettings   `json:"ciFixer,omitempty"`
}

// ReviewBotSettings configures the PR review bot.
type ReviewBotSettings struct {
	Enabled bool   `json:"enabled"`
	RepoID  string `json:"repoId"`
	// Labels, Authors and SkipDrafts filter which PRs are reviewed. Empty
	// lists match everything.
	Labels     []string `json:"labels,omitempty"`
	SkipDrafts bool     `json:"skipDrafts"`
	Authors    []string `json:"authors,omitempty"`
	// Routing overrides the review recipe's model routing.
	Routing json.RawMessage `json:"routing,omitempty"`
	// AutoPost posts a finished review without waiting, unless a finding
	// has a severity in HoldSeverities.
	AutoPost       bool     `json:"autoPost"`
	HoldSeverities []string `json:"holdSeverities,omitempty"`
}

// CIFixerSettings configures the CI fixer.
type CIFixerSettings struct {
	Enabled bool   `json:"enabled"`
	RepoID  string `json:"repoId"`
	// Branches are the watched branches, as path.Match patterns.
	Branches   []string `json:"branches"`
	MaxMinutes int      `json:"maxMinutes,omitempty"`
	MaxUSD     float64  `json:"maxUsd,omitempty"`
	// Push pushes a fix straight to a branch matching PushBranches; every
	// other fix opens a pull request.
	Push         bool     `json:"push"`
	PushBranches []string `json:"pushBranches,omitempty"`
}

var validSeverities = map[string]bool{"blocking": true, "should-fix": true, "nit": true}

// validateAutomations checks the automation settings of one project.
func (f *Fleet) validateAutomations(a *Automations) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: automations: "+format, append([]any{errInvalidProjectSettings}, args...)...)
	}
	repoOK := func(id string) error {
		repo, ok := f.ws.Repo(id)
		if !ok {
			return bad("repoId %q is not a registered repo", id)
		}
		if repo.Forge == "" {
			return bad("repo %q has no forge declared", id)
		}
		return nil
	}
	if a == nil {
		return nil
	}
	if rb := a.ReviewBot; rb != nil {
		if rb.Enabled {
			if err := repoOK(rb.RepoID); err != nil {
				return err
			}
		}
		if len(rb.Routing) > 0 && string(rb.Routing) != "null" {
			var obj map[string]json.RawMessage
			if json.Unmarshal(rb.Routing, &obj) != nil {
				return bad("reviewBot.routing must be a JSON object")
			}
		}
		for _, s := range rb.HoldSeverities {
			if !validSeverities[s] {
				return bad("holdSeverities: %q is not blocking, should-fix or nit", s)
			}
		}
	}
	if ci := a.CIFixer; ci != nil {
		if ci.Enabled {
			if err := repoOK(ci.RepoID); err != nil {
				return err
			}
			if len(ci.Branches) == 0 {
				return bad("ciFixer.branches must name at least one branch")
			}
		}
		if ci.MaxMinutes < 0 || ci.MaxUSD < 0 {
			return bad("ciFixer limits cannot be negative")
		}
		for _, list := range [][]string{ci.Branches, ci.PushBranches} {
			for _, pat := range list {
				if _, err := path.Match(pat, ""); err != nil {
					return bad("branch pattern %q is malformed", pat)
				}
			}
		}
	}
	return nil
}

// automationProject finds the first project (by root) whose automation
// settings satisfy match.
func (w *Workspace) automationProject(match func(*Automations) bool) (string, ProjectSettings, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	roots := make([]string, 0, len(w.projectSettings))
	for root := range w.projectSettings {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if ps := w.projectSettings[root]; ps.Automations != nil && match(ps.Automations) {
			return root, ps, true
		}
	}
	return "", ProjectSettings{}, false
}

// reviewBotSettings returns the review bot settings that apply to repoID,
// with the root of the project that owns them.
func (f *Fleet) reviewBotSettings(repoID string) (string, *ReviewBotSettings, bool) {
	root, ps, ok := f.ws.automationProject(func(a *Automations) bool {
		return a.ReviewBot != nil && a.ReviewBot.Enabled && a.ReviewBot.RepoID == repoID
	})
	if !ok {
		return "", nil, false
	}
	return root, ps.Automations.ReviewBot, true
}

// ciFixerSettings is reviewBotSettings for the CI fixer.
func (f *Fleet) ciFixerSettings(repoID string) (string, *CIFixerSettings, bool) {
	root, ps, ok := f.ws.automationProject(func(a *Automations) bool {
		return a.CIFixer != nil && a.CIFixer.Enabled && a.CIFixer.RepoID == repoID
	})
	if !ok {
		return "", nil, false
	}
	return root, ps.Automations.CIFixer, true
}

// autoState is the in-memory half of the automations: the keys of runs
// still in flight (so a burst of events starts one run, not several), and
// the seams tests replace.
type autoState struct {
	mu       sync.Mutex
	inflight map[string]bool
	// deliveries holds recent webhook delivery ids, against replay.
	deliveries map[string]time.Time
	drafts     *autoStore[ReviewDraft]
	history    *autoStore[CIHistoryEntry]

	// Tests replace these; nil uses the Fleet's own.
	onPR      func(context.Context, prEvent)
	onCheck   func(context.Context, checkEvent)
	runRecipe func(context.Context, string, RecipeRunRequest) (string, error)
	diff      func(context.Context, string, string) (json.RawMessage, error)
	discard   func(context.Context, string) error
	exit      func(context.Context, string, ExitOptions) (ExitResult, error)
	// branchHead resolves a branch's tip commit for the polling fallback.
	branchHead func(context.Context, Repo, string) (string, error)
}

// autoClaim marks key as in flight. It reports false when it already is.
func (f *Fleet) autoClaim(key string) bool {
	f.auto.mu.Lock()
	defer f.auto.mu.Unlock()
	if f.auto.inflight[key] {
		return false
	}
	if f.auto.inflight == nil {
		f.auto.inflight = map[string]bool{}
	}
	f.auto.inflight[key] = true
	return true
}

func (f *Fleet) autoRelease(key string) {
	f.auto.mu.Lock()
	delete(f.auto.inflight, key)
	f.auto.mu.Unlock()
}

// autoContext is a context cancelled when the fleet closes.
func (f *Fleet) autoContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-f.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (f *Fleet) autoRunRecipe(ctx context.Context, name string, req RecipeRunRequest) (string, error) {
	if f.auto.runRecipe != nil {
		return f.auto.runRecipe(ctx, name, req)
	}
	return f.RunRecipe(ctx, name, req)
}

func (f *Fleet) autoDiff(ctx context.Context, id, p string) (json.RawMessage, error) {
	if f.auto.diff != nil {
		return f.auto.diff(ctx, id, p)
	}
	return f.Diff(ctx, id, p)
}

func (f *Fleet) autoDiscard(ctx context.Context, id string) error {
	if f.auto.discard != nil {
		return f.auto.discard(ctx, id)
	}
	return f.Discard(ctx, id)
}

func (f *Fleet) autoExit(ctx context.Context, id string, opts ExitOptions) (ExitResult, error) {
	if f.auto.exit != nil {
		return f.auto.exit(ctx, id, opts)
	}
	return f.Exit(ctx, id, opts)
}

// automationDelta is the "automation" fleet delta. Title and Text feed the
// outbound notification of the same name.
type automationDelta struct {
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId,omitempty"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Status    string `json:"status,omitempty"`
	Title     string `json:"title,omitempty"`
	Text      string `json:"text,omitempty"`
	At        int64  `json:"at"`
}

func (f *Fleet) emitAutomation(typ, id, agentID, status, title, text string) {
	f.emit(automationDelta{Kind: "automation", SessionID: studioOwner, AgentID: agentID,
		Type: typ, ID: id, Status: status, Title: title, Text: text, At: f.now().UnixMilli()})
}

// autoStore keeps one JSON file per record under <state>/automations/<kind>.
type autoStore[T any] struct {
	dir string
	mu  sync.Mutex
}

var autoIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

var errUnknownAutomation = errors.New("bridge: unknown automation record")

func newAutoStore[T any](stateDir, kind string) *autoStore[T] {
	return &autoStore[T]{dir: filepath.Join(stateDir, "automations", kind)}
}

func (s *autoStore[T]) put(id string, v T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putLocked(id, v)
}

// update applies mutate to the stored record under the store lock. mutate
// returns an error to abandon the change.
func (s *autoStore[T]) update(id string, mutate func(*T) error) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.get(id)
	if err != nil {
		return v, err
	}
	if err := mutate(&v); err != nil {
		return v, err
	}
	return v, s.putLocked(id, v)
}

func (s *autoStore[T]) putLocked(id string, v T) error {
	if !autoIDRe.MatchString(id) {
		return fmt.Errorf("bad automation id %q", id)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, id+".json"), data, 0o600)
}

func (s *autoStore[T]) get(id string) (T, error) {
	var zero T
	if !autoIDRe.MatchString(id) {
		return zero, errUnknownAutomation
	}
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return zero, errUnknownAutomation
	}
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, fmt.Errorf("automation record %s is corrupt: %w", id, err)
	}
	return v, nil
}

// all returns every readable record; a corrupt one is logged and skipped.
func (s *autoStore[T]) all() []T {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []T
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		v, err := s.get(id)
		if err != nil {
			slog.Default().Warn("webbridge: skipping automation record", "id", id, "err", err)
			continue
		}
		out = append(out, v)
	}
	return out
}

func newAutoID() string { return newAgentID() }

func (f *Fleet) reviewDrafts() *autoStore[ReviewDraft] {
	f.auto.mu.Lock()
	defer f.auto.mu.Unlock()
	if f.auto.drafts == nil {
		f.auto.drafts = newAutoStore[ReviewDraft](f.stateDir, "review")
	}
	return f.auto.drafts
}

func (f *Fleet) ciHistory() *autoStore[CIHistoryEntry] {
	f.auto.mu.Lock()
	defer f.auto.mu.Unlock()
	if f.auto.history == nil {
		f.auto.history = newAutoStore[CIHistoryEntry](f.stateDir, "ci")
	}
	return f.auto.history
}
