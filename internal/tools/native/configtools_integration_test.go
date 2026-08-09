package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestConfigIntegrationProjectThenGlobal(t *testing.T) {
	dir := t.TempDir()
	projectPath := config.ProjectConfigPath(dir)
	globalPath := config.UserConfigPath(dir)

	cfg := config.Default()

	state := session.New(cfg, dir, time.Now(), session.Persistence{})
	approvalRequested := false
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := state.PendingApproval(); p != nil {
				approvalRequested = true
				p.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var reloaded *config.Config
	ts := toolSet{
		config:         cfg,
		configPath:     projectPath,
		userConfigPath: globalPath,
		configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil },
		sessionState:   state,
	}
	reg := registry.New()
	for _, tool := range ts.configTools() {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}

	// 1. Project: set an agent scalar.
	agentTool, _ := reg.Lookup("config.agent.set")
	if _, err := agentTool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.agent.set", Args: json.RawMessage(`{"max_tool_iterations":30}`)}); err != nil {
		t.Fatalf("agent set: %v", err)
	}
	if reloaded == nil || reloaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("agent scalar not reloaded: %+v", reloaded)
	}

	// 2. Global: enable web search (forces approval).
	webTool, _ := reg.Lookup("config.web.set")
	if _, err := webTool.Handler(context.Background(), registry.ToolCall{ID: "2", Name: "config.web.set", Args: json.RawMessage(`{"scope":"global","enabled":true}`)}); err != nil {
		t.Fatalf("web set: %v", err)
	}
	if !reloaded.Web.Enabled {
		t.Fatal("web.enabled not reloaded after global write")
	}
	if !approvalRequested {
		t.Fatal("global write must have requested approval")
	}

	// 3. Disk reflects both: project file has the agent scalar, global file
	// has web.
	loaded, err := config.Load(config.LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("project disk agent scalar missing: %+v", loaded.Agent)
	}
	if !loaded.Web.Enabled {
		t.Fatalf("global disk web.enabled = %v", loaded.Web.Enabled)
	}

	// 4. config.read reflects merged state with secrets masked.
	// Use capitalized section names to match Go struct field names (no json tags).
	readTool, _ := reg.Lookup("config.read")
	res, err := readTool.Handler(context.Background(), registry.ToolCall{ID: "3", Name: "config.read", Args: json.RawMessage(`{"sections":["Agent","Web"]}`)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !strings.Contains(res.Content, `"MaxToolIterations": 30`) {
		t.Fatalf("read missing agent scalar: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"Enabled": true`) {
		t.Fatalf("read missing web.Enabled: %s", res.Content)
	}
}
