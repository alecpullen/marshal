package acp

import (
	"context"
	"encoding/json"

	"marshal/internal/workspacecfg"
)

// WorkspaceFiles serves the workspace/* methods: parse, patch and format
// workspace files. Like config/*, they are stateless and need no session.
type WorkspaceFiles struct{}

// WorkspaceParseParams is the body of workspace/parse.
type WorkspaceParseParams struct {
	Source string `json:"source"`
}

// WorkspacePatchParams is the body of workspace/patch.
type WorkspacePatchParams struct {
	Source string          `json:"source"`
	Layer  int             `json:"layer"`
	Value  json.RawMessage `json:"value"`
}

// WorkspaceFormatParams is the body of workspace/format.
type WorkspaceFormatParams struct {
	Doc workspacecfg.Doc `json:"doc"`
}

// WorkspaceParseResult is the result of workspace/parse.
type WorkspaceParseResult struct {
	Doc         workspacecfg.Doc          `json:"doc"`
	Sections    []workspacecfg.Section    `json:"sections"`
	Diagnostics []workspacecfg.Diagnostic `json:"diagnostics"`
}

// WorkspacePatchResult is the result of workspace/patch.
type WorkspacePatchResult struct {
	Source      string                    `json:"source"`
	Doc         workspacecfg.Doc          `json:"doc"`
	Sections    []workspacecfg.Section    `json:"sections"`
	Diagnostics []workspacecfg.Diagnostic `json:"diagnostics"`
}

// Parse handles workspace/parse.
func (WorkspaceFiles) Parse(_ context.Context, params json.RawMessage) (any, error) {
	var p WorkspaceParseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidParamsError("parse workspace/parse params: %v", err)
	}
	doc, sections, diags := workspacecfg.Parse([]byte(p.Source))
	return WorkspaceParseResult{Doc: doc, Sections: nonNilSections(sections), Diagnostics: diags}, nil
}

// Patch handles workspace/patch. A patch that cannot be applied is an
// invalid-params error.
func (WorkspaceFiles) Patch(_ context.Context, params json.RawMessage) (any, error) {
	var p WorkspacePatchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidParamsError("parse workspace/patch params: %v", err)
	}
	if len(p.Value) == 0 {
		return nil, invalidParamsError("workspace/patch requires value")
	}
	out, err := workspacecfg.Patch([]byte(p.Source), p.Layer, p.Value)
	if err != nil {
		return nil, invalidParamsError("%v", err)
	}
	doc, sections, diags := workspacecfg.Parse(out)
	return WorkspacePatchResult{Source: string(out), Doc: doc, Sections: nonNilSections(sections), Diagnostics: diags}, nil
}

// Format handles workspace/format.
func (WorkspaceFiles) Format(_ context.Context, params json.RawMessage) (any, error) {
	var p WorkspaceFormatParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, invalidParamsError("parse workspace/format params: %v", err)
	}
	return map[string]any{"source": string(workspacecfg.Format(p.Doc))}, nil
}

func nonNilSections(s []workspacecfg.Section) []workspacecfg.Section {
	if s == nil {
		return []workspacecfg.Section{}
	}
	return s
}
