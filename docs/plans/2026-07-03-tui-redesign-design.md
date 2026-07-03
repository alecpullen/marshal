# TUI Redesign: Approach 1 (Dual-Column with Dynamic Sidebar Tabs)

This document specifies the design for transitioning the Marshal terminal interface from a basic scrolling output to a full-screen, responsive, and stylish multi-panel Terminal User Interface (TUI).

## Goals & Philosophy
* **Inspectable & Fast:** Provide immediate visibility of agent plans, active files, and tool audits without cluttered screens.
* **Minimalist & Clean:** Use clean rounded borders, consistent typography, and a quiet, professional color palette.
* **Responsive:** Adapt layout dynamically to terminal size, ensuring readable borders and wrapped text.

---

## 1. UI Structure & Layout Design

The TUI uses `tea.WithAltScreen()` to run in full-screen mode, dividing the screen space as follows:

```text
┌───────────────────────────────── Marshal ─────────────────────────────────┐
│                                                                           │
│  [Left Column - 70% Width]                  [Right Column - 30% Width]     │
│  ┌──────────────────────────────────────┐  ┌── [1] Plan ── [2] Context ──┐ │
│  │ User: fix the tests                  │  │                             │ │
│  │ Agent: Running go test...            │  │   ● Parse config file       │ │
│  │                                      │  │   ○ Fix parser logic        │ │
│  │                                      │  │   ○ Verify fixes            │ │
│  │                                      │  │                             │ │
│  │                                      │  │                             │ │
│  └──────────────────────────────────────┘  │                             │ │
│  ❯ Ask Marshal...                          └─────────────────────────────┘ │
│                                                                           │
├───────────────────────────────────────────────────────────────────────────┤
│ MARSHAL │ 🤖 qwen2.5-coder:14b │ 📁 /workspace │ 🔒 local │ 💾 backup-ready │
└───────────────────────────────────────────────────────────────────────────┘
```

### Panels
1. **Left Column (Chat & Input):**
   * **Chat Viewport:** Displays the scrollable session transcript.
   * **Input Box:** Pinned text input at the bottom of the left column.
   * **Approval/Diff Overlay:** When `state.PendingApproval()` is active:
     * If a diff is proposed, the left column splits vertically: **Left half** shows the diff; **Right half** shows the security approval box (Reason, Risk, Command).
2. **Right Column (Sidebar Inspector):**
   * A bounded container displaying three tab options:
     * **Plan (1):** Lists current session tasks (e.g. `✓ done`, `● active`, `○ pending`).
     * **Context (2):** Shows active files loaded into the LLM context pack with token estimates.
     * **Log (3):** Chronological log of recent tool executions and outcomes.
3. **Footer Status Bar:**
   * A solid bar at the absolute bottom of the screen displaying system info, connection mode, and active LLM configuration.

---

## 2. Interaction Model & Navigation

| Action / Hotkey | Scope | Description |
|---|---|---|
| `Esc` | Input Focused | Unfocuses input box, enabling viewport scroll keys and tab hotkeys. |
| `Enter` | Input Unfocused | Focuses input box to begin typing. |
| `Ctrl+C` | Global | Gracefully shuts down the agent session and exits alternate screen. |
| `Ctrl+P` | Global | Focuses/switches Right Sidebar to the **Plan** tab. |
| `Ctrl+K` | Global | Focuses/switches Right Sidebar to the **Context** tab. |
| `Ctrl+T` | Global | Focuses/switches Right Sidebar to the **Tool Log** tab. |
| `1` / `2` / `3` | Input Unfocused | Directly switch Right Sidebar tabs. |
| `Up` / `Down` | Input Unfocused | Scroll the Chat viewport history. |
| `PageUp` / `PageDown` | Input Unfocused | Page-scroll the Chat viewport history. |
| `Ctrl+R` | Global | Reverts/rolls back the last applied file patch. |

---

## 3. Styling & Lip Gloss Token Spec

* **Border Style:** `lipgloss.RoundedBorder()` with padding `(0, 1)`.
* **Palette:**
  * **Accent / Active:** Teal (`#00AFD7`) or Violet (`#5F5FD7`).
  * **Inactive / Border:** Charcoal / Gray (`#303030` or `#4E4E4E`).
  * **Text Main:** Foreground terminal default.
  * **Status Active:** Background Indigo (`#5F5FD7`), Foreground White (`#FFFFFF`).
  * **Success:** Green (`#5FAF5F`).
  * **Warning:** Yellow (`#D7AF00`).
  * **Critical:** Red (`#D75F5F`).
* **Text Wrapping:** Ensure chat messages, diffs, and logs are explicitly wrapped using `lipgloss.Width` and `runewidth` constraints to prevent visual clipping.

---

## 4. Settings TUI Integration

When Settings is opened (`Ctrl+O`):
* The TUI displays the settings configuration panel centered on the screen (replacing the primary dashboard view).
* We use `lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, settingsView)` to position the panel.
* If canceled (`Esc`) or saved (`Ctrl+S`), the TUI returns to the main multi-panel dashboard layout.

