# TUI interactions

How to read, inspect, select, copy and search the conversation. Every key here
has been verified against the running app; anything that could not be verified on
this machine is marked as such rather than described from the plan.

## The frame

```
┌─ run panel ─────────────────────────────────────────────┐  SDD/swarm progress (only during a run)
│ transcript                                              │
│                                                         │
├─ live strip ────────────────────────────────────────────┤  active swarm, or an open browser session
├─ activity lane ─────────────────────────────────────────┤  "2 agents · 1 job · click or ⏎ inspect agents"
├─ inspector (side or dock) ──────────────────────────────┤  Changes · Agents · Context · Overview
│ composer                                                │
└─ status line ───────────────────────────────────────────┘  mode · focus · find · branch · ctx
```

Every one of these is budgeted out of the same column. The activity lane is
exactly one count row plus its separator, however much is running — it used to be
up to seven rows, subtracted from the transcript at exactly the moment a reader
needs the transcript most.

## Reading

| Key | What it does |
| --- | --- |
| `↑` `↓` | scroll a line |
| `PgUp` `PgDn` | scroll a page |
| `Ctrl+U` `Ctrl+D` | scroll half a page |
| `Home` `End` | top / bottom. `End` resumes following |
| wheel | scrolls the transcript |

The reader's position is an **anchor**: a block identity plus an offset inside it,
never a row number. Resizing the terminal, or new output arriving above, moves
every row below the change — so a row-based position would be wrong by however
many rows reflowed, silently. Scroll away from the bottom and you stay on your
block through any number of resizes.

`End`, or scrolling to the bottom, resumes *following* the live end.

### The reading state is reported

The status line shows `find "query": 3/7` while a search is open, and `N selected`
while text is selected. Both are at priority 2 — above the session identity, below
the mode cue — so they survive a narrow terminal that drops the branch name.

## Inspecting

| Key | What it does |
| --- | --- |
| `Ctrl+B` | toggle the inspector. Side column on a wide terminal, docked panel otherwise |
| `Ctrl+Shift+B` | expand the inspector over the body; press again to return it |
| `F6` / `Shift+F6` | cycle focus forward / back |
| `Tab` / `Shift+Tab` | next / previous tab, on **every** tab including Context |
| `←` `→` | on the Context tab, cycle its scope (pack ↔ last request) |

The inspector has four tabs: **Changes** (working-tree diff against a base ref),
**Agents** (running sub-agents, with their transcripts), **Context** (the pack and
the last request), **Overview** (the same numbers as the side rail).

`Tab` keeps its tab-bar meaning everywhere, including on the Context tab: a panel
where `Tab` sometimes changed tabs and sometimes did something else could not be
learned. The Context tab's scope is cycled by the arrow keys instead, and cycling
it does **not** change the tab on display.

The **side rail** is a separate, read-only surface with its own visibility rule:
it appears at the width set by `tui.side_panel.min_width` and is toggled for the
session by the *Side rail* row in the action palette (`F2`). It has no `Ctrl+B`
binding, because `Ctrl+B` belongs to the inspector — the inspector's Overview is
the rail's content made scrollable and navigable, so one key for both would leave
the more capable surface unreachable.

`/inspect <tab>` opens it by name. A tab that is not implemented cannot be opened
by name — it says so rather than showing an empty panel.

An open detail body survives new snapshots: a refresh updates badges without
replacing what you are reading. Escape backs out of one level at a time —
expanded body → the Context tab's child scope → open detail → focus.

## Selecting and copying

| Key | What it does |
| --- | --- |
| drag over a block's **body** | select text |
| drag over a block's **header** | expand/collapse the block |
| `v` | start a keyboard selection at the reading position |
| arrows | extend the selection (otherwise they scroll) |
| `y` | copy. The selection wins over the block under the reading anchor |
| `Esc` | clear the selection |

Selecting is **application-owned**. The mouse scrolls the transcript and
drag-selects text without releasing capture; `Ctrl+S` hands the mouse back to the
terminal for its own native selection, and takes it back.

A selection is stored as *logical* offsets, so it survives a reflow, and it is
frozen when the gesture ends, so streaming output cannot change bytes you already
chose. The highlight is drawn from the current layout — a highlight is a claim
about cells on screen, so it is re-derived from the live geometry on every rebuild
and follows the text through a resize. If a block's text genuinely changed
underneath a selection, the highlight is dropped rather than tinted over whatever
now sits at those offsets; the copied bytes still come from the frozen snapshot.
A selection stays inside one block: the text between a point in one block and a
point in another is not a range of any single string, and concatenating across
blocks splices two unrelated documents together.

Copy feedback is transient and states the scope: "Copied answer", "Copied code 1
(go)", "Copy selection (42 characters)". Local clipboard helpers are tried first
(`pbcopy`, `wl-copy`, `xclip`, `xsel`); if none is available the app falls back to
a terminal OSC 52 request and says **which** of the two happened, because
"copied" and "asked the terminal" are different claims. Payloads over 100 KiB are
never truncated.

### Terminal-specific selection

There is no portable modifier for native selection while the mouse is captured.
The app therefore does not tell you to "hold Option" or "hold Alt": that claim is
wrong on terminals that do not implement it. The verified route is `Ctrl+S`,
which releases the mouse.

## Finding

| Key | What it does |
| --- | --- |
| `/` | open find, while the **conversation** owns the keys |
| `/find <phrase>` | open find with a query |
| `F3` / `Shift+F3` | next / previous match |
| `Enter` | accept — close, and stay on the match |
| `Esc` | close, and return to where you opened it |
| `Ctrl+F` | unchanged: drill into the most recently started running agent |

`/` stays a slash while the composer owns the keys, because that is how every
slash command is typed. `F3` steps rather than opens: with no search running it
does nothing, so a reflexive press does not put you in a mode you did not choose.

The search runs over the **readable projection** of each block — the text you
actually read — in the conversation currently on screen. Specifically:

- **Markdown is searched as projected.** A list item is found as "item", a link
  by its label, an entity resolved.
- **Tool output is searched as written.** A tool that printed `- item` is not
  rewritten into a bullet.
- **Renderer chrome is not searched.** A list bullet or a table separator is the
  renderer's, so a query for `•` finds nothing.
- **Hidden content is not searched.** A skill body or a sub-agent report reaches
  the model and is deliberately not drawn, so a jump to it would be a lie.
- **A capped tool result is disclosed.** The status line says the search covered
  captured output that may itself be truncated, rather than implying it covered
  everything.

An open search stays in step with the conversation: the result list is rebuilt on
every transcript rebuild, so output that streams in while you are searching is
searchable as it arrives, and the count in the status line describes the text you
are actually looking at. Stepping to a match that cannot be scrolled to at the
current width says so in the status line rather than moving the cursor silently.

Matches are highlighted in the transcript through the same cell mapping the click
and selection offsets come from, so a highlight cannot drift from the text it
marks. The match you are on is painted differently from the others: you need to
see both which lines matched and which one "next" counts from.

**Scope limit, stated plainly:** only a *final* answer has a cell mapping, so a
match in a user prompt, a narration line or a system notice can be jumped to but
not tinted. That is pinned by a test rather than left to be discovered.

### Archived conversations

Find searches the conversation **on screen**. Earlier generations live in
`/history` — `/history search <term>` — which reads the database. A find that
silently ranged over every past session would move you somewhere you did not ask
to go.

## The activity lane

One separator rule and one count row, whatever is running:

```
▍─────────────
▍⠋ 2 agents · 1 job · click or ⏎ inspect agents
```

Clicking **any** row of it — including the separator — opens the inspector's
Agents tab, which lists the running children with their models, elapsed times and
transcripts. Before this was consolidated the lane had one row per child and those
rows drilled in directly; the count now reaches the same information and more.

With nothing running, the lane renders nothing at all and costs no rows.

## Actions

`F2` opens the action palette, which lists every action available *right now* with
the reason for anything that is not. `?` prints the keybinding cheatsheet.

The footer and the palette resolve from one snapshot, so they cannot disagree
about what a key does.

## Settings

The panel is labelled **Inspector** in `/settings`, because that is what the rest
of the app calls it. The TOML keys stay `tui.side_panel.*` — renaming a key to
match a label would break every config file already on disk.

```toml
[tui.side_panel]
enabled   = true   # show the inspector beside the conversation
min_width = 80     # frame width at which it appears
width_pct = 25     # percentage of the frame it occupies
```

## No colour

Under `NO_COLOR` every state is still distinguishable: focus has a glyph and a
label, the untrusted warning is text, and the status line reports the mode, the
focus and any reading state in words.
