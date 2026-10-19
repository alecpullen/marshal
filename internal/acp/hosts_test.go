package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/trust"
)

// sharedHostProject writes a project whose config runs a stub language
// server for shell scripts, so its sessions have a manager to share. The stub
// exits at once; the manager is pooled all the same.
func sharedHostProject(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := writeMarshalConfig(t, tmp); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(tmp, "lsp-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(tmp, ".marshal", "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[lsp]\nenabled = true\n[lsp.servers.shell]\ncommand = %q\n", stub); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func TestSessionsOnOneProjectShareTheHostAndLanguageServers(t *testing.T) {
	tmp := sharedHostProject(t)
	ctx := context.Background()
	hosts := app.NewHosts()
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = hosts.Close(closeCtx)
	})
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
		CloseRuntime: func(ctx context.Context, rt *app.Runtime) error { return rt.Close(ctx) },
		Notify:       func(method string, params any) error { return nil },
		Options: []app.Option{
			app.WithHomeDir(t.TempDir()),
			app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			app.WithHosts(hosts),
		},
	})
	m.SetTurnCanceller(noopCancel())
	t.Cleanup(func() { _ = m.CloseAll(ctx) })

	create := func() *app.Runtime {
		t.Helper()
		res, err := m.Create(ctx, json.RawMessage(`{"cwd":"`+tmp+`","mcpServers":[]}`))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		rt, ok := m.Get(res.(SessionResponse).SessionID)
		if !ok {
			t.Fatal("created session is not registered")
		}
		return rt
	}
	a, b := create(), create()
	if a.SessionID == b.SessionID {
		t.Fatal("two session/new calls returned one session")
	}
	if a.Host == nil || a.Host != b.Host {
		t.Fatal("two sessions on one project got different project hosts")
	}
	if a.LSPManager.Get() == nil || a.LSPManager.Get() != b.LSPManager.Get() {
		t.Fatalf("two sessions on one project got managers %p and %p; want one shared manager", a.LSPManager.Get(), b.LSPManager.Get())
	}
}
