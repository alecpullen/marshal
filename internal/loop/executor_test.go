package loop

import (
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/config"
)

func TestExecutor_SystemPromptContainsSecurity(t *testing.T) {
	exec := &Executor{
		backend: nil,
		cfg:     config.AgentConfig{},
		skills:  nil,
	}

	prompt := exec.buildSystemPrompt()

	if !strings.Contains(prompt, SecurityInstructions) {
		t.Error("system prompt missing security instructions")
	}
	if !strings.Contains(prompt, ExecutorBaseInstructions) {
		t.Error("system prompt missing base instructions")
	}
}

func TestExecutor_SkillsAppendedToPrompt(t *testing.T) {
	skills := []Skill{
		{Name: "go-testing", SystemPromptAdditions: "Write table-driven tests."},
		{Name: "rust-error", SystemPromptAdditions: "Use Result types."},
	}

	exec := &Executor{
		backend: nil,
		cfg:     config.AgentConfig{},
		skills:  skills,
	}

	prompt := exec.buildSystemPrompt()

	if !strings.Contains(prompt, "<skill_additions>") {
		t.Error("missing skill_additions section")
	}
	if !strings.Contains(prompt, "Write table-driven tests.") {
		t.Error("first skill not in prompt")
	}
	if !strings.Contains(prompt, "Use Result types.") {
		t.Error("second skill not in prompt")
	}
}

func TestExecutor_SkillsEmpty(t *testing.T) {
	exec := &Executor{
		backend: nil,
		cfg:     config.AgentConfig{},
		skills:  []Skill{},
	}

	prompt := exec.buildSystemPrompt()

	if strings.Contains(prompt, "<skill_additions>") {
		t.Error("should not have skill_additions when no skills")
	}
}
