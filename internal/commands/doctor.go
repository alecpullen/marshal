package commands

import (
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// doctorHandler runs config.Diagnose and renders the results as a Doc.
// Real per-layer provenance comes from state.Layers(); when Layers is
// the zero value (e.g. unit tests) the Source on each Diagnostic reads
// as "default" and the rendered rows omit the provenance suffix.
func doctorHandler(state *session.State, _ []string) Result {
	diags := config.Diagnose(state.Config, state.Layers())

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

	renderRow := func(d config.Diagnostic, severity string) Row {
		desc := d.Message
		if d.Source != "" && d.Source != "default" {
			desc = fmt.Sprintf("%s — in %s", d.Message, d.Source)
		}
		return Row{
			Text:   d.Path,
			Detail: severity,
			Desc:   desc,
		}
	}

	if len(errs) > 0 {
		rows = append(rows, Row{Header: "Errors"})
		for _, d := range errs {
			rows = append(rows, renderRow(d, "error"))
		}
	}

	if len(warns) > 0 {
		rows = append(rows, Row{Header: "Warnings"})
		for _, d := range warns {
			rows = append(rows, renderRow(d, "warning"))
		}
	}

	return Panel("Doctor", false, rows)
}
