package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// FactoryAPI keeps HTTP transport separate from the durable controller.
type FactoryAPI interface {
	Create(FactoryCreate) (FactoryItem, error)
	Get(string) (FactoryItem, error)
	List() []FactoryItem
	Command(context.Context, string, FactoryCommand) (FactoryItem, error)

	// The four below are the MCP service surface. Each takes the
	// server-authenticated principal as an argument, because the REST body and
	// the MCP arguments must never be able to name their own client. The
	// Prepare step runs outside the policy lock; the three durable steps run
	// inside it and do no external work.
	PrepareDraft(FactoryCreate) (FactoryCreate, error)
	CreateDraftAuthorized(FactoryCreate, FactoryCreate, factoryPrincipal) (FactoryItem, error)
	ReplaceDraftAuthorized(string, FactoryCreate, uint64, factoryPrincipal) (FactoryItem, error)
	CommandAuthorized(context.Context, string, FactoryCommand, factoryPrincipal) (FactoryItem, error)
}
type factoryAPI struct {
	store      *FactoryStore
	controller *FactoryController
	fleet      *Fleet
	// assessor is the optional adapter that answers an explicitly requested
	// completion assessment. It is nil by default, and a nil adapter records
	// the assessment as unavailable: an assessment that does not exist is a
	// state the surfaces render, and it never starts a planner of its own.
	assessor FactoryCompletionAssessor
}

var errFactoryConfig = errors.New("factory configuration or capability unsupported")
var errFactoryInput = errors.New("invalid factory input")

// factoryStudioRevisionActor is the identity a Studio plan-revision command is
// journalled under. It is derived here, on the server side of the transport:
// FactoryCommand and FactoryRevisionCommand carry no field that could name an
// identity, and factoryDecode refuses unknown fields, so the authenticated
// Studio surface is the only caller that reaches the plain Command path with
// one of these kinds.
const factoryStudioRevisionActor = "studio"

// factoryFollowUpCandidate is the exact follow-up draft a user confirmed,
// normalised as the normal factory draft it becomes. It performs no external
// work: the transport preflight reaches the fleet, and this is the validation
// the journal itself can make before it records the intent.
//
// The returned create is keyed by the deterministic follow-up request identity
// the caller supplies, so a request id in the payload is neither needed nor
// used.
func factoryFollowUpCandidate(c FactoryCreate) (FactoryCreate, error) {
	normalized := c
	if strings.TrimSpace(normalized.RequestID) == "" {
		normalized.RequestID = "follow-up"
	}
	if err := factoryNormalize(&normalized); err != nil {
		return FactoryCreate{}, fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	normalized.RequestID = ""
	return normalized, nil
}

func NewFactoryAPI(store *FactoryStore, controller *FactoryController, fleet *Fleet) FactoryAPI {
	return &factoryAPI{store: store, controller: controller, fleet: fleet}
}
func (a *factoryAPI) priorRequest(id string) bool {
	if a.store == nil {
		return false
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	_, ok := a.store.requests[id]
	return ok
}
func (a *factoryAPI) Get(id string) (FactoryItem, error) {
	if a.store == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	item, err := a.store.Get(id)
	if err != nil {
		return FactoryItem{}, err
	}
	return withAssessmentFreshness(item), nil
}
func (a *factoryAPI) List() []FactoryItem {
	if a.store == nil {
		return []FactoryItem{}
	}
	items := a.store.List()
	for n := range items {
		items[n] = withAssessmentFreshness(items[n])
	}
	return items
}
func (a *factoryAPI) Create(req FactoryCreate) (FactoryItem, error) {
	if a.store == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	// A replay returns the original item, so it must be recognised before the
	// project's defaults are read: resolution consults mutable state and is
	// therefore not deterministic, while the replay identity is the request the
	// operator sent.
	if a.priorRequest(req.RequestID) {
		return a.store.Create(req, time.Now().UTC())
	}
	resolved, err := a.factoryResolveDefaults(req)
	if err != nil {
		return FactoryItem{}, err
	}
	if err = factoryNormalize(&resolved); err != nil {
		return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	if err = a.preflight(context.Background(), resolved.Config, false); err != nil {
		return FactoryItem{}, err
	}
	return a.store.CreateResolved(req, resolved, time.Now().UTC())
}

// PrepareFrozen resolves, normalizes and preflights one handoff without
// writing anything, so a conversion can freeze the exact configuration and the
// project-settings version it was reviewed against before it reserves an
// intent. It is the read-only half of Create: Create's own behaviour is
// unchanged, and nothing here touches the factory journal.
func (a *factoryAPI) PrepareFrozen(req FactoryCreate) (FactoryCreate, string, error) {
	resolved, err := a.prepareDraft(req)
	if err != nil {
		return FactoryCreate{}, "", err
	}
	if a.fleet == nil || a.fleet.ws == nil {
		return FactoryCreate{}, "", fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	settingsVersion, err := a.factorySettingsVersion(context.Background(), resolved.Config.ProjectRoot)
	if err != nil {
		return FactoryCreate{}, "", err
	}
	return resolved, settingsVersion, nil
}

// factorySessionSubagents reads the Marshal session's reported child state
// from the control session's status. It is the only place the bridge learns
// what defaults the runtime would apply, and it reads them from the status
// JSON: the bridge never imports the agent side.
//
// Every failure — no control agent, no session, an agent that does not answer
// the method — contributes nothing, which is exactly what a session reporting
// neither defaults nor capabilities contributes. An override that demands
// enforcement is then refused at approval rather than resolved against a
// guess, and a configuration that states no override keeps the behaviour it
// has always had.
func (a *factoryAPI) factorySessionSubagents(ctx context.Context) *factorySubagentRuntime {
	if a == nil || a.fleet == nil {
		return nil
	}
	raw, err := a.fleet.controlCall(ctx, "session/status", nil, true)
	if err != nil {
		return nil
	}
	var status struct {
		SubagentControls *struct {
			Settings bool `json:"settings"`
		} `json:"subagentControls"`
		SubagentDefaults *struct {
			Settings struct {
				Enabled     bool   `json:"enabled"`
				Model       string `json:"model"`
				Concurrency int    `json:"concurrency"`
			} `json:"settings"`
			Digest string `json:"digest"`
		} `json:"subagentDefaults"`
	}
	if json.Unmarshal(raw, &status) != nil || status.SubagentDefaults == nil {
		return nil
	}
	return &factorySubagentRuntime{
		Settings:    status.SubagentControls != nil && status.SubagentControls.Settings,
		Enabled:     status.SubagentDefaults.Settings.Enabled,
		Model:       status.SubagentDefaults.Settings.Model,
		Concurrency: status.SubagentDefaults.Settings.Concurrency,
		Digest:      status.SubagentDefaults.Digest,
	}
}

// factorySettingsVersion is the version a review surface displays and an
// approval echoes back: the project's settings ETag and, when the session
// reports them, the digest of the defaults it derived. A change on either
// side makes a reviewed version stale, because the approval would otherwise
// freeze settings nobody looked at.
//
// A session that reports no defaults contributes no digest, so the version
// stays the project's ETag alone and a review of a project without a Marshal
// snapshot behaves exactly as it always did.
func (a *factoryAPI) factorySettingsVersion(ctx context.Context, root string) (string, error) {
	return a.factorySettingsVersionOf(root, a.factorySessionSubagents(ctx))
}

// factorySettingsVersionOf composes the version from an already-read runtime
// report, so an approval that needs the report for resolution as well reads
// the control session once.
func (a *factoryAPI) factorySettingsVersionOf(root string, rt *factorySubagentRuntime) (string, error) {
	if a == nil || a.fleet == nil || a.fleet.ws == nil {
		return "", fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	etag, err := a.fleet.ws.ProjectSettingsETag(root)
	if err != nil {
		return "", fmt.Errorf("%w: project settings version: %v", ErrFactoryUnavailable, err)
	}
	if rt != nil && rt.Digest != "" {
		return etag + ":" + rt.Digest, nil
	}
	return etag, nil
}

// factoryFreezeSubagents resolves the subagent settings an approval revision
// freezes, refusing the two cases that cannot be honoured.
//
// displayed is the version the reviewer saw. When the caller sends one it must
// still be current, or the approval is refused as a conflict: the settings it
// would freeze are not the settings that were displayed. A caller that sends
// none — a legacy client, or a command replayed from an older journal — is not
// checked, and proceeds as it always did.
//
// An override that asks the runtime to enforce subagent settings needs a
// session that advertises per-session settings; without one the request is
// refused as unsupported rather than approved and quietly ignored. A resolved
// concurrency outside the enforceable range is refused too, never clamped.
func (a *factoryAPI) factoryFreezeSubagents(ctx context.Context, root string, c FactoryConfig, displayed string) (*factorySubagentFreeze, error) {
	runtime := a.factorySessionSubagents(ctx)
	current, err := a.factorySettingsVersionOf(root, runtime)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(displayed) != "" && displayed != current {
		return nil, fmt.Errorf("%w: project or session settings changed since review (reviewed %q, now %q)", ErrFactoryConflict, displayed, current)
	}
	var defaults *FactoryDefaults
	if a.fleet != nil && a.fleet.ws != nil && root != "" {
		defaults = a.fleet.ws.ProjectSettingsFor(root).FactoryDefaults
	}
	implementer, reviewer := factoryResolveSubagents(c, defaults, runtime)
	if factorySubagentEnforcement(c, defaults) {
		if runtime == nil || !runtime.Settings {
			return nil, fmt.Errorf("%w: the session does not support per-session subagent settings, so the override cannot be enforced", errFactoryConfig)
		}
		for _, role := range []struct {
			name string
			r    factorySubagentResolution
		}{{"implementer", implementer}, {"reviewer", reviewer}} {
			if !factorySubagentConcurrencySupported(role.r.Settings.Concurrency) {
				return nil, fmt.Errorf("%w: %s subagent concurrency %d is outside %d..%d", errFactoryConfig, role.name, role.r.Settings.Concurrency, factorySubagentMinConcurrency, factorySubagentMaxConcurrency)
			}
		}
	}
	return &factorySubagentFreeze{implementer: implementer.Settings, reviewer: reviewer.Settings}, nil
}

// PrepareDraft resolves, normalizes and preflights one candidate draft without
// writing anything: the half of an authorized create or replacement that must
// not run while the policy lock is held, because resolution reads mutable
// project state and preflight reaches the forge and the workspace store.
func (a *factoryAPI) PrepareDraft(req FactoryCreate) (FactoryCreate, error) {
	return a.prepareDraft(req)
}

// prepareDraft is the shared no-lock half of every create path.
func (a *factoryAPI) prepareDraft(req FactoryCreate) (FactoryCreate, error) {
	resolved, err := a.factoryResolveDefaults(req)
	if err != nil {
		return FactoryCreate{}, err
	}
	if err = factoryNormalize(&resolved); err != nil {
		return FactoryCreate{}, fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	if err = a.preflight(context.Background(), resolved.Config, false); err != nil {
		return FactoryCreate{}, err
	}
	return resolved, nil
}

// CreateDraftAuthorized journals a draft an authenticated MCP client proposed.
// It is the durable half of an authorized create: the caller holds the policy
// lock, has authorized createDraft, and has already resolved, normalized and
// preflighted the candidate with PrepareDraft. Nothing here does external work,
// and the provenance rides the same append that creates the item.
//
// The journal's request identity is namespaced and the client's own ID survives
// only in provenance. A request that already committed replays its original
// item before the candidate is read, so a retry after a lost response returns
// what is durable instead of a second draft.
func (a *factoryAPI) CreateDraftAuthorized(req, resolved FactoryCreate, p factoryPrincipal) (FactoryItem, error) {
	if a.store == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(p.ClientID) == "" || strings.TrimSpace(p.Action) == "" {
		return FactoryItem{}, fmt.Errorf("%w: authenticated client and action required", errFactoryInput)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return FactoryItem{}, fmt.Errorf("%w: requestId required", errFactoryInput)
	}
	p.RequestID = req.RequestID
	scoped := factoryScopedRequestID(p.ClientID, req.RequestID)
	req.RequestID = scoped
	resolved.RequestID = scoped
	return a.store.CreateAuthorized(req, resolved, p, time.Now().UTC())
}

// ReplaceDraftAuthorized replaces an unapproved draft's content on behalf of an
// authenticated MCP client. It is the durable half of an authorized edit: the
// caller holds the policy lock, has authorized editDraft, and has already
// validated the candidate with PrepareDraft. The store rechecks the state, the
// item/project/repo identity and the expected version under its own lock, so a
// draft approved in the meantime is refused rather than rewritten.
func (a *factoryAPI) ReplaceDraftAuthorized(id string, candidate FactoryCreate, expectedVersion uint64, p factoryPrincipal) (FactoryItem, error) {
	if a.store == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(p.ClientID) == "" || strings.TrimSpace(p.Action) == "" {
		return FactoryItem{}, fmt.Errorf("%w: authenticated client and action required", errFactoryInput)
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(candidate.RequestID) == "" || expectedVersion == 0 {
		return FactoryItem{}, fmt.Errorf("%w: item, requestId and expectedVersion required", errFactoryInput)
	}
	p.RequestID = candidate.RequestID
	request := factoryScopedRequestID(p.ClientID, candidate.RequestID)
	return a.store.ReplaceDraft(id, request, expectedVersion, candidate, p, time.Now().UTC())
}

// CommandAuthorized applies an approval or an answer on behalf of an
// authenticated MCP client. It is the durable half of an authorized command:
// the caller holds the policy lock, has authorized the capability and has
// preflighted whatever the command needs outside it. Only local validation and
// the journal append happen here.
func (a *factoryAPI) CommandAuthorized(ctx context.Context, id string, cmd FactoryCommand, p factoryPrincipal) (FactoryItem, error) {
	if a.store == nil || a.controller == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(cmd.RequestID) == "" || cmd.ExpectedVersion == 0 {
		return FactoryItem{}, fmt.Errorf("%w: requestId and expectedVersion required", errFactoryInput)
	}
	return a.controller.CommandAuthorized(ctx, id, cmd, p)
}

// CreateFrozen creates the handoff a conversion froze. It is callable only
// after that conversion has durably reserved its intent, and it never reads
// the project's mutable defaults: the configuration it receives is exactly the
// one that was reviewed, so an empty field cannot inherit a value that changed
// while the intent was in flight. That is why it exists next to Create, which
// resolves defaults on every attempt.
//
// The journal is consulted first, exactly as Create does, so a crash after the
// factory accepted the request replays the item it produced instead of
// creating a second one.
func (a *factoryAPI) CreateFrozen(req, resolved FactoryCreate) (FactoryItem, error) {
	if a.store == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if a.priorRequest(req.RequestID) {
		return a.store.Create(req, time.Now().UTC())
	}
	if err := factoryNormalize(&resolved); err != nil {
		return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
	}
	if err := a.preflight(context.Background(), resolved.Config, false); err != nil {
		return FactoryItem{}, err
	}
	return a.store.CreateResolved(req, resolved, time.Now().UTC())
}
func (a *factoryAPI) Command(ctx context.Context, id string, cmd FactoryCommand) (FactoryItem, error) {
	if a.store == nil || a.controller == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(cmd.RequestID) == "" || cmd.ExpectedVersion == 0 {
		return FactoryItem{}, fmt.Errorf("%w: requestId and expectedVersion required", errFactoryInput)
	}
	// An assessment is evidence about the business outcome, never a gate on
	// it, so it is applied here: the adapter call is transport-side work that
	// has to stay outside every store update. The MCP path
	// (CommandAuthorized) has no case for this kind and answers it with a
	// conflict, which is what makes the assessment Studio-only.
	if cmd.Kind == "assess_completion" {
		return a.assessCompletion(ctx, id, cmd)
	}
	if cmd.Kind == "retry" && cmd.Replacement != nil {
		copy := *cmd.Replacement
		copy.RequestID = cmd.RequestID
		cmd.Replacement = &copy
	}
	if a.priorRequest(cmd.RequestID) {
		raw, err := json.Marshal(cmd)
		if err != nil {
			return FactoryItem{}, err
		}
		item, err := a.store.Update(id, cmd.RequestID, cmd.ExpectedVersion, raw, nil, time.Now().UTC())
		if err != nil {
			return FactoryItem{}, err
		}
		return withAssessmentFreshness(item), nil
	}
	item, err := a.store.Get(id)
	if err != nil {
		return FactoryItem{}, err
	}
	if cmd.Kind == "approve" {
		if len(item.Draft.Config.ExpectedChecks) == 0 && !item.Draft.Config.NoRequiredCIAcknowledged {
			return FactoryItem{}, fmt.Errorf("%w: select expected CI checks or acknowledge none are required", errFactoryConfig)
		}
		if err := a.preflight(ctx, item.Draft.Config, true); err != nil {
			return FactoryItem{}, err
		}
		// The settings the approval freezes are resolved here, outside the
		// journal's lock, and ride the append that writes the revision. The
		// version the reviewer displayed is checked against the current one,
		// so an approval whose settings moved after review is a conflict.
		frozen, err := a.factoryFreezeSubagents(ctx, item.Draft.Config.ProjectRoot, item.Draft.Config, cmd.SettingsVersion)
		if err != nil {
			return FactoryItem{}, err
		}
		cmd.subagents = frozen
	}
	if cmd.Kind == "retry" {
		if cmd.Replacement != nil {
			replacement := *cmd.Replacement
			replacement.RequestID = cmd.RequestID
			if err := factoryNormalize(&replacement); err != nil {
				return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
			}
			cmd.Replacement = &replacement
		}
		// The candidate is the configuration the command would be applied
		// against: the replacement's when the command carries one, the
		// latest approval's otherwise.
		candidate, named, err := factoryRouteCandidate(item, cmd)
		if err != nil {
			return FactoryItem{}, err
		}
		switch {
		case cmd.Replacement != nil:
			// A plan replacement can change the repository, the project root,
			// the target branch, the workspace and the expected CI checks,
			// so it is validated in full — against the combined
			// configuration, in which the named routes are the ones the
			// command supplied.
			if named {
				cmd.Replacement.Config = candidate
			}
			if err := a.preflight(ctx, cmd.Replacement.Config, true); err != nil {
				return FactoryItem{}, err
			}
		case named:
			// A route-only retry changes exactly two fields, so it runs the
			// route rules alone: an unrelated condition the operator cannot
			// fix from this surface must not block the one action they need.
			if err := a.factoryValidateRoutes(ctx, candidate); err != nil {
				return FactoryItem{}, err
			}
		}
		if cmd.Replacement != nil {
			// A replacement is a new approved revision, so it freezes the
			// subagent settings in force for the configuration it states.
			frozen, err := a.factoryFreezeSubagents(ctx, cmd.Replacement.Config.ProjectRoot, cmd.Replacement.Config, cmd.SettingsVersion)
			if err != nil {
				return FactoryItem{}, err
			}
			cmd.subagents = frozen
		}
	}
	if cmd.Kind == "revision_follow_up_confirm" {
		// A follow-up becomes a normal factory draft, so it passes the same
		// preflight every handoff does before the journal records its intent.
		if cmd.Revision == nil || cmd.Revision.FollowUp == nil {
			return FactoryItem{}, fmt.Errorf("%w: revision.followUp required", errFactoryInput)
		}
		candidate, err := factoryFollowUpCandidate(*cmd.Revision.FollowUp)
		if err != nil {
			return FactoryItem{}, err
		}
		if err := a.preflight(ctx, candidate.Config, true); err != nil {
			return FactoryItem{}, err
		}
	}
	applied, err := a.controller.Command(ctx, id, cmd)
	if err != nil {
		return FactoryItem{}, err
	}
	return withAssessmentFreshness(applied), nil
}
func (a *factoryAPI) preflight(ctx context.Context, c FactoryConfig, _ bool) error {
	if a.fleet == nil || a.fleet.ws == nil {
		return fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable)
	}
	repo, ok := a.fleet.ws.Repo(c.RepoID)
	if !ok {
		return fmt.Errorf("%w: repository is not registered", errFactoryConfig)
	}
	if repo.Forge != "github" || repo.URL == "" || repo.Branch == "" && c.TargetBranch == "" {
		return fmt.Errorf("%w: GitHub repository and target branch required", errFactoryConfig)
	}
	if c.ProjectRoot == "" || c.TargetBranch == "" {
		return fmt.Errorf("%w: project root and target branch required", errFactoryConfig)
	}
	found := false
	for _, root := range a.fleet.ws.Projects() {
		if root == c.ProjectRoot {
			found = true
			break
		}
	}
	if !found || projectTrust(c.ProjectRoot) == "untrusted" {
		return fmt.Errorf("%w: trusted registered project required", errFactoryConfig)
	}
	if !c.Native && c.Workspace == "" {
		return fmt.Errorf("%w: select native or a published workspace", errFactoryConfig)
	}
	if c.Workspace != "" {
		ref, err := ParseWSRef(c.Workspace)
		if err != nil {
			return fmt.Errorf("%w: invalid workspace reference", errFactoryConfig)
		}
		resolved, err := a.fleet.ResolveWorkspace(ctx, ref, c.ProjectRoot)
		if err != nil {
			return fmt.Errorf("%w: workspace unavailable: %v", errFactoryConfig, err)
		}
		if _, err = a.fleet.builtImage(resolved); err != nil {
			return fmt.Errorf("%w: workspace image unavailable: %v", errFactoryConfig, err)
		}
	}
	if err := a.factoryValidateRoutes(ctx, c); err != nil {
		return err
	}
	forge, _, err := a.fleet.forgeFor(ctx, repo)
	if err != nil {
		return fmt.Errorf("%w: GitHub credential unavailable", errFactoryConfig)
	}
	if _, err := FactoryForgeFor(forge); err != nil {
		return fmt.Errorf("%w: GitHub factory lifecycle unsupported", errFactoryConfig)
	}
	return nil
}

func (s *Server) factoryRoutes(api FactoryAPI) {
	// The planner's handoff route drafts through the factory service this
	// server serves. NewServer sets the field before calling this, and setting
	// it here keeps a server whose factory routes were installed directly
	// working too; the planner service reads it when a handoff runs.
	s.factory = api
	s.mux.HandleFunc("GET /api/factory/items", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"items": api.List()}) })
	s.mux.HandleFunc("POST /api/factory/items", func(w http.ResponseWriter, r *http.Request) {
		var body FactoryCreate
		if !factoryDecode(w, r, &body) {
			return
		}
		replay := false
		if concrete, ok := api.(*factoryAPI); ok {
			replay = concrete.priorRequest(body.RequestID)
		}
		item, err := api.Create(body)
		if err != nil {
			factoryWriteError(w, err, "", 0)
			return
		}
		status := http.StatusCreated
		if replay {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"item": item})
	})
	s.mux.HandleFunc("GET /api/factory/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := api.Get(id)
		if err != nil {
			factoryWriteError(w, err, id, 0)
			return
		}
		writeJSON(w, 200, map[string]any{"item": item})
	})
	s.mux.HandleFunc("POST /api/factory/items/{id}/commands", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var cmd FactoryCommand
		if !factoryDecode(w, r, &cmd) {
			return
		}
		item, err := api.Command(r.Context(), id, cmd)
		if err != nil {
			current := uint64(0)
			if errors.Is(err, ErrFactoryConflict) {
				if now, getErr := api.Get(id); getErr == nil {
					current = now.Version
				}
			}
			factoryWriteError(w, err, id, current)
			return
		}
		writeJSON(w, 200, map[string]any{"item": item})
	})
	// The version a review displays and an approval echoes back is composed
	// from the project's settings and the control session's reported defaults.
	// It is served from the same method an approval checks against, so a client
	// can never build a version the bridge would compare differently.
	s.mux.HandleFunc("GET /api/factory/settings-version", func(w http.ResponseWriter, r *http.Request) {
		concrete, ok := api.(*factoryAPI)
		if !ok {
			factoryWriteError(w, fmt.Errorf("%w: fleet unavailable", ErrFactoryUnavailable), "", 0)
			return
		}
		root := strings.TrimSpace(r.URL.Query().Get("projectRoot"))
		if root == "" || !filepath.IsAbs(root) {
			factoryWriteError(w, fmt.Errorf("%w: projectRoot must be an absolute path", errFactoryInput), "", 0)
			return
		}
		version, err := concrete.factorySettingsVersion(r.Context(), root)
		if err != nil {
			factoryWriteError(w, err, "", 0)
			return
		}
		writeJSON(w, 200, map[string]any{"settingsVersion": version})
	})
}
func factoryDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, factoryRequestLimit+1))
	if err != nil || len(raw) > factoryRequestLimit {
		factoryWriteError(w, fmt.Errorf("%w: body exceeds 2 MiB", errFactoryInput), "", 0)
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		factoryWriteError(w, fmt.Errorf("%w: malformed JSON", errFactoryInput), "", 0)
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		factoryWriteError(w, fmt.Errorf("%w: trailing JSON", errFactoryInput), "", 0)
		return false
	}
	return true
}
func factoryWriteError(w http.ResponseWriter, err error, id string, expected uint64) {
	status, code := 400, "invalid_input"
	switch {
	case errors.Is(err, ErrFactoryNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, ErrFactoryConflict):
		status, code = 409, "conflict"
	case errors.Is(err, errFactoryConfig), errors.Is(err, ErrFactoryForgeUnsupported):
		status, code = 422, "unsupported"
	case errors.Is(err, ErrFactoryUnavailable), errors.Is(err, ErrFactoryLocked), errors.Is(err, ErrFactoryCorrupt):
		status, code = 503, "unavailable"
	}
	body := map[string]any{"error": err.Error(), "code": code}
	if id != "" {
		body["itemId"] = id
	}
	if status == 409 && expected != 0 {
		body["currentVersion"] = expected
	}
	writeJSON(w, status, body)
}
