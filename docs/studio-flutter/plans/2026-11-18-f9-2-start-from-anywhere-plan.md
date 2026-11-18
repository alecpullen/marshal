# F9.2 — start work from anywhere

The plan for Batch B of [F9](../f9-parity.md): a repository can be connected,
`⌘N` and `⌘1/⌘2/⌘3` work anywhere in the Studio, New is in the narrow app bar
and the sidebar, and Automations has a New action and per-tab counts.

- **Spec**: [../f9-parity.md](../f9-parity.md), Batch B and decisions 1, 5, 6.
- **Authored against**: `walk-main` at `b8bfefd8`.
- **Executed as**: one commit per task, `<plan-slug>: task N — <task title>`.
- **Verification runs on Linux**: `cd app && flutter test …`.

## Non-goals

- Host onboarding (being planned separately; the switcher stays read-only).
- Creating a project: `POST /api/projects` is dropped in
  [route-parity](../route-parity.md), so connecting a repository registers a
  repository (F9 decision 1).
- Starting an agent from an issue (`POST /api/repos/X/issues/X/spawn` is
  dropped).
- Editing a schedule or recipe from a place other than the one that owns it.

## Assumptions

- `registerRepo` takes a whole `Repo`
  (`app/lib/src/client/bridge_client.dart:684`), and the bridge accepts it when
  `id` is 1–64 characters of letters, digits, `.`, `_` or `-` and `url` is
  present (`web/bridge/repos_http.go:184-188`). It fills `ownerID` itself.
- `_Sections` (`app/lib/main.dart:307`) already owns one `_Automations` holder
  with the four models, built once per window, so counts and create actions
  can come from it.
- `Shell` is built in one place (`_StudioState.build`,
  `app/lib/src/ui/shell/shell.dart:592`), so one wrapper around it is every
  place's keyboard.

## Contracts

```dart
// app/lib/src/model/connect_repo_model.dart

/// Registering a repository from the Studio: the id the URL suggests, the
/// bridge's refusal in its own words, and the discovery that follows.
class ConnectRepoModel extends ChangeNotifier {
  ConnectRepoModel(BridgeClient client);

  bool get busy;
  String? get failure;

  /// The repository the bridge registered, once it has.
  Repo? get registered;

  /// The id a URL suggests: the last two path segments, joined by '-', lower
  /// cased, with anything outside `[a-z0-9._-]` dropped and runs of '-' folded.
  /// `https://github.com/acme/marshal.git` becomes `acme-marshal`.
  static String idFor(String url);

  Future<void> register({
    required String url,
    required String id,
    String forge = 'github',
    String? credRef,
    String? branch,
  });

  /// Discovers the repository just registered: what the discovery report
  /// answers is not shown here, only whether it ran.
  Future<void> discover();
}
```

```dart
// app/lib/src/ui/automations/automations_place.dart

/// The tab bar every Automations section draws, with the action the place adds
/// beside it.
class AutomationsTabs {
  const AutomationsTabs({
    required this.items,
    required this.index,
    required this.onChanged,
    this.trailing,
  });

  final List<TabItem> items;
  final int index;
  final ValueChanged<int> onChanged;

  /// The place's own control, drawn at the end of the tab row.
  final Widget? trailing;
}
```

```dart
// app/lib/src/ui/sections/section_page.dart — new parameter
//   final Widget? trailing;   // drawn beside the tabs, when there are tabs

// app/lib/src/ui/automations/automations_place.dart — new parameters
//   final Map<int, int?> counts;                          // by tab index
//   final Future<void> Function(int tab, BuildContext)? onNew;
```

```dart
// app/lib/src/ui/shell/shell.dart — new parameters on Shell and Sidebar
//   final VoidCallback? onNew;        // the place-level New, for the app bar
//                                       and the sidebar
//   final Future<void> Function()? onNewFromKeys;  // ⌘N (Studio)
//   final VoidCallback? onConnectRepo;             // the sidebar's plus
```

## Task 1: Connect a repository from the sidebar

**Goal.** The plus beside *Projects* opens a sheet that registers a
repository, and on success offers the discovery that makes its issues appear.

**Files.**
- `app/lib/src/model/connect_repo_model.dart` (new)
- `app/lib/src/ui/shell/connect_repo_sheet.dart` (new)
- `app/lib/src/ui/shell/sidebar.dart`
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/main.dart`
- `app/test/model/connect_repo_model_test.dart` (new)
- `app/test/ui/shell/connect_repo_test.dart` (new)

**Steps.**

1. `connect_repo_model.dart`: implement `ConnectRepoModel` as Contracts
   declares. `idFor` splits the URL on `/`, drops empty segments, takes the
   last two, joins with `-`, removes a trailing `.git`, lower-cases, replaces
   anything outside `[a-z0-9._-]` with `-`, folds runs of `-` and trims them.
   `register` sets `busy`, clears `failure`, awaits
   `client.registerRepo(Repo(id: id, url: url, ownerID: '', forge: forge,
   credRef: credRef, branch: branch))`, stores `registered`, notifies; a
   thrown object becomes `failure` through `'$e'`. `discover` returns when
   `registered` is null; otherwise awaits
   `client.discoverRepo(registered!.id)` and turns a throw into `failure`.

2. `connect_repo_sheet.dart`: `Future<void> showConnectRepoSheet(BuildContext
   context, ConnectRepoModel model)`. A `Sheet` titled `Connect a repository`
   with: a `Field` for the URL; the suggested id in a second `Field` (editable,
   prefilled from `ConnectRepoModel.idFor` as the URL changes); a `Menu` for
   the forge (`GitHub`, `Gitea`); a `Field` for an optional credential
   reference and one for an optional branch; a footer line saying a repository
   is registered for issue intake and that a project appears when the daemon
   discovers one — not when a repository is registered; `Btn.primary('Connect
   the repository')` disabled while `busy` or the URL is empty; on
   `model.registered != null` the sheet swaps to the registered URL with
   `Btn('Discover its issues', onTap: model.discover)`; `model.failure` is
   shown as a `StateMessage.fault` with `StateAction('Try again', …)`.

3. `sidebar.dart`: the `Label('Projects')` at `:106-113` becomes a `Row` of an
   `Expanded(child: Label('Projects'))` and
   `IconBtn(Glyphs.plus, tooltip: 'Connect a repository', onTap:
   widget.onConnectRepo)`, drawn only when `onConnectRepo != null`. Add the
   field to `Sidebar` beside `onHost`.

4. `shell.dart`: add `onConnectRepo` to `Shell` and to `_sidebar`
   (`:157-166`). `Studio` gains `connectRepoFor: ConnectRepoModel
   Function(String host)?` beside `newWorkFor`, and passes
   `onConnectRepo: () => unawaited(_openConnectRepo(context))` down; that
   method builds the model for `_host` (the same host rule as `_content`) and
   shows the sheet.

5. `main.dart`: `connectRepoFor: (host) => ConnectRepoModel(
   hosts.hosts.firstWhere((h) => h.name == host).client)`, beside
   `newWorkFor` (`:146`).

6. Tests.
   - `app/test/model/connect_repo_model_test.dart`: `'the id comes from the
     repository URL'` asserts `idFor('https://github.com/acme/marshal.git') ==
     'acme-marshal'`, and that a trailing slash and a query string do not
     change it; `'registering stores the repository'` asserts
     `registered!.id`; `'a refusal is reported in the bridge's words'`
     asserts `failure` and a null `registered`.
   - `app/test/ui/shell/connect_repo_test.dart`: pump `Studio` with
     `connectRepoFor`, tap `find.byTooltip('Connect a repository')`, type a
     URL, tap `Connect the repository`, and expect the registered URL and
     `Discover its issues` to appear.

**Verify.** `cd app && flutter test test/model/connect_repo_model_test.dart test/ui/shell/connect_repo_test.dart test/ui/shell_test.dart`
— the new tests and every sidebar/shell test pass.

## Task 2: ⌘N and ⌘1/⌘2/⌘3 work everywhere

**Goal.** From any place, `⌘N` opens the new-work dialog for the project in
view, `⌘1/⌘2/⌘3` go to Overview, Inbox and Pull requests, and none of them
fires while someone is typing.

**Files.**
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/src/ui/shell/keys.dart` (new)
- `app/test/ui/shell/keys_test.dart` (new)

**Steps.**

1. `keys.dart`: declare the intents the shell handles, so the mapping is in
   one place and testable without a widget:

   ```dart
   class NewWorkIntent extends Intent { const NewWorkIntent(); }
   class GoOverviewIntent extends Intent { const GoOverviewIntent(); }
   class GoInboxIntent extends Intent { const GoInboxIntent(); }
   class GoPullsIntent extends Intent { const GoPullsIntent(); }

   /// The shell's shortcuts (f9-parity.md decision 6): ⌘N, and ⌘1/⌘2/⌘3 for
   /// Overview, Inbox and Pull requests.
   final Map<ShortcutActivator, Intent> shellShortcuts = {
     const SingleActivator(LogicalKeyboardKey.keyN, meta: true): const NewWorkIntent(),
     const SingleActivator(LogicalKeyboardKey.digit1, meta: true): const GoOverviewIntent(),
     const SingleActivator(LogicalKeyboardKey.digit2, meta: true): const GoInboxIntent(),
     const SingleActivator(LogicalKeyboardKey.digit3, meta: true): const GoPullsIntent(),
   };
   ```

2. `shell.dart`: in `_StudioState.build`, wrap the `Shell` it returns in
   ```dart
   Shortcuts(
     shortcuts: shellShortcuts,
     child: Actions(
       actions: {
         NewWorkIntent: CallbackAction<NewWorkIntent>(onInvoke: (_) { _newWorkFromKeys(); return null; }),
         GoOverviewIntent: CallbackAction<GoOverviewIntent>(onInvoke: (_) { _go(const Place.overview()); return null; }),
         GoInboxIntent: CallbackAction<GoInboxIntent>(onInvoke: (_) { _go(const Place(PlaceKind.inbox)); return null; }),
         GoPullsIntent: CallbackAction<GoPullsIntent>(onInvoke: (_) { _go(const Place(PlaceKind.pulls)); return null; }),
       },
       child: Focus(autofocus: true, child: shell),
     ),
   )
   ```
   where `_go` is the body of the `onSelect` callback the `Shell` already
   receives (`shell.dart:597`) — extract it to a method so both paths clear
   `_notice` and set `_place` the same way — and `_newWorkFromKeys` returns
   immediately when `FocusManager.instance.primaryFocus?.context?.widget is
   EditableText`, so a composer keeps its `⌘N`.

3. `_newWorkFromKeys` resolves the project from the place, in this order: the
   thread place's thread (`_threadRef(_place)`), a project place's root, then
   `_firstProject()`; with none it returns without opening anything, because
   the dialog has no project to belong to.

4. `app/test/ui/shell/keys_test.dart`:
   - `'⌘N opens the new-work dialog from the Overview'`: pump `Studio` over a
     `ModelFixture`, send `LogicalKeyboardKey.keyN` with meta, expect
     `find.text('Start a new agent')`;
   - `'⌘2 opens the Inbox'`: send digit 2 with meta, expect the Inbox's title;
   - `'⌘N does nothing while a composer has focus'`: open a thread place, focus
     its composer's field, send `⌘N`, expect no `Start a new agent` sheet.

**Verify.** `cd app && flutter test test/ui/shell/keys_test.dart test/ui/thread/browse_keys_test.dart`
— the new keys and the thread's own keys both pass.

## Task 3: New in the narrow app bar and the sidebar

**Goal.** On a narrow window the app bar has a plus, and the sidebar has one
beside its search field; both start work the way the page headers do.

**Files.**
- `app/lib/src/ui/shell/shell.dart`
- `app/lib/src/ui/shell/sidebar.dart`
- `app/test/ui/shell_test.dart`

**Steps.**

1. `shell.dart`: add `final VoidCallback? onNew;` to `Shell`, passed to
   `Sidebar` and drawn in `_appBar()` (`:197-232`) as an `IconBtn(Glyphs.plus,
   tooltip: 'New', onTap: widget.onNew)` between the place title and the Inbox
   icon, when `onNew != null`.

2. `sidebar.dart`: draw the same control beside the search field
   (`:82-96`), inside the `Padding` that holds `Tip`/`Field`, as a `Row` of the
   expanded search tip and the button; add `final VoidCallback? onNew;`.

3. `Studio` passes `onNew: _firstProject() == null ? null : () =>
   _newWorkFromKeys()` — the same resolution Task 2 built, so all three
   entry points agree.

4. `app/test/ui/shell_test.dart`, three cases added to the existing file:
   - `'the narrow app bar has New'`: at 600×900, expect
     `find.byTooltip('New')`;
   - `'the sidebar has New beside search'`: at 1200×900, expect the same
     tooltip in the sidebar;
   - `'New starts work in the first project'`: tap it and expect the
     new-work sheet.

**Verify.** `cd app && flutter test test/ui/shell_test.dart test/ui/f2_flow_test.dart`
— the shell's own tests and the phase flow pass.

## Task 4: Automations tabs carry their counts

**Goal.** Each Automations tab shows how many entries it has, and the section
page can draw the place's own control beside its tabs.

**Files.**
- `app/lib/src/ui/automations/automations_place.dart`
- `app/lib/src/ui/sections/section_page.dart`
- `app/lib/src/ui/automations/bots_tab.dart`, `schedules_tab.dart`,
  `watches_tab.dart`, `recipes_tab.dart`
- `app/lib/main.dart`
- `app/test/ui/automations_test.dart`

**Steps.**

1. `section_page.dart`: add `final Widget? trailing;` and, where the page
   draws its tabs (`:102-110`), replace the bare `Tabs(tabs: tabs, index:
   tabIndex, onChanged: onTab ?? (_) {})` with a `Row` of
   `Expanded(child: Tabs(…))` and `if (trailing != null) trailing!`.

2. `automations_place.dart`: add `trailing` to `AutomationsTabs` as
   Contracts declares; add `final Map<int, int?> counts;` (default `const {}`)
   to `AutomationsPlace`; build the tab items as
   `TabItem(label, count: widget.counts[i])`.

3. Each of the four tabs passes its bar through to `SectionPage` with
   `trailing: tabs?.trailing`.

4. `main.dart` `_Sections.automations` (`:335`): build `counts` from the models
   it already holds and pass it — `models.schedules.items.length`,
   `models.watches.items.length`, `models.recipes.items.length`, and for Bots
   the number of forge rows the project has (the project's `Automations`:
   review bot, CI fixer, auto-fix — `null` when the project is empty, as the
   planned tab has nothing to count). `items` is the list
   `SectionModel` exposes (`schedules_model.dart:45` already reads it).

5. `app/test/ui/automations_test.dart`: `'every tab carries its count'` pumps
   `AutomationsPlace` with fixtures of known length and expects the counts on
   the four `TabItem`s.

**Verify.** `cd app && flutter test test/ui/automations_test.dart test/ui/watches_test.dart test/ui/sections_test.dart`
— the Automations, watches and section-page tests pass.

## Task 5: New automation

**Goal.** The Automations place has one New automation action whose choices
start a schedule, a watch or a recipe in the tab that owns that form.

**Files.**
- `app/lib/src/ui/automations/automations_place.dart`
- `app/lib/src/ui/automations/schedules_tab.dart`
- `app/lib/src/ui/automations/recipes_tab.dart`
- `app/lib/main.dart`
- `app/test/ui/automations_test.dart`

**Steps.**

1. `schedules_tab.dart`: extract the create it already performs through
   `createLabel: 'New schedule'` and `blank: SchedulesModel.blank`
   (`section_page.dart:104` opens the blank detail) into a public
   `static Future<void> create(BuildContext context, SchedulesModel model)`
   that opens the same form for `SchedulesModel.blank()`. `SectionPage` keeps
   its own primary button, which calls the same helper.

2. `recipes_tab.dart`: the same for its create (the tab's own `createLabel`
   or its copy flow, whichever it already draws); expose
   `static Future<void> create(BuildContext context, RecipesModel model)`.

3. `automations_place.dart`: add `final Future<void> Function(int tab,
   BuildContext context)? onNew;` as Contracts declares. Build the tab bar's
   `trailing` as
   ```dart
   Menu(
     choices: [
       MenuChoice('Schedule', 'A cron entry that runs a recipe',
           onSelect: () => widget.onNew?.call(1, context)),
       MenuChoice('Watch', 'Watch a command, a job or a file',
           onSelect: () => widget.onNew?.call(2, context)),
       MenuChoice('Recipe', 'A prompt a schedule or a person can run',
           onSelect: () => widget.onNew?.call(3, context)),
     ],
     anchor: (context, toggle) =>
         Btn('New automation', icon: Glyphs.plus, onTap: toggle),
   )
   ```
   — tab 0 is Bots, which has no create of its own, so the menu offers the
   three that do. `Menu`, `MenuChoice` and the anchor callback are the shape
   `thread_header.dart:123-140` already draws with.

4. `main.dart` `_Sections.automations`: supply
   ```dart
   onNew: (tab, context) => switch (tab) {
     1 => SchedulesTab.create(context, models.schedules),
     2 => showWatchForm(context, models.watches),
     _ => RecipesTab.create(context, models.recipes),
   },
   ```
   importing `watch_form.dart`.

5. `app/test/ui/automations_test.dart`: `'New automation starts a schedule'`
   taps `New automation`, chooses `Schedule`, and expects the schedule form
   for a blank schedule; `'the menu offers only the kinds that can be made'`
   expects three choices and no `Bots`.

**Verify.** `cd app && flutter test test/ui/automations_test.dart test/ui/sections_test.dart`
— the new cases and the section-page tests pass.

## Final verification

1. `cd app && flutter analyse`.
2. `cd app && flutter test` — the whole Dart suite, including
   `design_rules_test.dart`, `tooltips_test.dart` and `boundary_test.dart`
   (this plan adds two icon-only controls, so `tooltips_test.dart` must find
   their tooltips).
3. `cd app && flutter test test/route_parity_test.dart` — `POST /api/repos`
   already has a caller; the row stays valid.
4. Manual, last: `cd app && flutter build web --base-href / --no-web-resources-cdn --output ../web/bridge/static`
   then `scripts/dev-daemon.sh`.

## Integration notes

- No Go change: `POST /api/repos`, `POST /api/repos/X/discover`,
  `POST /api/watches` and `PUT /api/schedules/X` all exist and already have
  client methods.
- `ui/` gains no `client/` import: the repository sheet reads
  `ConnectRepoModel`, which lives in `model/` and may import the client.
- The keys wrapper is the only global state this plan adds; it sits above the
  place, not inside it, so no screen gains keyboard policy.
- `_Sections` keeps its one-holder-per-window lifetime; counts therefore
  refresh when the section reloads its model, which is the same moment its
  rows do.
