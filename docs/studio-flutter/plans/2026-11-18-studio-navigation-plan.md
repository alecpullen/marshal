# Studio navigation: search, Settings in the sidebar, kept preferences

A plan for three decisions made on 2026-11-18 after the Studio audit: the
sidebar's search finds threads, projects and places by title; Settings'
sections take the sidebar's place while Settings is open; and Appearance and
Density are kept on the device. The **Findings** section is the spec.

- **Authored against**: `origin/main` at `958e2ec9`.
- **Runs after**: the [visual fixes](2026-11-18-studio-visual-fixes-plan.md)
  (its task 8 adds `Field.search`), [dead controls](2026-11-18-studio-dead-controls-plan.md)
  (its task 3 makes the preferences reach the window; its task 4 writes the
  address) and [places polish](2026-11-18-studio-places-polish-plan.md) (its
  task 8 gives `NavProject` its earlier threads) plans.
- **Executed as**: one commit per task, `studio-navigation: task N — <title>`.
- **Verification runs on Linux**: `cd app && flutter test …`. Checks that
  need a browser or the desktop app are listed under each task.

## Findings

1. **Search is a placeholder.** The sidebar's field is disabled under the tip
   `Search arrives in a later phase` (`app/lib/src/ui/shell/sidebar.dart:93-107`).
   A thread is found by scrolling the tree or a project's page.
2. **Settings has a second navigation column.** `SettingsPlace` draws its own
   252-wide list of ten sections beside the sidebar
   (`app/lib/src/ui/settings/settings_place.dart:81-143`), and above a
   narrow window it stacks that list over the section. Its open section is
   local state (`_open`, `settings_place.dart:73-78`), so a section change
   skips the shell's unsaved-input guard and never reaches the address.
3. **Appearance and Density are forgotten on reload.** `GeneralPreferences`
   holds them in memory (`app/lib/src/model/general_model.dart:12-58`), and
   General says the bridge will store them "which this build does not read
   yet" (`general_model.dart:231-234`).

## Decisions

1. Search is a jump-to over titles the Studio already holds: no bridge
   route, no transcript text.
2. While Settings is open, the sidebar lists its sections under a **Back**
   row; the search field and New stay above them.
3. Preferences are kept on the device: browser storage on the web, a
   `studio.json` file in Marshal's config directory on the desktop.

## Non-goals

- Searching inside conversations.
- `⌘K` below `Layout.narrow`, where the field is in the closed drawer.
- Keeping preferences in the bridge's config.

## Contracts

```dart
// app/lib/src/model/nav_model.dart

/// What the sidebar's search found, in the order it is drawn and Enter
/// takes: places, then threads, then projects.
@immutable
class NavSearch {
  const NavSearch({
    this.places = const [],
    this.threads = const [],
    this.projects = const [],
  });
  final List<NavPlace> places;
  final List<ThreadRef> threads;
  final List<NavProject> projects;
  bool get isEmpty;
}

/// The places, threads and projects of [fixed] and [projects] whose name or
/// title holds every word of [query], ignoring case. A thread is searched
/// whether it is current or earlier.
NavSearch searchNav(List<NavPlace> fixed, List<NavProject> projects, String query);
```

```dart
// app/lib/src/client/preference_store.dart (new; a conditional import of
// preference_store_stub.dart, _web.dart and _io.dart, as host_tokens.dart is)

/// The small key–value store this device keeps the Studio's preferences in.
abstract interface class PreferenceStore {
  String? read(String key);
  void write(String key, String value);
}

/// This build's store: browser storage on the web, `studio.json` in Marshal's
/// config directory on the desktop, none in a test.
PreferenceStore? openPreferenceStore();
```

```dart
// app/lib/src/model/general_model.dart
GeneralPreferences({
  String? appearance,                          // a saved Appearance name
  String? density,                             // a saved Density name
  void Function(String key, String value)? save,
});
```

```dart
// app/lib/src/ui/kit/field.dart — Field.search gains
//   FocusNode? focusNode;   // used instead of the field's own when given

// app/lib/src/ui/shell/keys.dart
class FocusSearchIntent extends Intent { const FocusSearchIntent(); }

// app/lib/src/ui/shell/sidebar.dart — Sidebar gains
//   final FocusNode? searchFocus;
//   final VoidCallback? onLeaveSettings;   // the Back row in Settings
```

## Task 1: Appearance and density are kept on this device

**Goal.** A reload, or reopening the desktop app, keeps the Appearance and
Density last chosen on that device.

**Files.** `app/lib/src/client/preference_store.dart`,
`app/lib/src/client/preference_store_stub.dart`, `app/lib/src/client/preference_store_web.dart`,
`app/lib/src/client/preference_store_io.dart` (all new),
`app/lib/src/model/general_model.dart`, `app/lib/main.dart`,
`app/test/client/preference_store_io_test.dart` (new),
`app/test/model/general_model_test.dart` (new: no model test covers
`GeneralPreferences` at `958e2ec9`)

**Steps.**
1. `preference_store.dart`: the interface and `openPreferenceStore`
   (Contracts), choosing the implementation with the conditional import
   `host_tokens.dart:1-3` uses, extended with `if (dart.library.io)`.
2. `_stub.dart`: `openPreferenceStore` returns null.
3. `_web.dart`: keys are `marshal:studio:<key>` in `window.localStorage`,
   through `package:web` as `token_store_web.dart` does. Every read and write
   is wrapped so a blocked storage reads as nothing and writes nothing.
4. `_io.dart`: the directory is `MARSHAL_CONFIG_DIR`, else
   `$XDG_CONFIG_HOME/marshal`, else `$HOME/.config/marshal` — the same rule
   as `internal/app/config/locations.go:12-21`. Expose
   `IoPreferenceStore(String directory)` for tests;
   `openPreferenceStore()` builds it from the environment, or returns null
   when `HOME` is unset. The file is `studio.json`, a flat object of strings:
   read once when opened (a missing or malformed file reads as empty), and
   rewritten whole on each write, creating the directory when needed.
5. `GeneralPreferences` takes the saved names and `save` (Contracts). A
   saved name that is not an `Appearance` or `Density` value is ignored.
   `setAppearance` and `setDensity` call `save('appearance', name)` and
   `save('density', name)` after a change.
6. `main.dart:104`: open the store, build `GeneralPreferences` from its
   `appearance` and `density` keys, and pass `save: store.write` when there
   is a store.
7. `general_model.dart:231-234`: `windowOnly` becomes `Kept on this device:
   another browser or computer keeps its own.`; rename it `deviceOnly` and
   update its two uses (`:251`, `:277`).
8. Tests:
   - `preference_store_io_test.dart`: `'a written preference is read back
     after reopening'`, `'a malformed file reads as empty'`, `'the
     directory is created on first write'` — each in a temp directory, never
     the real config.
   - `general_model_test.dart`: `'saved choices open as the preferences'`,
     `'an unknown saved name is ignored'`, `'a change is saved'`.

**Verify.** `cd app && flutter test test/client test/model test/boundary_test.dart test/ui/settings_test.dart`
passes; `boundary_test.dart` confirms `dart:io` and browser APIs stay in
their `client/` files.

**Needs a browser and a desktop build.** Choose Dark, reload the web
Studio: it opens dark. Do the same in the desktop app and check
`~/.config/marshal/studio.json`.

## Task 2: The sidebar's search finds by title

**Goal.** Typing in the sidebar's field replaces the tree with what matches;
Enter opens the first match and Escape clears it.

**Files.** `app/lib/src/model/nav_model.dart`, `app/lib/src/ui/shell/sidebar.dart`,
`app/test/model/nav_model_test.dart`, `app/test/ui/shell_test.dart`

**Steps.**
1. Add `NavSearch` and `searchNav` (Contracts) to `nav_model.dart`. Split
   the query on whitespace; an entry matches when every word occurs in its
   label (a place, a project's name) or title (a thread), ignoring case. A
   blank query finds nothing. Threads keep the tree's order: by project as
   listed, then current before earlier.
2. `_SidebarState` holds a `TextEditingController` for the query. The field
   (`sidebar.dart:93-107`, `Field.search` after the visual fixes) is
   enabled, hint `Search threads and places`, its `Tip` reading `Find a
   thread, project or place (⌘K)`, and `onChanged` rebuilds.
3. While the query is not blank, the sidebar draws, in place of the fixed
   rows, the tree and the foot: under `Label('Places')` each matching place
   as its `NavRow`; under `Label('Threads')` each matching thread as its
   `ThreadRow`; under `Label('Projects')` each matching project as a
   `NavRow` with the folder icon. A group with nothing is not drawn. Nothing
   at all draws one line in `small` `muted`: `Nothing is called “<query>”`.
   The search runs over the host-scoped tree (`_tree`, `sidebar.dart:161`).
4. `onSubmitted` opens the first entry in that order, then clears the
   query. Escape, bound with `CallbackShortcuts` around the field, clears
   the query and lets go of focus. Opening any result also clears it.
5. Tests:
   - `nav_model_test.dart`: `'search matches every word, in any case'`,
     `'search finds earlier threads'`, `'a blank query finds nothing'`.
   - `shell_test.dart`: `'typing filters the sidebar to what matches'`,
     `'Enter opens the first match'`, `'Escape restores the tree'`,
     `'nothing matching says so'`.

**Verify.** `cd app && flutter test test/model/nav_model_test.dart test/ui/shell_test.dart test/tooltips_test.dart test/overflow_test.dart`
passes.

## Task 3: ⌘K goes to search

**Goal.** `⌘K` focuses the sidebar's search from anywhere on a wide window,
including from inside a text field.

**Files.** `app/lib/src/ui/kit/field.dart`, `app/lib/src/ui/shell/keys.dart`,
`app/lib/src/ui/shell/shell.dart`, `app/lib/src/ui/shell/sidebar.dart`,
`docs/studio-flutter/design-system.md`, `app/test/ui/shell/keys_test.dart`

**Steps.**
1. `Field.search` takes `focusNode` (Contracts); `_FieldState` uses it
   instead of its own `_focus` (`field.dart:67`) when given, and does not
   dispose one it was given. design-system.md's `Field.search` line says so.
2. `keys.dart`: add `FocusSearchIntent` and map
   `SingleActivator(LogicalKeyboardKey.keyK, meta: true)` to it in
   `shellShortcuts`; update the doc comment above the map.
3. `_StudioState` owns a `FocusNode` for search, disposed with it, and
   passes it through `Shell` to `Sidebar.searchFocus`, which hands it to the
   field. The Actions map (`shell.dart:863`) handles `FocusSearchIntent` by
   requesting that focus. It does not check `_editingText()`: moving focus
   loses nothing typed.
4. Tests: `'⌘K focuses search'` and `'⌘K from the composer focuses search'`
   in `keys_test.dart`.

**Verify.** `cd app && flutter test test/ui/shell test/kit/field_test.dart test/gallery_test.dart`
passes on Linux.

## Task 4: Settings' sections are the sidebar while Settings is open

**Goal.** In Settings the sidebar lists the ten sections under a Back row,
and a section is a place: it goes through the unsaved-input guard and into
the address.

**Files.** `app/lib/src/ui/shell/sidebar.dart`, `app/lib/src/ui/shell/shell.dart`,
`app/lib/src/ui/settings/settings_place.dart`, `docs/studio-flutter/design-system.md`,
`app/test/ui/settings_test.dart`, `app/test/ui/shell_test.dart`

**Steps.**
1. `Sidebar.build`: when `place.kind` is `PlaceKind.settings`, keep the
   host switcher, the search field and New, then draw, instead of the fixed
   rows, the tree and the foot: `NavRow(label: 'Back', icon:
   Glyphs.chevron_left, onTap: onLeaveSettings)`, `Label('Settings')`, and
   one `NavRow` per `SettingsSectionKind` (`settings_place.dart:6`) with its
   label, selected when its index is `sectionIndex(place)`, opening
   `Place(PlaceKind.settings, section: settingsSections[index])`
   (`app/lib/src/model/place.dart:129`). A search query still replaces all
   of it (task 2).
2. `_StudioState` records the place it left when `_go` (`shell.dart:905`)
   moves into Settings from elsewhere, and `onLeaveSettings` goes back to it,
   or to the Overview when there is none. `Shell` passes `onLeaveSettings`
   to `_sidebar` (`shell.dart:246`), whose `onSelect` already goes through
   `_leave`, the unsaved-input guard.
3. `SettingsPlace` draws only the open section: remove `_list` and the
   wide and narrow layouts (`settings_place.dart:81-143`) and the `_open`
   state; `_section` reads `widget.index`. Update its doc comment.
4. design-system.md, the F2 `NavRow` paragraph: in Settings the sidebar's
   rows are its sections, under a Back row.
5. Tests:
   - `settings_test.dart` `'sections in the specified order'` (`:134`)
     moves to `shell_test.dart` as `'in Settings the sidebar lists its
     sections in order'`, reading the sidebar's `NavRow` labels after
     `Back`.
   - `shell_test.dart`: `'a section opens through the guard'` (an unsaved
     edit in one section asks before another opens), `'Back returns to the
     place before Settings'`, and `'Settings draws no list of its own'`.

**Verify.** `cd app && flutter test test/ui test/states_test.dart test/overflow_test.dart test/tooltips_test.dart`
passes.

**Needs a browser.** Open Settings › Secrets, reload: the address names
`settings/secrets` and the page reopens there.

## Final verification

1. `cd app && dart format --set-exit-if-changed lib test && flutter analyze`.
2. `cd app && flutter test` on Linux, goldens included.
3. The browser and desktop checks under tasks 1 and 4.

## Integration notes

- No Go or contract change.
- After task 4, the places polish plan's Settings gutter finding is moot:
  the section list is the sidebar's.
- `PreferenceStore` is the Studio's own file; nothing in Go reads
  `studio.json`.
