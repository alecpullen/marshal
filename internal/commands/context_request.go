package commands

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
)

// maxRequestLinesPerField caps how many lines one message, tool call or tool
// definition contributes to the panel. The snapshot is already bounded in
// bytes; this keeps a single large field from burying the rest.
const maxRequestLinesPerField = 400

// lastRequestPanel shows the last conversation request Marshal handed to its
// provider adapter: options, outcome, every message and every tool definition.
func lastRequestPanel(state *session.State) Result {
	snap, ok := state.RequestInspection()
	if !ok {
		return Panel("Last request", true, []Row{{
			Text: "No request sent yet",
			Desc: "The first model call of a turn records what was submitted to the provider adapter.",
		}})
	}

	rows := []Row{
		{
			Text:   "Sent",
			Detail: snap.At.Format("15:04:05") + " · " + snap.Provider + "/" + snap.Model,
			Desc:   "As handed to the provider adapter, not its wire encoding.",
		},
		{Text: "Outcome", Detail: requestOutcomeDetail(snap.Outcome)},
		{Text: "Options", Detail: requestOptionsDetail(snap.Options)},
	}
	if snap.PackKnown {
		detail := fmt.Sprintf("~%s/%s tokens, %d sections",
			strutil.CompactTokens(snap.PackTokens), strutil.CompactTokens(snap.PackWindow), snap.PackSections)
		if snap.PackTruncated {
			detail += ", truncated"
		}
		rows = append(rows, Row{Text: "Pack", Detail: detail, Desc: "The context pack at dispatch; token counts are estimates."})
	}
	if snap.Truncated {
		rows = append(rows, Row{
			Text:   "Snapshot capped",
			Detail: fmt.Sprintf("%d messages, %d tools, %d bytes omitted", snap.MessagesOmitted, snap.ToolsOmitted, snap.OmittedBytes),
			Desc:   "The inspection copy is bounded; the request itself was sent in full.",
		})
	}

	rows = append(rows, Row{Header: fmt.Sprintf("Messages (%d)", len(snap.Messages))})
	for i, m := range snap.Messages {
		rows = append(rows, Row{
			Text:     fmt.Sprintf("%d  %s", i+1, m.Role),
			Detail:   requestMessageDetail(m),
			Children: requestMessageRows(m),
		})
	}

	rows = append(rows, Row{Header: fmt.Sprintf("Tools (%d)", len(snap.Tools))})
	for _, t := range snap.Tools {
		children := textRows(t.Description, t.Truncated, t.OmittedBytes)
		children = append(children, Row{Header: "Parameters"})
		children = append(children, textRows(t.Parameters, false, 0)...)
		rows = append(rows, Row{
			Text:     t.Name,
			Detail:   byteSize(len(t.Description) + len(t.Parameters)),
			Children: children,
		})
	}
	return Panel("Last request", true, rows)
}

// lastRequestRow summarises the last request for the /context panel and opens
// the full view.
func lastRequestRow(state *session.State) (Row, bool) {
	snap, ok := state.RequestInspection()
	if !ok {
		return Row{}, false
	}
	return Row{
		Text: "Last request",
		Detail: fmt.Sprintf("%s · %s · %d messages, %d tools",
			snap.Model, snap.Outcome.Status, len(snap.Messages), len(snap.Tools)),
		ActionLabel: "↵ open",
		Action:      lastRequestPanel,
	}, true
}

func requestOutcomeDetail(o session.InspectionOutcome) string {
	detail := string(o.Status)
	if o.Err != "" {
		detail += ": " + o.Err
	}
	return detail
}

func requestOptionsDetail(o session.InspectionOptions) string {
	var parts []string
	if o.Thinking != "" {
		parts = append(parts, "thinking "+o.Thinking)
	}
	if o.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max %d", *o.MaxTokens))
	}
	if o.Temperature != nil {
		parts = append(parts, fmt.Sprintf("temperature %g", *o.Temperature))
	}
	if o.ResponseFormat != "" {
		parts = append(parts, "format "+o.ResponseFormat)
	}
	if o.ToolChoice != "" {
		parts = append(parts, "tool choice "+o.ToolChoice)
	}
	if o.Streaming {
		parts = append(parts, "streaming")
	}
	if len(parts) == 0 {
		return "defaults"
	}
	return strings.Join(parts, " · ")
}

func requestMessageDetail(m session.InspectionMessage) string {
	var parts []string
	if m.Content != "" {
		parts = append(parts, byteSize(len(m.Content)))
	}
	switch n := len(m.ToolCalls); {
	case n == 1:
		parts = append(parts, "1 tool call")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d tool calls", n))
	}
	if m.ToolCallID != "" {
		parts = append(parts, "for "+m.ToolCallID)
	}
	return strings.Join(parts, " · ")
}

func requestMessageRows(m session.InspectionMessage) []Row {
	rows := textRows(m.Content, m.Truncated, m.OmittedBytes)
	for _, tc := range m.ToolCalls {
		rows = append(rows, Row{Header: "Tool call " + tc.ID})
		rows = append(rows, textRows(tc.Name+" "+tc.Args, tc.Truncated, tc.OmittedBytes)...)
	}
	if len(rows) == 0 {
		rows = append(rows, Row{Text: "(empty)"})
	}
	return rows
}

// textRows renders text one row per line, capped, with a note when the
// snapshot or the panel dropped content.
func textRows(text string, truncated bool, omitted int) []Row {
	if text == "" && !truncated {
		return nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var rows []Row
	for i, line := range lines {
		if i == maxRequestLinesPerField {
			rows = append(rows, Row{Text: fmt.Sprintf("… %d more lines", len(lines)-i)})
			break
		}
		if line == "" {
			line = " "
		}
		rows = append(rows, Row{Text: line})
	}
	if truncated {
		rows = append(rows, Row{Text: fmt.Sprintf("… %d bytes omitted from the snapshot", omitted)})
	}
	return rows
}

func byteSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
