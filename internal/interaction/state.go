package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Mode string

const (
	ModeAsk Mode = "ask"
	ModeAct Mode = "act"
)

type Defaults struct {
	Mode Mode
	Auto bool
}

type State struct {
	Mode     Mode   `json:"mode"`
	Auto     bool   `json:"auto"`
	System   bool   `json:"system_access"`
	Revision uint64 `json:"access_revision"`
}

type Patch struct {
	Mode   *Mode `json:"mode,omitempty"`
	Auto   *bool `json:"auto,omitempty"`
	System *bool `json:"system_access,omitempty"`
}

type Ceiling struct {
	Act, Auto, System bool
}

type Snapshot struct {
	OwnerSessionID string
	ActorSessionID string
	WorkspaceRoot  string
	Access         State
}

type AdmissionLease interface {
	Release()
}

type Authority interface {
	Snapshot() Snapshot
	BeginAdmission(context.Context, Snapshot) (AdmissionLease, error)
}

func Default() Defaults { return Defaults{Mode: ModeAct} }

func ParseMode(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case ModeAsk:
		return ModeAsk, nil
	case ModeAct:
		return ModeAct, nil
	default:
		return "", fmt.Errorf("invalid interaction mode %q: use ask or act", raw)
	}
}

// ModeHasEditing reports whether a mode carries editing authority. Canonical
// Act does; the retired editing spellings still count where an unmigrated
// boundary has not translated them yet. A blank or unrecognized mode is
// read-only, so it never counts.
//
// This is the single definition of "this mode can edit": the session's
// capability bookkeeping and the runtime's access-commit validation both ask
// it rather than keeping their own lists, which would silently diverge.
func ModeHasEditing(mode string) bool {
	switch Mode(strings.ToLower(strings.TrimSpace(mode))) {
	case ModeAct, "edit", "copilot", "auto":
		return true
	}
	return false
}

func ValidateDefaults(d Defaults) error {
	if d.Mode != ModeAsk && d.Mode != ModeAct {
		return fmt.Errorf("invalid interaction mode %q", d.Mode)
	}
	if d.Mode == ModeAsk && d.Auto {
		return errors.New("Ask cannot enable Auto")
	}
	return nil
}

func ValidateState(s State) error {
	if err := ValidateDefaults(Defaults{Mode: s.Mode, Auto: s.Auto}); err != nil {
		return err
	}
	if s.Revision == 0 {
		return errors.New("interaction authority is not initialized")
	}
	return nil
}

func Apply(s State, p Patch) (State, error) {
	if err := ValidateState(s); err != nil {
		return s, err
	}
	if p.Mode == nil && p.Auto == nil && p.System == nil {
		return s, errors.New("empty interaction patch")
	}
	next := s
	if p.Mode != nil {
		if *p.Mode != ModeAsk && *p.Mode != ModeAct {
			return s, fmt.Errorf("invalid interaction mode %q", *p.Mode)
		}
		next.Mode, next.Auto = *p.Mode, false
	}
	if p.Auto != nil {
		if *p.Auto && p.Mode != nil && *p.Mode == ModeAsk {
			return s, errors.New("Ask cannot enable Auto")
		}
		next.Auto = *p.Auto
		if *p.Auto {
			next.Mode = ModeAct
		}
	}
	if p.System != nil {
		next.System = *p.System
	}
	if err := ValidateState(next); err != nil {
		return s, err
	}
	return next, nil
}

func Bound(s State, c Ceiling) State {
	if !c.Act {
		s.Mode = ModeAsk
	}
	s.Auto = s.Auto && c.Auto && s.Mode == ModeAct
	s.System = s.System && c.System
	return s
}

func Intersect(a, b Ceiling) Ceiling {
	return Ceiling{Act: a.Act && b.Act, Auto: a.Auto && b.Auto, System: a.System && b.System}
}

func LegacyDefaults(raw string) (Defaults, string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "plan", "default":
		return Defaults{Mode: ModeAsk}, "replaced with Ask; planning remains a skill workflow", nil
	case "edit":
		return Defaults{Mode: ModeAsk}, "replaced with Ask; approval for every file change was retired", nil
	case "copilot":
		return Defaults{Mode: ModeAct}, "replaced with Act", nil
	case "auto":
		return Defaults{Mode: ModeAct, Auto: true}, "replaced with Act + Auto; Auto now preauthorizes destructive workspace commands", nil
	default:
		return Defaults{}, "", fmt.Errorf("invalid legacy interaction mode %q", raw)
	}
}
