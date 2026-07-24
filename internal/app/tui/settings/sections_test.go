package settings

import (
	"testing"
)

func TestSectionListContainsCustomAgents(t *testing.T) {
	sections := sectionList()
	found := false
	for _, s := range sections {
		if s.id == "custom_agents" {
			found = true
			if s.title != "Custom Agents" {
				t.Errorf("custom_agents section title = %q, want %q", s.title, "Custom Agents")
			}
			if s.root == nil {
				t.Error("custom_agents section root is nil")
			}
			break
		}
	}
	if !found {
		t.Fatal("sectionList() does not contain a 'custom_agents' entry")
	}
}
