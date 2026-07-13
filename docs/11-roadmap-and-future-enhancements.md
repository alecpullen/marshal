# 11. Roadmap and Future Enhancements

This document outlines suggested features and expansions for subsequent iterations of Marshal.

---

## 1. Onboarding Wizard
- **Auto-Bootstrap**: A CLI startup step that checks if the working directory contains `.marshal/config.toml`. If missing, it prompts the user interactively (e.g. choosing Ollama, OpenRouter, or OpenAI) and automatically generates a standard configuration.
- **Model Auto-Detection**: Dynamically queries the local Ollama instance or the configured API provider to find installed or available model IDs.

---

## 2. Visual TUI Themes — SHIPPED (see `docs/13-project-audit-2026-07-11.md`, batch "TUI themes")

Four named themes (`warm-sunset`, `dracula`, `nord`, `catppuccin-mocha`) plus a `[tui]` TOML block for palette overrides ship in the `feature/tui-themes` batch. `NO_COLOR` still forces monochrome. Light themes and auto-detect remain out of scope.

---

## 3. Fine-Grained Safety Policies
- **Regex Guardrails**: Extend command-level safety checks to support matching arguments or blocking based on regex patterns (e.g., preventing deletions, git resets, or network commands with specific flags).
- **Tool-Specific confirmation**: Allow defining individual safety rules for specific MCP tools rather than just namespace prefixes.

---

## 4. Rich MCP Approval States
- **Arguments inspector**: Display tool input schemas directly in the TUI during approval prompts.
- **Interactive Editing**: Allow editing proposed JSON tool parameters inside a TUI input form before approving and launching the tool.

---

## 5. Token Usage Visualization
- **Budget Panel**: Render real-time token tracking stats (input, output, and budget remaining) in the status bar or a dedicated Swarm details sidebar.
