package config

import "testing"

func TestDefaultWatchResumeEnabled(t *testing.T) {
	if !Default().Watch.ResumeEnabled {
		t.Fatal("default [watch] resume_enabled = false, want true")
	}
}

func TestMergeWatchResumeExplicitFalse(t *testing.T) {
	cfg := Default()
	off := false
	if err := merge(&cfg, configFile{Watch: &fileWatch{ResumeEnabled: &off}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.Watch.ResumeEnabled {
		t.Fatal("explicit resume_enabled=false did not survive merge")
	}
}

func TestMergeWatchUnsetKeepsDefault(t *testing.T) {
	cfg := Default()
	if err := merge(&cfg, configFile{Session: &fileSession{}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !cfg.Watch.ResumeEnabled {
		t.Fatal("absent [watch] stanza reset resume_enabled to false")
	}
}

func TestWatchSurvivesSave(t *testing.T) {
	cfg := Default()
	cfg.Watch.ResumeEnabled = false
	var file configFile
	writeSections(&file, cfg, Default())
	if file.Watch == nil || file.Watch.ResumeEnabled == nil || *file.Watch.ResumeEnabled {
		t.Fatalf("writeSections dropped [watch] resume_enabled: %+v", file.Watch)
	}
}
