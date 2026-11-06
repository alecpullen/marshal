package tui

import (
	"fmt"
	"strings"

	"marshal/internal/acpwire"
)

// parsedSDDArgs is the pure result of parsing /sdd arguments.
type parsedSDDArgs struct {
	Strategy         string
	ExplicitStrategy bool
	New              bool
	FromLastPlan     bool
	PlanPath         string
	Goal             string
}

// parseSDDArgs parses /sdd arguments. Rules:
//
//   - `--strategy=agent|adaptive|strict` sets ExplicitStrategy.
//   - `--strategy agent|adaptive|strict` consumes the next argument.
//   - `new` must be the first non-flag argument and sends the remaining text
//     to Goal.
//   - `new --from-last-plan` sets FromLastPlan and requires no goal.
//   - A normal non-flag argument sequence becomes PlanPath joined with one
//     space, preserving the existing quoted-path behavior.
//   - Unknown strategy values and missing strategy values return usage errors.
func parseSDDArgs(args []string) (parsedSDDArgs, error) {
	var out parsedSDDArgs
	var positional []string
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--strategy="):
			val := strings.TrimPrefix(a, "--strategy=")
			if err := out.setStrategy(val); err != nil {
				return out, err
			}
			i++
		case a == "--strategy":
			if i+1 >= len(args) {
				return out, fmt.Errorf("usage: /sdd --strategy <agent|adaptive|strict>")
			}
			if err := out.setStrategy(args[i+1]); err != nil {
				return out, err
			}
			i += 2
		case a == "--from-last-plan":
			out.FromLastPlan = true
			i++
		case a == "new" && len(positional) == 0:
			out.New = true
			i++
		default:
			positional = append(positional, a)
			i++
		}
	}
	if out.New {
		out.Goal = strings.TrimSpace(strings.Join(positional, " "))
		return out, nil
	}
	out.PlanPath = strings.TrimSpace(strings.Join(positional, " "))
	return out, nil
}

func (p *parsedSDDArgs) setStrategy(val string) error {
	switch val {
	case acpwire.StrategyAgent, acpwire.StrategyAdaptive, acpwire.StrategyStrict:
		p.Strategy = val
		p.ExplicitStrategy = true
		return nil
	default:
		return fmt.Errorf("unknown strategy %q (want agent, adaptive, or strict)", val)
	}
}
