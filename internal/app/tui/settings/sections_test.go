package settings

import (
	"testing"
)

func TestSectionListContainsCustomAgents(t *testing.T) {
	sections := sectionList()
	found := false
	for _, s := range sections {
		if s.ID == "custom_agents" {
			found = true
			if s.Title != "Custom Agents" {
				t.Errorf("custom_agents section title = %q, want %q", s.Title, "Custom Agents")
			}
			if s.Root == nil {
				t.Error("custom_agents section root is nil")
			}
			break
		}
	}
	if !found {
		t.Fatal("sectionList() does not contain a 'custom_agents' entry")
	}
}
