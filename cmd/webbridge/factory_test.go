package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"marshal/web/bridge"
)

func TestFactoryStartupOrdering(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	owned, err := bridge.OpenFactoryStore(filepath.Join(state, "factory", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	err = run(context.Background(), []string{"--token", "test-token", "--workspace", filepath.Join(root, "workspace.json"), "--state-dir", state, "--cwd-root", root, "--egress", "off", "--preview-addr", "off"}, io.Discard, io.Discard)
	if !errors.Is(err, bridge.ErrFactoryLocked) {
		t.Fatalf("startup must refuse owned journal before starting work: %v", err)
	}
	if !strings.Contains(err.Error(), "open factory") {
		t.Fatalf("missing startup phase: %v", err)
	}
}
