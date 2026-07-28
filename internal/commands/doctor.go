package commands

import (
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// doctorHandler runs config.Diagnose and renders the results as a Doc.
func doctorHandler(state *session.State, _ []string) Result {
	// Layers are not stored on session.State; pass an empty value so
	// provenance-based checks (Source field) are unavailable here.
	diags := config.Diagnose(state.Config, config.Layers{})

	var rows []Row

	// Group by severity: errors first, then warnings.
	var errs, warns []config.Diagnostic
	for _, d := range diags {
		switch d.Severity {
		case config.SeverityError:
			errs = append(errs, d)
		case config.SeverityWarning:
			warns = append(warns, d)
		}
	}

	if len(errs) == 0 && len(warns) == 0 {
		rows = append(rows, Row{Text: "No problems found."})
		return Panel("Doctor", false, rows)
	}

	if len(errs) > 0 {
		rows = append(rows, Row{Header: "Errors"})
		for _, d := range errs {
			rows = append(rows, Row{
				Text:   d.Path,
				Detail: "error",
				Desc:   d.Message,
			})
		}
	}

	if len(warns) > 0 {
		rows = append(rows, Row{Header: "Warnings"})
		for _, d := range warns {
			rows = append(rows, Row{
				Text:   d.Path,
				Detail: "warning",
				Desc:   d.Message,
			})
		}
	}

	return Panel("Doctor", false, rows)
}
