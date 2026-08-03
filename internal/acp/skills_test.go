package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTestSkillFile(t *testing.T, dir, name string) string {
	t.Helper()
	content := `+++
name = "` + name + `"
description = "a test skill"
risk = "read_only"
+++

# ` + name + `

Body content.
`
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test skill file: %v", err)
	}
	return path
}

func TestSkillsListReturnsScopedSkills(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestSkillFile(t, globalDir, "my-skill")

	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})

	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := mgr.SkillsList(context.Background(), raw)
	if err != nil {
		t.Fatalf("SkillsList: %v", err)
	}
	result, ok := res.(SkillsListResult)
	if !ok {
		t.Fatalf("SkillsList result type = %T, want SkillsListResult", res)
	}
	if len(result.Skills) != 1 || result.Skills[0].Name != "my-skill" || result.Skills[0].Scope != "global" {
		t.Fatalf("SkillsList = %+v, unexpected", result.Skills)
	}
}

func TestSkillsListRequiresSessionID(t *testing.T) {
	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) { return nil, false },
	})
	_, err := mgr.SkillsList(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("SkillsList with no sessionId: got nil error, want an error")
	}
}

func TestSkillsRemoveDeletesFromDisk(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTestSkillFile(t, globalDir, "to-remove")

	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})
	raw, _ := json.Marshal(SkillsRemoveParams{SessionID: "sess_1", Name: "to-remove", Scope: "global"})
	if _, err := mgr.SkillsRemove(context.Background(), raw); err != nil {
		t.Fatalf("SkillsRemove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalDir, "to-remove.md")); !os.IsNotExist(err) {
		t.Fatalf("to-remove.md still exists after SkillsRemove (stat err = %v)", err)
	}
}

func TestSkillsRemoveRejectsInvalidScope(t *testing.T) {
	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(SkillsRemoveParams{SessionID: "sess_1", Name: "x", Scope: "everywhere"})
	_, err := mgr.SkillsRemove(context.Background(), raw)
	if err == nil {
		t.Fatal("SkillsRemove with invalid scope: got nil error, want an error")
	}
}
