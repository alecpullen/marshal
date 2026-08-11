package skills

import (
	_ "embed"
	"fmt"
)

//go:embed builtin/marshal-sdd-plan-authoring.md
var marshalSDDPlanAuthoring []byte

//go:embed builtin/marshal-executing-plans.md
var marshalExecutingPlans []byte

func loadBuiltIns(idx *Index) error {
	for name, raw := range map[string][]byte{
		"marshal-sdd-plan-authoring": marshalSDDPlanAuthoring,
		"marshal-executing-plans":    marshalExecutingPlans,
	} {
		skill, err := Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse built-in skill %q: %w", name, err)
		}
		idx.Set(skill.Name, skill)
	}
	return nil
}
