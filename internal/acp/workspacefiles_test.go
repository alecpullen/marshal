package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/workspacecfg"
)

const wsSource = "[workspace]\nname = \"x\"\n\n[network]\nmode = \"off\"\n"

func callWS(t *testing.T, fn func(context.Context, json.RawMessage) (any, error), params any) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := fn(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func TestWorkspaceFilesParse(t *testing.T) {
	var w WorkspaceFiles
	out, err := callWS(t, w.Parse, map[string]any{"source": wsSource + "bogus = 1\n"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Doc         workspacecfg.Doc
		Sections    []workspacecfg.Section
		Diagnostics []workspacecfg.Diagnostic
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.Doc.Workspace.Name != "x" || res.Doc.Network.Mode != "off" || len(res.Sections) < 3 {
		t.Fatalf("result = %s", out)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Severity != "warning" {
		t.Fatalf("diagnostics = %+v", res.Diagnostics)
	}
	for _, key := range []string{`"doc"`, `"sections"`, `"diagnostics"`, `"startLine"`, `"secretsEnv"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("result missing %s: %s", key, out)
		}
	}
}

func TestWorkspaceFilesPatch(t *testing.T) {
	var w WorkspaceFiles
	out, err := callWS(t, w.Patch, map[string]any{
		"source": wsSource, "layer": 3, "value": map[string]any{"apt": []string{"git"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Source      string
		Doc         workspacecfg.Doc
		Diagnostics []workspacecfg.Diagnostic
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Doc.Packages.Apt) != 1 || !strings.Contains(res.Source, "[packages]") || len(res.Diagnostics) != 0 {
		t.Fatalf("result = %s", out)
	}
}

func TestWorkspaceFilesPatchErrorsAreInvalidParams(t *testing.T) {
	var w WorkspaceFiles
	for name, params := range map[string]map[string]any{
		"bad layer":  {"source": wsSource, "layer": 99, "value": map[string]any{}},
		"bad source": {"source": "[net", "layer": 3, "value": map[string]any{}},
		"no value":   {"source": wsSource, "layer": 3},
	} {
		_, err := callWS(t, w.Patch, params)
		var rpcErr *jsonRPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
			t.Errorf("%s: err = %v, want invalid params", name, err)
		}
	}
}

func TestWorkspaceFilesFormat(t *testing.T) {
	var w WorkspaceFiles
	doc, _, _ := workspacecfg.Parse([]byte(wsSource))
	out, err := callWS(t, w.Format, map[string]any{"doc": doc})
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Source string }
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	got, _, diags := workspacecfg.Parse([]byte(res.Source))
	if len(diags) != 0 || got.Workspace.Name != "x" || got.Network.Mode != "off" {
		t.Fatalf("source = %q diags = %+v", res.Source, diags)
	}
}
