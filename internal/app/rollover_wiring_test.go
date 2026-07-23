package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/rollover"
)

func TestRolloverPolicyFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{
			name:   "empty defaults to context_percent",
			policy: "",
			want:   rollover.PolicyContextPercent,
		},
		{
			name:   "auto defaults to context_percent",
			policy: "auto",
			want:   rollover.PolicyContextPercent,
		},
		{
			name:   "context_percent passed through",
			policy: "context_percent",
			want:   rollover.PolicyContextPercent,
		},
		{
			name:   "turn_count passed through",
			policy: "turn_count",
			want:   rollover.PolicyTurnCount,
		},
		{
			name:   "unknown policy passed through unchanged",
			policy: "custom_policy",
			want:   "custom_policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.RolloverConfig{Policy: tt.policy}
			got := rolloverPolicyFromConfig(cfg)
			if got.Mode != tt.want {
				t.Errorf("rolloverPolicyFromConfig(%q).Mode = %q, want %q", tt.policy, got.Mode, tt.want)
			}
		})
	}
}

func TestRolloverPolicyFromConfig_CopiesThresholds(t *testing.T) {
	cfg := config.RolloverConfig{
		Policy:                  "context_percent",
		ContextPercentThreshold: 70,
		TurnCountThreshold:      40,
	}
	got := rolloverPolicyFromConfig(cfg)
	if got.ContextPercent != 70 {
		t.Errorf("ContextPercent = %d, want 70", got.ContextPercent)
	}
	if got.TurnCount != 40 {
		t.Errorf("TurnCount = %d, want 40", got.TurnCount)
	}
}

func TestRolloverPolicyFromConfig_DefaultsUseThresholds(t *testing.T) {
	cfg := config.RolloverConfig{
		Policy:                  "",
		ContextPercentThreshold: 85,
		TurnCountThreshold:      50,
	}
	got := rolloverPolicyFromConfig(cfg)
	if got.Mode != rollover.PolicyContextPercent {
		t.Errorf("Mode = %q, want %q", got.Mode, rollover.PolicyContextPercent)
	}
	if got.ContextPercent != 85 {
		t.Errorf("ContextPercent = %d, want 85", got.ContextPercent)
	}
	if got.TurnCount != 50 {
		t.Errorf("TurnCount = %d, want 50", got.TurnCount)
	}
}

func TestNewRolloverController_Disabled(t *testing.T) {
	cfg := config.RolloverConfig{Enabled: false}
	ctrl, err := NewRolloverController("sess_test", cfg, nil, 0)
	if err != nil {
		t.Fatalf("NewRolloverController disabled: %v", err)
	}
	if ctrl != nil {
		t.Fatal("NewRolloverController with disabled config should return nil")
	}
}

func TestNewRolloverController_Enabled(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}

	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	// Create a project and session so the generation can reference them.
	projectID, err := database.GetOrCreateProject(tmp, "test-project")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_test", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := config.RolloverConfig{
		Enabled: true,
		Policy:  "context_percent",
	}
	ctrl, err := NewRolloverController("sess_test", cfg, database, 0)
	if err != nil {
		t.Fatalf("NewRolloverController enabled: %v", err)
	}
	if ctrl == nil {
		t.Fatal("NewRolloverController with enabled config should return non-nil")
	}
	if ctrl.SessionID != "sess_test" {
		t.Errorf("ctrl.SessionID = %q, want %q", ctrl.SessionID, "sess_test")
	}
	if ctrl.Store == nil {
		t.Error("ctrl.Store is nil, expected database")
	}
	if ctrl.Counter == nil {
		t.Error("ctrl.Counter is nil, expected a TokenCounter")
	}
	if ctrl.Digest == nil {
		t.Error("ctrl.Digest is nil, expected a DigestProvider")
	}
}

func TestNewRolloverController_Enabled_SetsModelContextWindow(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}

	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	projectID, err := database.GetOrCreateProject(tmp, "test-project")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_test", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := config.RolloverConfig{
		Enabled: true,
		Policy:  "context_percent",
	}
	// Pass a non-zero model context window and verify it's set on the controller.
	ctrl, err := NewRolloverController("sess_test", cfg, database, 128000)
	if err != nil {
		t.Fatalf("NewRolloverController: %v", err)
	}
	if ctrl == nil {
		t.Fatal("expected non-nil controller")
	}
	if ctrl.ModelContextWindow != 128000 {
		t.Errorf("ctrl.ModelContextWindow = %d, want %d", ctrl.ModelContextWindow, 128000)
	}
}

func TestNewRolloverController_StartsGenerationZero(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}

	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	projectID, err := database.GetOrCreateProject(tmp, "test-project")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_test", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := config.RolloverConfig{
		Enabled: true,
		Policy:  "context_percent",
	}
	ctrl, err := NewRolloverController("sess_test", cfg, database, 0)
	if err != nil {
		t.Fatalf("NewRolloverController: %v", err)
	}
	if ctrl == nil {
		t.Fatal("expected non-nil controller")
	}

	// Start generation 0.
	ctx := context.Background()
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("ctrl.Start: %v", err)
	}

	genID, genSeq, genSeed := ctrl.Current()
	if genID == "" {
		t.Error("generation ID is empty after Start")
	}
	if genSeq != 0 {
		t.Errorf("generation seq = %d, want 0", genSeq)
	}
	if genSeed != "" {
		t.Errorf("generation seed digest = %q, want empty for gen 0", genSeed)
	}

	// Verify the generation was persisted.
	generations, err := database.GenerationsForSession("sess_test")
	if err != nil {
		t.Fatalf("GenerationsForSession: %v", err)
	}
	if len(generations) != 1 {
		t.Fatalf("expected 1 generation, got %d", len(generations))
	}
	if generations[0].Seq != 0 {
		t.Errorf("generation seq = %d, want 0", generations[0].Seq)
	}
	if generations[0].ID != genID {
		t.Errorf("generation ID = %q, want %q", generations[0].ID, genID)
	}
}

func TestNewRolloverController_DisabledNoGenerationRows(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}

	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	projectID, err := database.GetOrCreateProject(tmp, "test-project")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_test", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Disabled config: controller is nil, no generation rows created.
	cfg := config.RolloverConfig{Enabled: false}
	ctrl, err := NewRolloverController("sess_test", cfg, database, 0)
	if err != nil {
		t.Fatalf("NewRolloverController disabled: %v", err)
	}
	if ctrl != nil {
		t.Fatal("expected nil controller when disabled")
	}

	// Verify no generation rows exist.
	generations, err := database.GenerationsForSession("sess_test")
	if err != nil {
		t.Fatalf("GenerationsForSession: %v", err)
	}
	if len(generations) != 0 {
		t.Fatalf("expected 0 generations when disabled, got %d", len(generations))
	}
}

func TestBuildAgentRunnerWiresRollover(t *testing.T) {
	ctx := context.Background()
	cfg := rolloverTestConfig()
	cfg.Session.Rollover.Enabled = true
	cfg.Session.Rollover.Policy = "context_percent"

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_test", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	state := newTestSessionState(t, cfg)
	// Override the session ID to match the DB session.
	// We need to set it via Persistence since session.New copies it.
	state = session.New(cfg, tmp, time.Unix(100, 0), session.Persistence{DB: database, SessionID: "sess_test"})
	runner, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, database, projectID, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Rollover == nil {
		t.Fatal("runner.Rollover is nil, expected non-nil when rollover is enabled")
	}
	if runner.Rollover.Controller == nil {
		t.Fatal("runner.Rollover.Controller is nil")
	}
	if runner.Rollover.State != state {
		t.Fatal("runner.Rollover.State does not match session state")
	}
}

func TestBuildAgentRunnerSkipsRolloverWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfg := rolloverTestConfig()
	cfg.Session.Rollover.Enabled = false

	state := newTestSessionState(t, cfg)
	runner, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Rollover != nil {
		t.Fatal("runner.Rollover should be nil when rollover is disabled")
	}
}

// newTestSessionState creates a minimal session.State for testing buildAgentRunner.
func newTestSessionState(t *testing.T, cfg config.Config) *session.State {
	t.Helper()
	return session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

// rolloverTestConfig returns a config suitable for buildAgentRunner tests.
// It sets up a mock provider and profile so routing succeeds.
func rolloverTestConfig() config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"test-provider": {
			Type:        "openai_compatible",
			BaseURL:     "http://localhost:11434/v1",
			APIKey:      "test-key",
			ToolCalling: true,
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"test-preset": {
			Name:      "test-preset",
			Provider:  "test-provider",
			Model:     "test-model",
			LocalOnly: true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "test-preset",
				routing.RolePlanner:     "test-preset",
				routing.RoleRepoScout:   "test-preset",
				routing.RoleTester:      "test-preset",
				routing.RoleReviewer:    "test-preset",
			},
		},
	}
	return cfg
}
