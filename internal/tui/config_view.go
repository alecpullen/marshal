// internal/tui/config_view.go
// Config overlay: displays the active marshal.toml values in a table.
// API keys are masked. Model names are coloured blue.

package tui

import (
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Row types ─────────────────────────────────────────────────────────────────

type cfgRowKind int

const (
	cfgSection cfgRowKind = iota
	cfgPair
	cfgBlank
)

type cfgRow struct {
	kind  cfgRowKind
	key   string
	value string
	vkind cfgValKind
}

type cfgValKind int

const (
	cfgValNormal cfgValKind = iota
	cfgValModel             // blue — model name
	cfgValURL               // muted — base URL
	cfgValSecret            // dim — masked key
	cfgValBool              // amber / green / red
	cfgValNum               // default
)

// ── Model ─────────────────────────────────────────────────────────────────────

type ConfigModel struct {
	rows       []cfgRow
	execConn   connStatus
	criticConn connStatus
	width      int
	height     int
}

func newConfigModel() ConfigModel {
	return ConfigModel{}
}

// WithCfg rebuilds the display rows from a live config.
func (m ConfigModel) WithCfg(cfg *config.Config) ConfigModel {
	m.rows = buildRows(cfg)
	return m
}

// SetConnStatus updates the live connectivity dots shown in the overlay.
func (m ConfigModel) SetConnStatus(exec, critic connStatus) ConfigModel {
	m.execConn = exec
	m.criticConn = critic
	return m
}

func buildRows(cfg *config.Config) []cfgRow {
	mask := func(s string) string {
		if len(s) <= 8 {
			return strings.Repeat("*", len(s))
		}
		return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
	}
	boolStr := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}

	rows := []cfgRow{
		{kind: cfgSection, key: "executor"},
		{kind: cfgPair, key: "model", value: cfg.Executor.Model, vkind: cfgValModel},
		{kind: cfgPair, key: "base_url", value: cfg.Executor.BaseURL, vkind: cfgValURL},
		{kind: cfgPair, key: "api_key", value: mask(cfg.Executor.APIKey), vkind: cfgValSecret},
		{kind: cfgPair, key: "temperature", value: fmt.Sprintf("%.2f", cfg.Executor.Temperature), vkind: cfgValNum},
		{kind: cfgPair, key: "max_tokens", value: fmt.Sprintf("%d", cfg.Executor.MaxTokens), vkind: cfgValNum},
		{kind: cfgBlank},

		{kind: cfgSection, key: "critic"},
		{kind: cfgPair, key: "model", value: cfg.Critic.Model, vkind: cfgValModel},
		{kind: cfgPair, key: "base_url", value: cfg.Critic.BaseURL, vkind: cfgValURL},
		{kind: cfgPair, key: "api_key", value: mask(cfg.Critic.APIKey), vkind: cfgValSecret},
		{kind: cfgPair, key: "temperature", value: fmt.Sprintf("%.2f", cfg.Critic.Temperature), vkind: cfgValNum},
		{kind: cfgPair, key: "max_tokens", value: fmt.Sprintf("%d", cfg.Critic.MaxTokens), vkind: cfgValNum},
		{kind: cfgPair, key: "json_output", value: boolStr(cfg.Critic.JSONOutput), vkind: cfgValBool},
		{kind: cfgBlank},

		{kind: cfgSection, key: "loop"},
		{kind: cfgPair, key: "max_rounds", value: fmt.Sprintf("%d", cfg.Loop.MaxRounds), vkind: cfgValNum},
		{kind: cfgPair, key: "auto_commit", value: boolStr(cfg.Loop.AutoCommit), vkind: cfgValBool},
		{kind: cfgPair, key: "auto_revert", value: boolStr(cfg.Loop.AutoRevert), vkind: cfgValBool},
		{kind: cfgPair, key: "branch_isolation", value: boolStr(cfg.Loop.BranchIsolation), vkind: cfgValBool},
		{kind: cfgPair, key: "compact_after", value: fmt.Sprintf("%d", cfg.Loop.CompactAfter), vkind: cfgValNum},
		{kind: cfgBlank},

		{kind: cfgSection, key: "session"},
		{kind: cfgPair, key: "db_path", value: cfg.Session.DBPath, vkind: cfgValURL},
		{kind: cfgBlank},

		{kind: cfgSection, key: "ui"},
		{kind: cfgPair, key: "editor", value: cfg.UI.Editor, vkind: cfgValNormal},
	}
	return rows
}

func (m ConfigModel) Update(msg tea.Msg) (ConfigModel, tea.Cmd) {
	return m, nil
}

func (m ConfigModel) View(w, h int) string {
	innerW := w - 8
	if innerW < 20 {
		innerW = 20
	}

	const keyW = 18 // key column width

	var sb strings.Builder

	// Title with live connection summary
	execStatus  := connDot(m.execConn) + lipgloss.NewStyle().Foreground(colTx3).Render(" exec")
	criticStatus := connDot(m.criticConn) + lipgloss.NewStyle().Foreground(colTx3).Render(" critic")
	titleLeft  := lipgloss.NewStyle().Foreground(colTx).Bold(true).Render("config")
	titleRight := execStatus + "  " + criticStatus
	fillW := innerW - lipgloss.Width(titleLeft) - lipgloss.Width(titleRight)
	if fillW < 1 { fillW = 1 }
	fill := strings.Repeat(" ", fillW)
	sb.WriteString(titleLeft + fill + titleRight)
	sb.WriteByte('\n')
	sb.WriteString(styleRoundSep.Render(strings.Repeat("─", innerW)))
	sb.WriteByte('\n')
	sb.WriteByte('\n')

	currentSection := ""
	for _, row := range m.rows {
		switch row.kind {
		case cfgBlank:
			sb.WriteByte('\n')

		case cfgSection:
			currentSection = row.key
			sb.WriteString(
				lipgloss.NewStyle().Foreground(colBl).Bold(true).
					Render("[" + row.key + "]"),
			)
			sb.WriteByte('\n')

		case cfgPair:
			keyStr := lipgloss.NewStyle().
				Foreground(colTx2).
				Width(keyW).
				Render(row.key)

			valStr := m.renderValue(row.value, row.vkind, innerW-keyW-4)

			// Append a connectivity dot next to base_url for executor and critic.
			dot := ""
			if row.key == "base_url" {
				switch currentSection {
				case "executor":
					dot = "  " + connDot(m.execConn)
				case "critic":
					dot = "  " + connDot(m.criticConn)
				}
			}

			sb.WriteString("  " + keyStr + " " + valStr + dot)
			sb.WriteByte('\n')
		}
	}

	// Hint
	sb.WriteByte('\n')
	sb.WriteString(stylePromptHint.Render("q / esc  close"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg2).
		Padding(1, 2).
		Width(w - 4).
		Height(h - 2).
		Render(sb.String())
}

func (m ConfigModel) renderValue(v string, kind cfgValKind, maxW int) string {
	display := truncate(v, maxW)
	switch kind {
	case cfgValModel:
		return lipgloss.NewStyle().Foreground(colBl).Render(display)
	case cfgValURL:
		return lipgloss.NewStyle().Foreground(colTx3).Render(display)
	case cfgValSecret:
		return lipgloss.NewStyle().Foreground(colBr3).Render(display)
	case cfgValBool:
		if v == "true" {
			return lipgloss.NewStyle().Foreground(colGr).Render(display)
		}
		return lipgloss.NewStyle().Foreground(colRd).Render(display)
	default:
		return lipgloss.NewStyle().Foreground(colTx).Render(display)
	}
}
