package skills

import (
	"testing"
)

func TestParseFrontmatterValid(t *testing.T) {
	raw := `+++
name = "systematic-debugging"
description = "Systematic debugging process for bugs, test failures, and unexpected behavior"
risk = "read_only"
+++

# Systematic Debugging

When debugging, follow this process:
1. Reproduce the bug
2. Isolate
3. Identify root cause
`

	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if skill.Name != "systematic-debugging" {
		t.Fatalf("Name = %q, want systematic-debugging", skill.Name)
	}
	if skill.Description != "Systematic debugging process for bugs, test failures, and unexpected behavior" {
		t.Fatalf("Description = %q", skill.Description)
	}
	if skill.Risk != "read_only" {
		t.Fatalf("Risk = %q, want read_only", skill.Risk)
	}
	if skill.Body != "# Systematic Debugging\n\nWhen debugging, follow this process:\n1. Reproduce the bug\n2. Isolate\n3. Identify root cause\n" {
		t.Fatalf("Body = %q", skill.Body)
	}
	if len(skill.Tools) != 0 {
		t.Fatalf("Tools = %v, want empty", skill.Tools)
	}
}

func TestParseFrontmatterMissingName(t *testing.T) {
	raw := `+++
description = "A skill without a name"
+++

Body text.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseFrontmatterMissingDescription(t *testing.T) {
	raw := `+++
name = "my-skill"
+++

Body text.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	raw := `# Just a heading

No frontmatter here.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseFrontmatterDefaultRisk(t *testing.T) {
	raw := `+++
name = "my-skill"
description = "A skill without explicit risk"
+++

Body.
`
	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if skill.Risk != "read_only" {
		t.Fatalf("Risk = %q, want read_only (default)", skill.Risk)
	}
}

func TestParseFrontmatterToolDefinitions(t *testing.T) {
	raw := `+++
name = "k8s-deploy"
description = "Kubernetes deployment workflows"
risk = "command"

[[tools]]
name = "kubectl_get_pods"
description = "List pods in a namespace"
risk = "command"
schema = '{"type": "object", "properties": {"namespace": {"type": "string"}}}'
handler = "shell"
command = "kubectl get pods -n {{.namespace}}"
+++

# K8s Deploy

Safe deployment instructions.
`
	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if len(skill.Tools) != 1 {
		t.Fatalf("Tools length = %d, want 1", len(skill.Tools))
	}
	if skill.Tools[0].Name != "kubectl_get_pods" {
		t.Fatalf("Tools[0].Name = %q", skill.Tools[0].Name)
	}
	if skill.Tools[0].Command != "kubectl get pods -n {{.namespace}}" {
		t.Fatalf("Tools[0].Command = %q", skill.Tools[0].Command)
	}
}

func TestIndexLoadAndList(t *testing.T) {
	idx := NewIndex()
	idx.Set("a", Skill{Name: "a", Description: "Skill A"})
	idx.Set("b", Skill{Name: "b", Description: "Skill B"})

	skill, ok := idx.Load("a")
	if !ok {
		t.Fatal("Load(a) returned false")
	}
	if skill.Name != "a" {
		t.Fatalf("Load(a).Name = %q", skill.Name)
	}

	_, ok = idx.Load("nonexistent")
	if ok {
		t.Fatal("Load(nonexistent) should return false")
	}

	list := idx.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("List order: %v, want [a, b]", []string{list[0].Name, list[1].Name})
	}
}

func TestIndexListEmpty(t *testing.T) {
	idx := NewIndex()
	list := idx.List()
	if list == nil {
		t.Fatal("List() returned nil, want empty slice")
	}
	if len(list) != 0 {
		t.Fatalf("List length = %d, want 0", len(list))
	}
}
