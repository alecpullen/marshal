package settings

import (
	"fmt"
	"strconv"
	"time"

	"charm.land/huh/v2"
)

type shellBuffers struct {
	timeout, maxOutput, maxJobs, retention string
}

func newShellPane(s *state) sectionPane {
	b := &shellBuffers{}
	form := newScalarPane(func() *huh.Form {
		b.timeout = strconv.Itoa(s.cfg.Tools.Shell.DefaultTimeoutSeconds)
		b.maxOutput = strconv.Itoa(s.cfg.Tools.Shell.MaxOutputBytes)
		b.maxJobs = strconv.Itoa(s.cfg.Tools.Shell.MaxBackgroundJobs)
		b.retention = s.cfg.Tools.Shell.BackgroundRetention.String()
		return newSectionForm(
			numField("Default timeout (s)", &b.timeout, 0, func(v int) { s.cfg.Tools.Shell.DefaultTimeoutSeconds = v }),
			numField("Max output bytes", &b.maxOutput, 0, func(v int) { s.cfg.Tools.Shell.MaxOutputBytes = v }),
			numField("Max background jobs", &b.maxJobs, 0, func(v int) { s.cfg.Tools.Shell.MaxBackgroundJobs = v }),
			huh.NewInput().Key("Background retention").Title("Background retention").Value(&b.retention).
				Validate(func(v string) error {
					d, err := time.ParseDuration(v)
					if err != nil {
						return fmt.Errorf("must be a duration like 8h")
					}
					s.cfg.Tools.Shell.BackgroundRetention = d
					return nil
				}),
			huh.NewConfirm().Key("Allow network").Title("Allow network").Value(&s.cfg.Tools.Shell.AllowNetwork),
			huh.NewConfirm().Key("Allow sudo").Title("Allow sudo").Value(&s.cfg.Tools.Shell.AllowSudo),
			huh.NewConfirm().Key("Allow destructive").Title("Allow destructive").Value(&s.cfg.Tools.Shell.AllowDestructive),
			huh.NewConfirm().Key("Auto-approve shell").Title("Auto-approve shell").Value(&s.cfg.Tools.Shell.AutoApprove),
			huh.NewSelect[string]().Key("Dynamic argv0 guardrail").Title("Dynamic argv0 guardrail").
				Options(huh.NewOption("deny", "deny"), huh.NewOption("confirm", "confirm"), huh.NewOption("allow", "allow")).
				Value(&s.cfg.Tools.Shell.GuardrailDynamicArgv0),
		)
	})
	return newMixedPane(form,
		newListStrings("Allow commands", &s.cfg.Tools.Shell.Allow.Commands),
		newListStrings("Confirm commands", &s.cfg.Tools.Shell.Confirm.Commands),
		newListStrings("Deny patterns", &s.cfg.Tools.Shell.Deny.Patterns),
	)
}
