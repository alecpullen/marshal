package livebox

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/progress"
)

// The measurement helpers below are the display boundary for producer
// strings: everything they return is already cleaned, measured and cut.

// progressOf is a wire progress observation as the progress package measures
// it, or nil for nil.
func progressOf(p *acpwire.WorkProgress) *progress.Snapshot {
	if p == nil {
		return nil
	}
	out := &progress.Snapshot{
		Frame: progress.Frame{
			Version:         p.Version,
			Sequence:        p.Sequence,
			PrimaryMetricID: p.PrimaryMetricID,
			Detail:          p.Detail,
		},
		ObservedAt:    acpwire.Time(p.ObservedAt),
		MeasuredAt:    acpwire.Time(p.MeasuredAt),
		Partial:       p.Partial,
		Warning:       p.Warning,
		ReportedError: p.ReportedError,
	}
	if p.Phase != nil {
		out.Phase = &progress.Phase{ID: p.Phase.ID, Label: p.Phase.Label, Index: p.Phase.Index, Total: p.Phase.Total}
	}
	if p.Metrics != nil {
		out.Metrics = make([]progress.Metric, len(p.Metrics))
		for i, m := range p.Metrics {
			pm := progress.Metric{ID: m.ID, Label: m.Label, Unit: m.Unit, Current: m.Current}
			if m.Total != nil {
				total := *m.Total
				pm.Total = &total
			}
			if m.Rate != nil {
				rate := *m.Rate
				pm.Rate = &rate
			}
			out.Metrics[i] = pm
		}
	}
	if p.Rows != nil {
		out.Rows = make([]progress.Row, len(p.Rows))
		for i, r := range p.Rows {
			out.Rows[i] = progress.Row{ID: r.ID, Label: r.Label, State: r.State}
		}
	}
	return out
}

// primaryMetric resolves the profile-selected primary metric, falling back
// to the first observation and then to nothing.
func primaryMetric(p *progress.Snapshot) (progress.Metric, bool) {
	if p == nil {
		return progress.Metric{}, false
	}
	if p.PrimaryMetricID != "" {
		for _, m := range p.Metrics {
			if m.ID == p.PrimaryMetricID {
				return m, true
			}
		}
	}
	if len(p.Metrics) > 0 {
		return p.Metrics[0], true
	}
	return progress.Metric{}, false
}

// metricCount is current/total in the metric's own unit, an observed count
// when the producer gave no total, and IEC bytes for a byte metric.
func metricCount(m progress.Metric) string {
	if m.Unit == "bytes" {
		if m.Total != nil {
			return bytesIEC(m.Current) + "/" + bytesIEC(*m.Total)
		}
		return bytesIEC(m.Current)
	}
	if m.Total != nil {
		return fmt.Sprintf("%s/%s %s", number(m.Current), number(*m.Total), m.Unit)
	}
	return fmt.Sprintf("%s %s", number(m.Current), m.Unit)
}

// number renders a producer count without inventing a decimal it did not
// report.
func number(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// rateText renders a measured rate in the metric's own unit.
func rateText(m progress.Metric, rate float64) string {
	if m.Unit == "bytes" {
		return bytesIEC(rate)
	}
	return number(rate) + " " + m.Unit
}

// bytesIEC renders a byte count with IEC binary prefixes (KiB, MiB, …).
func bytesIEC(v float64) string {
	const base = 1024
	if v < base {
		return fmt.Sprintf("%.0f B", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value, i := v, -1
	for value >= base && i < len(units)-1 {
		value /= base
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}

// clean is the display boundary for every producer string in the box. It
// strips escapes (so no producer can control colour or layout) and secrets,
// and folds control runes before anything is measured or rendered.
func clean(s string, limit int) string {
	return progress.Clean(s, limit, false)
}

// lastLine returns the last non-empty line of a bounded output tail, with
// control runes removed, so a row never renders several rows from one field.
func lastLine(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := clean(lines[i], 200); s != "" {
			return s
		}
	}
	return ""
}

// formatElapsed renders a duration as `12s` or `4m 12s`. It is copied
// unchanged from internal/app/tui/transcript.go.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}
