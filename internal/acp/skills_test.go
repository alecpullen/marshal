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

func TestSkillsInstallPreviewParsesStagedMetadataWithoutInstalling(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	srcDir := t.TempDir()
	source := writeTestSkillFile(t, srcDir, "previewed-skill")

	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})

	raw, _ := json.Marshal(SkillsInstallPreviewParams{SessionID: "sess_1", Source: source})
	res, err := mgr.SkillsInstallPreview(context.Background(), raw)
	if err != nil {
		t.Fatalf("SkillsInstallPreview: %v", err)
	}
	result, ok := res.(SkillsInstallPreviewResult)
	if !ok {
		t.Fatalf("SkillsInstallPreview result type = %T, want SkillsInstallPreviewResult", res)
	}
	if result.StagingToken == "" || result.Name != "previewed-skill" || result.Description != "a test skill" {
		t.Fatalf("SkillsInstallPreview result = %+v, unexpected", result)
	}

	// Nothing installed for real yet.
	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	if _, err := os.Stat(filepath.Join(globalDir, "previewed-skill.md")); !os.IsNotExist(err) {
		t.Fatalf("preview installed into the real scope dir (stat err = %v), want no-op", err)
	}
}

func TestSkillsInstallPreviewRejectsEmptySource(t *testing.T) {
	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(SkillsInstallPreviewParams{SessionID: "sess_1", Source: "  "})
	_, err := mgr.SkillsInstallPreview(context.Background(), raw)
	if err == nil {
		t.Fatal("SkillsInstallPreview with blank source: got nil error, want an error")
	}
}

func TestSkillsInstallConfirmWritesToScopeDir(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	srcDir := t.TempDir()
	source := writeTestSkillFile(t, srcDir, "confirmed-skill")

	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})

	previewRaw, _ := json.Marshal(SkillsInstallPreviewParams{SessionID: "sess_1", Source: source})
	previewRes, err := mgr.SkillsInstallPreview(context.Background(), previewRaw)
	if err != nil {
		t.Fatalf("SkillsInstallPreview: %v", err)
	}
	token := previewRes.(SkillsInstallPreviewResult).StagingToken
	tempDir := mgr.staging["sess_1"][token].tempDir

	confirmRaw, _ := json.Marshal(SkillsInstallConfirmParams{SessionID: "sess_1", StagingToken: token, Scope: "global"})
	confirmRes, err := mgr.SkillsInstallConfirm(context.Background(), confirmRaw)
	if err != nil {
		t.Fatalf("SkillsInstallConfirm: %v", err)
	}
	if confirmRes.(SkillsInstallResult).Name != "confirmed-skill" {
		t.Fatalf("SkillsInstallConfirm result = %+v, want Name=confirmed-skill", confirmRes)
	}

	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	data, err := os.ReadFile(filepath.Join(globalDir, "confirmed-skill.md"))
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("installed skill file is empty")
	}

	// Temp dir and staging entry are both gone.
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after confirm", tempDir)
	}
	if _, exists := mgr.staging["sess_1"][token]; exists {
		t.Fatal("staging entry still present after confirm")
	}
}

func TestSkillsInstallConfirmRejectsUnknownToken(t *testing.T) {
	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}, true
		},
	})
	raw, _ := json.Marshal(SkillsInstallConfirmParams{SessionID: "sess_1", StagingToken: "stg_nonexistent", Scope: "global"})
	_, err := mgr.SkillsInstallConfirm(context.Background(), raw)
	if err == nil {
		t.Fatal("SkillsInstallConfirm with unknown token: got nil error, want an error")
	}
}

func TestSkillsInstallConfirmRejectsInvalidScope(t *testing.T) {
	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) { return nil, false },
	})
	raw, _ := json.Marshal(SkillsInstallConfirmParams{SessionID: "sess_1", StagingToken: "stg_x", Scope: "nowhere"})
	_, err := mgr.SkillsInstallConfirm(context.Background(), raw)
	if err == nil {
		t.Fatal("SkillsInstallConfirm with invalid scope: got nil error, want an error")
	}
}

func TestSkillsInstallDiscardRemovesStagingWithoutInstalling(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	srcDir := t.TempDir()
	source := writeTestSkillFile(t, srcDir, "discarded-skill")

	mgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})
	previewRaw, _ := json.Marshal(SkillsInstallPreviewParams{SessionID: "sess_1", Source: source})
	previewRes, err := mgr.SkillsInstallPreview(context.Background(), previewRaw)
	if err != nil {
		t.Fatalf("SkillsInstallPreview: %v", err)
	}
	token := previewRes.(SkillsInstallPreviewResult).StagingToken
	tempDir := mgr.staging["sess_1"][token].tempDir

	discardRaw, _ := json.Marshal(SkillsInstallDiscardParams{SessionID: "sess_1", StagingToken: token})
	if _, err := mgr.SkillsInstallDiscard(context.Background(), discardRaw); err != nil {
		t.Fatalf("SkillsInstallDiscard: %v", err)
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists after discard", tempDir)
	}
	if _, exists := mgr.staging["sess_1"][token]; exists {
		t.Fatal("staging entry still present after discard")
	}

	// Confirming a discarded token fails.
	confirmRaw, _ := json.Marshal(SkillsInstallConfirmParams{SessionID: "sess_1", StagingToken: token, Scope: "global"})
	if _, err := mgr.SkillsInstallConfirm(context.Background(), confirmRaw); err == nil {
		t.Fatal("SkillsInstallConfirm after discard: got nil error, want an error")
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
