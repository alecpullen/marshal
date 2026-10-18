// Package progress holds bounded display data. It never owns execution.
package progress

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"marshal/internal/redact"
)

const (
	Version         = 1
	Prefix          = "@@marshal-progress/v1 "
	MaxNumber       = float64(1<<53 - 1)
	MaxEventBytes   = 16 << 10
	MaxGoLineBytes  = 64 << 10
	MaxDisplayBytes = 8 << 10
	MaxCases        = 20000
	FreshFor        = 30 * time.Second
)

var ErrInvalid = errors.New("invalid progress data")
var ErrSequence = errors.New("stale progress sequence")

type Config struct {
	Profile string `json:"profile,omitempty"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
}
type Phase struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`
}
type Metric struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Unit    string   `json:"unit"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
	Rate    *float64 `json:"rate,omitempty"`
}
type Row struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
}

// Frame is the producer's complete snapshot. Runtime facts are excluded.
type Frame struct {
	Version         int      `json:"version"`
	Sequence        uint64   `json:"sequence"`
	Phase           *Phase   `json:"phase,omitempty"`
	PrimaryMetricID string   `json:"primaryMetricId,omitempty"`
	Metrics         []Metric `json:"metrics,omitempty"`
	Rows            []Row    `json:"rows,omitempty"`
	Detail          string   `json:"detail,omitempty"`
}
type Snapshot struct {
	Frame
	ObservedAt    time.Time `json:"observedAt"`
	MeasuredAt    time.Time `json:"measuredAt,omitempty"`
	Partial       bool      `json:"partial,omitempty"`
	Warning       string    `json:"warning,omitempty"`
	ReportedError bool      `json:"reportedError,omitempty"`
}
type Estimate struct {
	Fraction *float64
	Rate     *float64
	ETA      *time.Duration
	Stale    bool
}

// Clean is the common display/persistence boundary; callers keep raw identity.
func Clean(s string, limit int, multiline bool) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			if multiline {
				return r
			}
			return ' '
		}
		if r == '\r' {
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = redact.Secrets(s)
	if limit > 0 && utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}
func NormalizeConfig(c Config) (Config, error) {
	if c.Profile == "" {
		c.Profile = "generic"
	}
	if c.Source == "" {
		c.Source = "none"
	}
	switch c.Profile {
	case "generic", "tests", "download", "transfer", "custom":
	default:
		return Config{}, fmt.Errorf("%w: profile", ErrInvalid)
	}
	switch c.Source {
	case "none":
	case "events-v1":
		if c.Profile == "generic" {
			return Config{}, fmt.Errorf("%w: events profile", ErrInvalid)
		}
	case "go-test-json":
		if c.Profile != "tests" {
			return Config{}, fmt.Errorf("%w: test source/profile", ErrInvalid)
		}
	default:
		return Config{}, fmt.Errorf("%w: source", ErrInvalid)
	}
	if !utf8.ValidString(c.Title) || utf8.RuneCountInString(c.Title) > 80 {
		return Config{}, fmt.Errorf("%w: title", ErrInvalid)
	}
	c.Title = Clean(c.Title, 80, false)
	return c, nil
}
func Clone(s *Snapshot) *Snapshot {
	if s == nil {
		return nil
	}
	out := *s
	if s.Phase != nil {
		p := *s.Phase
		out.Phase = &p
	}
	out.Metrics = append([]Metric(nil), s.Metrics...)
	for i := range out.Metrics {
		if p := s.Metrics[i].Total; p != nil {
			v := *p
			out.Metrics[i].Total = &v
		}
		if p := s.Metrics[i].Rate; p != nil {
			v := *p
			out.Metrics[i].Rate = &v
		}
	}
	out.Rows = append([]Row(nil), s.Rows...)
	return &out
}
func WorkKey(conversation, epoch, kind, sourceID string) string {
	b, _ := json.Marshal([4]string{conversation, epoch, kind, sourceID})
	return "work:" + base64.RawURLEncoding.EncodeToString(b)
}
func ResultRevision(key, state, outcome string, exitCode *int, endedAt time.Time) string {
	b, _ := json.Marshal([]any{key, state, outcome, exitCode, endedAt.UTC().Format(time.RFC3339Nano)})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
