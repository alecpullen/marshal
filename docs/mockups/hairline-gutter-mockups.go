//go:build ignore

// Static terminal mockups of three minimal-TUI directions for Marshal.
// Run with: go run mockups.go
// Colors match the Warm Sunset theme (internal/app/tui/theme/theme.go).
package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const W = 72

// Warm Sunset 256-color palette.
const (
	coral   = 209 // AccentPrimary
	violet  = 175 // AccentSecondary
	teal    = 43  // StatusSuccess
	warning = 172 // StatusWarning
	errRed  = 203 // StatusError
	fgDef   = 252 // FGDefault
	muted   = 244 // FGMuted
	bordMu  = 245 // BorderMuted
	emph    = 255 // FGEmphasis
	surface = 237 // BGSurface
	usrTxt  = 246 // UserPrompt
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func vis(s string) int { return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, "")) }

func c(n int, s string) string  { return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", n, s) }
func cb(n int, s string) string { return fmt.Sprintf("\x1b[1;38;5;%dm%s\x1b[0m", n, s) }
func dim(s string) string       { return c(muted, s) }
func bold(s string) string      { return "\x1b[1m" + s + "\x1b[0m" }
func txt(s string) string       { return c(fgDef, s) }

// cursor is a block cursor rendered as reverse video.
const cursor = "\x1b[7m \x1b[27m"

// lr joins a left and right cluster padded to width W.
func lr(left, right string) string {
	gap := W - vis(left) - vis(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// tint renders one code line on a BGSurface background, padded to inner.
func tint(indent string, s string, inner int) string {
	pad := inner - utf8.RuneCountInString(s)
	if pad < 0 {
		pad = 0
	}
	return indent + fmt.Sprintf("\x1b[48;5;%dm\x1b[38;5;%dm%s%s\x1b[0m", surface, fgDef, s, strings.Repeat(" ", pad))
}

func p(lines ...string) {
	for _, l := range lines {
		fmt.Println(l)
	}
}

func section(title, note string) {
	fmt.Println()
	fmt.Println()
	label := " " + title + " "
	rule := W - utf8.RuneCountInString(label) - 4
	if rule < 0 {
		rule = 0
	}
	fmt.Println(dim("────") + cb(emph, label) + dim(strings.Repeat("─", rule)))
	if note != "" {
		fmt.Println(dim("  " + note))
	}
	fmt.Println()
}

func caption(s string) {
	fmt.Println()
	fmt.Println(dim("  ▸ " + s))
	fmt.Println()
}

// ───────────────────────── current design ─────────────────────────

func current() {
	section("CURRENT", "7 rows of chrome · borders on input, code, answers · 3 metadata rows")

	// title bar
	p(lr(" "+c(coral, "●")+" "+cb(coral, "marshal"), dim("marshal · branch 1/2")+" "))
	p("")
	// user echo
	p(" " + c(coral, "❯") + " " + c(usrTxt, "fix the config merge order bug"))
	p("")
	// tool lines
	p(" " + c(teal, "✔") + dim(" file.read config.go · 0.4s"))
	p(" " + c(teal, "✔") + dim(" patch.apply config.go +12 -3"))
	p("")
	// final answer: left border + Response label
	g := c(coral, "│") + " "
	p(g + cb(coral, "Response"))
	p(g + txt("The loader merged project config before user config, so"))
	p(g + txt("project-local values were clobbered. Swapped the merge order"))
	p(g + txt("in load() and added a regression test."))
	inner := 42
	p(g + dim("╭"+strings.Repeat("─", inner)+"╮"))
	for _, l := range []string{"func merge(dst, src *Config) {", "    applyUser(dst, src)", "}"} {
		p(g + dim("│") + txt(" "+l+strings.Repeat(" ", inner-1-utf8.RuneCountInString(l))) + dim("│"))
	}
	p(g + dim("╰"+strings.Repeat("─", inner)+"╯"))
	// input box
	p(c(coral, "╭" + strings.Repeat("─", W-2) + "╮"))
	line := " " + c(coral, "❯") + " " + dim("ask marshal…")
	p(c(coral, "│") + line + strings.Repeat(" ", W-2-vis(line)) + c(coral, "│"))
	p(c(coral, "╰" + strings.Repeat("─", W-2) + "╯"))
	// footer rule + hints
	p(c(bordMu, strings.Repeat("─", W)))
	p(bold("Tab") + " mode" + dim(" · ") + bold("Alt+M") + " model" + dim(" · ") + bold("/") + " command" + dim(" · ") + bold("@") + " file" + dim(" · ") + bold("Ctrl+G") + " thinking")
	// status line
	p(lr(" "+txt("auto")+dim(" · ")+txt("qwen3 @ ollama")+dim(" · ")+txt("local")+dim(" · ")+txt("ctx 12k/32k"),
		c(teal, "✔ patch.apply")+" "))

	caption("approval today — bordered form inside the input box:")
	aw := 52
	top := "╭" + strings.Repeat("─", aw) + "╮"
	bot := "╰" + strings.Repeat("─", aw) + "╯"
	p(c(warning, top))
	rows := []string{
		" " + cb(warning, "⚠ shell.exec") + dim(" · rm -rf build/ · risk: high"),
		" " + dim("sandbox: restricted · network: off"),
		" " + c(coral, "▸ ") + bold("Approve"),
		"   " + dim("Deny"),
		"   " + dim("Edit command/args"),
		"   " + dim("Always allow (save to config)"),
		"   " + dim("Allow this session"),
	}
	for _, r := range rows {
		p(c(warning, "│") + r + strings.Repeat(" ", aw-vis(r)) + c(warning, "│"))
	}
	p(c(warning, bot))
}

// ───────────────────────── direction A ─────────────────────────

func directionA() {
	section("A · BARE PROSE", "2 rows of chrome · no borders, no labels · state lives in the prompt glyph")

	p(c(coral, "❯") + " " + c(usrTxt, "fix the config merge order bug"))
	p("")
	p("  " + c(teal, "✓") + dim(" read config.go") + dim(" · ") + c(teal, "✓") + dim(" patch config.go +12 -3"))
	p("")
	p("  " + txt("The loader merged project config before user config, so"))
	p("  " + txt("project-local values were clobbered. Swapped the merge order"))
	p("  " + txt("in load() and added a regression test."))
	p("")
	inner := 40
	for _, l := range []string{"func merge(dst, src *Config) {", "    applyUser(dst, src)", "}"} {
		p(tint("    ", " "+l, inner))
	}
	p("")
	p(c(coral, "❯") + " " + cursor)
	p("")
	p(lr("  "+dim("auto · qwen3 @ ollama · ctx 12k/32k"),
		bold("Tab")+dim(" mode · ")+bold("/")+dim(" cmd · ")+bold("?")+dim(" help  ")))

	caption("prompt glyph carries the old border-color state:")
	p("  " + c(coral, "❯") + dim(" idle    ") + c(coral, "⠹") + dim(" busy    ") + c(violet, "?") + dim(" question    ") + c(warning, "⚠") + dim(" approval    ") + c(teal, "❯") + dim(" success pulse"))

	caption("approval — two flat lines replace the bordered form:")
	p("  " + c(warning, "⚠") + " " + txt("rm -rf build/") + dim(" · shell · high risk · sandboxed"))
	p("  " + c(coral, "▸ ") + bold("allow") + dim("   always   session   edit   deny") + strings.Repeat(" ", 18) + dim("Esc deny"))
}

// ───────────────────────── direction B ─────────────────────────

func directionB() {
	section("B · HAIRLINE GUTTER", "2 rows of chrome · one gutter column encodes speaker + state everywhere")

	p(" " + c(coral, "❯") + " " + c(usrTxt, "fix the config merge order bug"))
	p("")
	p(" " + dim("·") + dim(" read config.go 0.4s"))
	p(" " + dim("·") + dim(" patch config.go +12 -3"))
	p("")
	g := c(coral, "▍")
	p(" " + g + " " + txt("The loader merged project config before user config, so"))
	p(" " + g + " " + txt("project-local values were clobbered. Swapped the merge"))
	p(" " + g + " " + txt("order in load() and added a regression test."))
	p(" " + g)
	inner := 40
	for _, l := range []string{"func merge(dst, src *Config) {", "    applyUser(dst, src)", "}"} {
		p(" " + g + tint("   ", " "+l, inner))
	}
	p("")
	p(" " + c(coral, "▍") + c(coral, "❯") + " " + cursor)
	p("")
	p(lr("  "+dim("auto · qwen3 @ ollama · ctx 12k/32k"), bold("?")+dim(" help  ")))

	caption("the gutter column tells the story of the session at a glance:")
	p("  " + c(coral, "❯") + dim(" you   ") + dim("·") + dim(" tool   ") + c(coral, "▍") + dim(" answer   ") + c(violet, "▍") + dim(" thinking   ") + c(errRed, "✗") + dim(" error   ") + c(warning, "⚠") + dim(" approval"))

	caption("approval — one gutter-marked line, mnemonic keys:")
	p(" " + c(warning, "⚠") + " " + txt("shell · rm -rf build/") + strings.Repeat(" ", 12) + bold("a") + dim("llow  ") + bold("A") + dim("lways  ") + bold("d") + dim("eny  ") + bold("e") + dim("dit"))
}

// ───────────────────────── direction B dock panels ─────────────────────────

func dockPanelsB() {
	section("B · DOCK PANELS", "settings/picker/memory/connect all render via chrome.Panel — one seam")

	fmt.Println(dim("  today — chrome.Panel: rounded box, title embedded in the border:"))
	fmt.Println()
	aw := 54
	boxRow := func(r string) {
		pad := aw - vis(r)
		if pad < 0 {
			pad = 0
		}
		p(c(coral, "│") + r + strings.Repeat(" ", pad) + c(coral, "│"))
	}
	label := " Settings › Shell "
	p(c(coral, "╭─") + cb(coral, label) + c(coral, strings.Repeat("─", aw-2-utf8.RuneCountInString(label)+1)+"╮"))
	boxRow(" " + dim("/ ") + txt("filter…"))
	boxRow(" " + dim(strings.Repeat("─", aw-2)))
	boxRow(" " + c(coral, "▸ ") + txt("allow_network") + strings.Repeat(" ", 12) + txt("false"))
	boxRow("   " + dim("timeout_seconds") + strings.Repeat(" ", 10) + dim("120"))
	boxRow("   " + dim("sandbox_backend") + strings.Repeat(" ", 10) + dim("restricted"))
	boxRow(" " + dim("12 settings · [↵] edit · [Esc] close"))
	p(c(coral, "╰" + strings.Repeat("─", aw) + "╯"))

	fmt.Println()
	fmt.Println(dim("  style B — same panel, gutter instead of box; breadcrumb becomes a header line:"))
	fmt.Println()
	g := c(violet, "▍")
	p(" " + g + " " + cb(violet, "settings › shell") + strings.Repeat(" ", 26) + dim("↵ edit · Esc back"))
	p(" " + g + " " + dim("/ ") + txt("filter…"))
	p(" " + g + " " + c(coral, "▸ ") + txt("allow_network") + strings.Repeat(" ", 12) + txt("false"))
	p(" " + g + "   " + dim("timeout_seconds") + strings.Repeat(" ", 10) + dim("120"))
	p(" " + g + "   " + dim("sandbox_backend") + strings.Repeat(" ", 10) + dim("restricted"))
	p(" " + g + " " + dim("12 settings"))

	fmt.Println()
	fmt.Println(dim("  style B — model picker (Alt+M):"))
	fmt.Println()
	p(" " + g + " " + cb(violet, "model"))
	p(" " + g + " " + c(coral, "▸ ") + txt("qwen3:32b @ ollama") + strings.Repeat(" ", 15) + c(teal, "local"))
	p(" " + g + "   " + dim("llama3:70b @ ollama") + strings.Repeat(" ", 14) + dim("local"))
	p(" " + g + "   " + dim("gpt-4.1 @ openai") + strings.Repeat(" ", 17) + dim("remote"))

	fmt.Println()
	fmt.Println(dim("  style B — connect flow collapses to gutter lines:"))
	fmt.Println()
	p(" " + g + " " + cb(violet, "connect") + " " + txt("ollama @ localhost:11434") + "  " + c(teal, "✓ reachable · 12 models"))

	fmt.Println()
	fmt.Println(dim("  style B — pinned todo panel (above live strip and input, Ctrl+T toggles):"))
	fmt.Println()
	p(" " + c(teal, "✓") + " " + dim("scaffold parser package"))
	p(" " + c(teal, "✓") + " " + dim("define token types"))
	p(" " + cb(coral, "▶") + " " + txt("implement expression parsing"))
	p(" " + dim("·") + " " + dim("handle operator precedence"))
	p(" " + dim("·") + " " + dim("add error recovery tests"))
	fmt.Println()
	fmt.Println(dim("  collapsed (Ctrl+T) or when the frame is short:"))
	fmt.Println()
	p(" " + cb(coral, "▶") + " " + dim("tasks 3/7 · implement expression parsing"))

	fmt.Println()
	fmt.Println(dim("  unfocused dock (input has focus) — gutter drops to muted:"))
	fmt.Println()
	gm := dim("▍")
	p(" " + gm + " " + dim("settings › shell"))
	p(" " + gm + " " + dim("▸ allow_network        false"))
}

// ───────────────────────── direction C ─────────────────────────

func directionC() {
	section("C · SINGLE LINE", "1 row of chrome · status lives on the input row · hints only when needed")

	p("  " + txt("The loader merged project config before user config, so"))
	p("  " + txt("project-local values were clobbered. Fixed in load()."))
	p("")
	p("  " + dim("· model → llama3 @ ollama") + strings.Repeat(" ", 20) + dim("state changes print here"))
	p("")
	p(lr(c(coral, "❯")+" "+cursor, dim("auto · qwen3 · 12k   ")+c(coral, "⠹ patching · 4s")+" "))

	caption("approval — transient which-key line appears above, then vanishes:")
	p("  " + c(warning, "⚠") + " " + txt("rm -rf build/") + dim(" → ") + bold("a") + dim(" allow · ") + bold("A") + dim(" always · ") + bold("d") + dim(" deny · ") + bold("e") + dim(" edit"))
	p(lr(c(warning, "⚠")+" "+cursor, dim("auto · qwen3 · 12k")+" "))
}

func main() {
	fmt.Println()
	fmt.Println(dim("  marshal TUI mockups · warm-sunset palette · " + fmt.Sprint(W) + " cols"))
	current()
	directionA()
	directionB()
	dockPanelsB()
	directionC()
	fmt.Println()
	fmt.Println()
}
