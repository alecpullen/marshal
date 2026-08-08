package skills

import (
	_ "embed"
	"fmt"
)

//go:embed builtin/marshal-sdd-plan-authoring.md
var marshalSDDPlanAuthoring []byte

func loadBuiltIns(idx *Index) error {
	raw, err := Parse(string(marshalSDDPlanAuthoring))
	if err != nil {
		return fmt.Errorf("parse built-in skill %q: %w", "marshal-sdd-plan-authoring", err)
	}
	idx.Set(raw.Name, raw)
	return nil
}
