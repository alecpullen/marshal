package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
		templateRoot, trustRoot := f.workspaceRoots(a)
		path = filepath.Join(".marshal", "workspaces", a.Workspace.Name+".toml")
		src, err = readRepoTemplate(templateRoot, trustRoot, path)
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
		// The draft is replaced only if it is still the one that was read.
		if err := f.templates.SaveDraftIf(a.Workspace.Name, src, []byte(res.Source)); err != nil {
			return nil, err
		}
	}
	return f.addToWorkspaceResult(a, path, src, []byte(res.Source)), nil
}

// maxRepoTemplateBytes caps a repo template the bridge will read: the
// agent controls the file, and it is parsed and diffed in memory.
const maxRepoTemplateBytes = 256 << 10

// readRepoTemplate reads a repo workspace template from an agent's
// checkout. It applies the same gate as ResolveWorkspaceIn (the project
// must be trusted), then reads only a regular file inside the checkout,
// never through a symlink, and no larger than maxRepoTemplateBytes.
func readRepoTemplate(templateRoot, trustRoot, rel string) ([]byte, error) {
	if trustRoot == "" || projectTrust(trustRoot) != "trusted" {
		return nil, ErrUntrustedRepoTemplate
	}
	// Every component under the checkout must be a real directory or the
	// file itself: a symlink anywhere on the way could point off the host.
	cur := templateRoot
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: repo template %s", ErrTemplateNotFound, rel)
			}
			return nil, err
		}
		last := i == len(parts)-1
		if fi.Mode()&os.ModeSymlink != 0 || (last && !fi.Mode().IsRegular()) || (!last && !fi.IsDir()) {
			return nil, fmt.Errorf("%w: repo template %s is not a regular file", ErrWorkspaceInvalid, rel)
		}
		if last && fi.Size() > maxRepoTemplateBytes {
			return nil, fmt.Errorf("%w: repo template %s is over %d KiB", ErrWorkspaceInvalid, rel, maxRepoTemplateBytes>>10)
		}
	}
	file, err := os.Open(cur)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRepoTemplateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRepoTemplateBytes {
		return nil, fmt.Errorf("%w: repo template %s is over %d KiB", ErrWorkspaceInvalid, rel, maxRepoTemplateBytes>>10)
	}
	return data, nil
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
