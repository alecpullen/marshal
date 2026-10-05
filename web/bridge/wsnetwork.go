package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// networkLayer is the workspace layer that holds [network].
const networkLayer = 7

// ErrAgentNoWorkspace is returned by add-to-workspace for an agent that
// runs without a workspace: there is no template to change.
var ErrAgentNoWorkspace = errors.New("agent has no workspace")

// addAgentHostToWorkspace is Fleet.addToWorkspace. It appends host to the
// agent's workspace [network].egress through the control agent's
// workspace/patch. A Studio template's draft is rewritten (the change
// takes effect from its next published version) and the result names the
// workspace and carries the new source. A repo template is never written:
// the result carries a unified diff, in `patch`, for the user to apply.
func (f *Fleet) addAgentHostToWorkspace(r *http.Request, a Agent, host string) (any, error) {
	if a.Workspace == nil {
		return nil, ErrAgentNoWorkspace
	}
	ctx := r.Context()
	var (
		src  []byte
		path string
		err  error
	)
	switch a.Workspace.Source {
	case "repo":
		templateRoot, _ := f.workspaceRoots(a)
		path = filepath.Join(".marshal", "workspaces", a.Workspace.Name+".toml")
		src, err = os.ReadFile(filepath.Join(templateRoot, path))
	default:
		src, err = f.templates.Read(a.Workspace.Name, 0)
	}
	if err != nil {
		return nil, err
	}
	doc, _, _, err := f.parseWorkspace(ctx, src)
	if err != nil {
		return nil, err
	}
	for _, h := range doc.Network.Egress {
		if normalizeHost(h) == host {
			return f.addToWorkspaceResult(a, path, src, src), nil
		}
	}
	net := doc.Network
	net.Egress = append(append([]string(nil), net.Egress...), host)
	value, err := json.Marshal(net)
	if err != nil {
		return nil, err
	}
	raw, err := f.controlCall(ctx, "workspace/patch", map[string]any{"source": string(src), "layer": networkLayer, "value": json.RawMessage(value)}, false)
	if err != nil {
		return nil, err
	}
	var res struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("bridge: decode workspace/patch: %w", err)
	}
	if a.Workspace.Source != "repo" {
		if err := f.templates.SaveDraft(a.Workspace.Name, []byte(res.Source)); err != nil {
			return nil, err
		}
	}
	return f.addToWorkspaceResult(a, path, src, []byte(res.Source)), nil
}

func (f *Fleet) addToWorkspaceResult(a Agent, path string, before, after []byte) map[string]any {
	out := map[string]any{"ok": true, "workspace": a.Workspace.Name}
	if a.Workspace.Source == "repo" {
		out["patch"] = unifiedDiff(string(before), string(after), "a/"+path, "b/"+path)
		return out
	}
	out["source"] = string(after)
	return out
}
