package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FactoryAPI keeps HTTP transport separate from the durable controller.
type FactoryAPI interface {
	Create(FactoryCreate) (FactoryItem, error)
	Get(string) (FactoryItem, error)
	List() []FactoryItem
	Command(context.Context, string, FactoryCommand) (FactoryItem, error)
}
type factoryAPI struct {
	store      *FactoryStore
	controller *FactoryController
	fleet      *Fleet
}

var errFactoryConfig = errors.New("factory configuration or capability unsupported")
var errFactoryInput = errors.New("invalid factory input")

func NewFactoryAPI(store *FactoryStore, controller *FactoryController, fleet *Fleet) FactoryAPI {
	return &factoryAPI{store, controller, fleet}
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
	return a.store.Get(id)
}
func (a *factoryAPI) List() []FactoryItem {
	if a.store == nil {
		return []FactoryItem{}
	}
	return a.store.List()
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
func (a *factoryAPI) Command(ctx context.Context, id string, cmd FactoryCommand) (FactoryItem, error) {
	if a.store == nil || a.controller == nil {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(cmd.RequestID) == "" || cmd.ExpectedVersion == 0 {
		return FactoryItem{}, fmt.Errorf("%w: requestId and expectedVersion required", errFactoryInput)
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
		return a.store.Update(id, cmd.RequestID, cmd.ExpectedVersion, raw, nil, time.Now().UTC())
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
	}
	if cmd.Kind == "retry" && cmd.Replacement != nil {
		replacement := *cmd.Replacement
		replacement.RequestID = cmd.RequestID
		if err := factoryNormalize(&replacement); err != nil {
			return FactoryItem{}, fmt.Errorf("%w: %v", errFactoryInput, err)
		}
		if err := a.preflight(ctx, replacement.Config, true); err != nil {
			return FactoryItem{}, err
		}
		cmd.Replacement = &replacement
	}
	return a.controller.Command(ctx, id, cmd)
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
	raw, err := a.fleet.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return fmt.Errorf("%w: model configuration unavailable", ErrFactoryUnavailable)
	}
	var modelConfig struct {
		Providers map[string]struct {
			HasKey    bool   `json:"hasKey"`
			Type      string `json:"type"`
			APIKeyEnv string `json:"apiKeyEnv"`
		} `json:"providers"`
		Presets map[string]struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"presets"`
		Profiles map[string]map[string]struct {
			Preset string `json:"preset"`
		} `json:"profiles"`
		DefaultProfile string `json:"defaultProfile"`
	}
	if json.Unmarshal(raw, &modelConfig) != nil {
		return fmt.Errorf("%w: invalid model configuration", ErrFactoryUnavailable)
	}
	for n, route := range []json.RawMessage{c.ImplementerRouting, c.ReviewerRouting} {
		role := "implementer"
		if n == 1 {
			role = "reviewer"
		}
		var selection struct {
			Profile   string            `json:"profile"`
			Overrides map[string]string `json:"overrides"`
		}
		if len(route) == 0 || json.Unmarshal(route, &selection) != nil {
			return fmt.Errorf("%w: explicit %s routing required", errFactoryConfig, role)
		}
		if selection.Profile == "" {
			return fmt.Errorf("%w: explicit routing profile required", errFactoryConfig)
		}
		profile := selection.Profile
		if profile == "" {
			profile = modelConfig.DefaultProfile
		}
		bindings, ok := modelConfig.Profiles[profile]
		if !ok {
			return fmt.Errorf("%w: unknown routing profile", errFactoryConfig)
		}
		preset := selection.Overrides[role]
		if preset == "" {
			preset = bindings[role].Preset
		}
		p, ok := modelConfig.Presets[preset]
		if !ok || p.Model == "" || p.Provider == "" {
			return fmt.Errorf("%w: %s model preset unavailable", errFactoryConfig, role)
		}
		provider, ok := modelConfig.Providers[p.Provider]
		if !ok || provider.Type == "" {
			return fmt.Errorf("%w: %s provider unavailable", errFactoryConfig, role)
		}
		if provider.Type != "openai_codex" && (provider.Type == "anthropic" || provider.APIKeyEnv != "") && !provider.HasKey {
			return fmt.Errorf("%w: %s provider credential unavailable", errFactoryConfig, role)
		}
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
