import 'dart:async';

import 'package:flutter/services.dart' show SystemNavigator;

import '../../model/age.dart';
import '../../model/composer_model.dart';
import '../../model/connect_repo_model.dart';
import '../../model/factory_store.dart';
import '../../model/handoff_model.dart';
import '../../model/new_work_model.dart';
import '../../model/fleet_store.dart';
import '../../model/inbox_model.dart';
import '../../model/inbox_model.dart' as inbox;
import '../../model/inspector_state.dart';
import '../../model/nav_model.dart';
import '../../model/overview_model.dart'
    show AuditEvent, UsageReport, buildOverview;
import '../../model/place.dart';
import '../../model/place_host.dart' show placeHost;
import '../../model/planning_model.dart';
import '../../model/project_model.dart';
import '../../model/route_translation.dart';
import '../../model/signals_store.dart';
import '../../model/source_state.dart';
import '../../model/pulls_model.dart';
import '../../model/revision_model.dart';
import '../../model/run_model.dart';
import '../../model/share_model.dart';
import '../../model/stack_store.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/thread_view_state.dart';
import '../../model/work_item_model.dart'
    show WorkItemView, WorkItemWorker, threadOfItem;
import '../inbox/answerable_ask.dart';
import '../inbox/inbox.dart' show Inbox;
import '../factory/handoff_sheet.dart';
import '../factory/planning_screen.dart';
import '../factory/revision_sheet.dart';
import '../factory/run_screen.dart';
import '../factory/work_item_screen.dart';
import '../kit/kit.dart';
import '../overview/overview.dart' show Overview;
import '../project/project_page.dart';
import '../pulls/fix_control.dart';
import '../pulls/merge_control.dart';
import '../pulls/pulls_place.dart';
import '../sections/unsaved_guard.dart';
import '../new_work/new_work_dialog.dart';
import '../thread/share_sheet.dart';
import '../thread/thread_composer.dart';
import '../thread/thread_screen.dart';
import 'connect_repo_sheet.dart';
import 'host_switcher.dart';
import 'keys.dart';
import 'placeholder.dart';
import 'sidebar.dart';

/// Builds the handoff review for a plan (F6 decision 8), bound to the frozen
/// capture the review was opened from when the caller has one.
///
/// It names the host, the project and the title rather than a thread, because
/// a plan is handed off from two places: a planning thread, and the new-work
/// dialog's "Work item", which a project page or the Overview can open
/// without any thread of its own.
///
/// It is asynchronous because what the review inherits is the project's own
/// settings, read when the person hands the plan off (F6 decision 12).
typedef HandoffBuilder = Future<HandoffModel> Function(
  String host,
  String project,
  String title,
  String plan,
  String spec, {
  String? plannerID,
  PlannerSnapshot? snapshot,
});

/// Builds a per-host place for [host] (spec D1): Automations, Library and
/// Settings each read one bridge, so each is built from the host the
/// switcher's scope resolves to.
typedef HostPlaceBuilder = Widget? Function(
  String host,
  int section,
  StateKind? state,
);

/// One open thread's inspector: the view state the frame animates, what each
/// tab draws, and how to release both.
class ThreadInspector {
  const ThreadInspector({
    required this.state,
    required this.body,
    required this.start,
    this.dispose,
  });

  final InspectorState state;
  final Widget Function(InspectorTab tab) body;

  /// Starts the reads the tab models own. Called once, when the thread place
  /// opens: a tab that draws what its model holds has nothing to draw until
  /// the holder's own read runs.
  final VoidCallback start;

  /// Releases the tab models and the state. Called when the thread place
  /// closes.
  final VoidCallback? dispose;
}

/// Builds a thread's inspector, from the host's client. Null leaves a thread
/// without the panel, which is what every thread had before F9.
typedef InspectorBuilder = ThreadInspector Function(
  String host,
  model.ThreadRef thread,
);

/// The Studio's frame (design-system.md §4): the sidebar, and the place's
/// content beside it. Below 760 the navigation becomes a panel over the
/// content, opened from the app bar the narrow layout needs.
///
/// It takes data and callbacks and never a store: the same [NavModel] and
/// content make the same shell in a test as on screen (§5).
class Shell extends StatefulWidget {
  const Shell({
    super.key,
    required this.nav,
    required this.hosts,
    required this.place,
    required this.content,
    required this.onSelect,
    this.state,
    this.now,
    this.onRetry,
    this.unsaved,
    this.notice,
    this.automationsFor,
    this.libraryFor,
    this.settingsFor,
    this.signals,
    this.onConnectRepo,
    this.onNew,
    this.onLeaveSettings,
    this.searchFocus,
  });

  final NavModel nav;
  final List<HostView> hosts;
  final Place place;

  /// What the place draws: the Overview, the Inbox, or a place's promise.
  ///
  /// It is a builder rather than a widget because a place's own controls
  /// leave it too — the Overview's rows, the Inbox's asks — and they must
  /// ask the one guard through the same consultation the sidebar and the
  /// app bar use (design-system.md §9).
  final Widget Function(PlaceNavigation navigation) content;

  /// Opens a place.
  final void Function(Place place) onSelect;

  /// The fleet's data state, when it is not ready (§7). The sidebar is still
  /// drawn; the content area says what the state is.
  final StateKind? state;

  /// What ages are measured against.
  final DateTime? now;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

  /// The guard that asks before the shell leaves a place holding unsaved
  /// edits (design-system.md §9, F7 decision 3).
  final UnsavedGuard? unsaved;

  /// What to say above the place, when the link that opened it names nothing
  /// this build knows (F8 decision 2): one line, and never a state of its
  /// own.
  final String? notice;

  /// Builds the Automations place for the host the switcher's scope resolves
  /// to (F7 decision 4, D1). Null leaves the place planned, so a host that
  /// wires no bridge never draws a section whose read could only fail.
  final HostPlaceBuilder? automationsFor;

  /// Builds the Library place for the resolved host (F7 decision 5).
  final HostPlaceBuilder? libraryFor;

  /// Builds the Settings place for the resolved host (F7 decision 7).
  final HostPlaceBuilder? settingsFor;

  /// Every host's tripped watches, blocked requests and held drafts (spec D4).
  /// Null leaves the Overview's Automations card with nothing to list, so a
  /// test or an embedded host that supplies none still builds.
  final SignalsStore? signals;

  /// Connects a repository (F9 B1): the plus beside *Projects*. Null leaves
  /// the control out; the sheet is the Studio's, so the shell only offers the
  /// way into it.
  final VoidCallback? onConnectRepo;

  /// The place-level New (F9 B3): the narrow app bar's plus and the sidebar's
  /// plus both call it, so they start work the way the page headers do. Null
  /// leaves both out — there is no project to start work in.
  final VoidCallback? onNew;

  /// Where Settings' Back row goes: the place Settings was opened from, named
  /// by the screen that knows it (F2: a section is a place, and the whole of
  /// Settings is one too). Null leaves the row without a destination of its
  /// own.
  final VoidCallback? onLeaveSettings;

  /// The node the sidebar's search field draws its keyboard from, so `⌘K` can
  /// put the keyboard there from anywhere on a wide window. Null leaves the
  /// field its own node.
  final FocusNode? searchFocus;

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  /// The host the tree shows, or null for every host.
  String? _host;

  /// The narrow layout's navigation panel is open.
  bool _panel = false;

  DateTime get _now => widget.now ?? DateTime.now();

  @override
  Widget build(BuildContext context) {
    // A scope naming a host the fleet no longer lists reads as All hosts (see
    // [placeHost]); it is cleared here, before anything is drawn, so the
    // switcher's anchor and the places that read a host agree on the one they
    // show from this frame on.
    final host = _host;
    if (host != null && !widget.hosts.any((each) => each.name == host)) {
      _host = null;
    }
    final narrow = MediaQuery.sizeOf(context).width < Layout.narrow;
    if (!narrow) {
      return Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(width: Layout.sidebar, child: _sidebar(inPanel: false)),
          const Hairline.vertical(),
          Expanded(child: _content()),
        ],
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _appBar(),
        Expanded(child: _panel ? _sidebar(inPanel: true) : _content()),
      ],
    );
  }

  Widget _sidebar({required bool inPanel}) => Sidebar(
    nav: widget.nav,
    hosts: widget.hosts,
    place: widget.place,
    host: _host,
    onHost: _setHost,
    onSelect: (place) => _leave(() => widget.onSelect(place)),
    onConnectRepo: widget.onConnectRepo,
    onNew: widget.onNew,
    // Settings' Back row is a way out of a place like any other, so it asks
    // the one guard through [_leave] as the rows do (design-system.md §9).
    onLeaveSettings: widget.onLeaveSettings == null
        ? null
        : () => _leave(widget.onLeaveSettings!),
    now: widget.now,
    inPanel: inPanel,
    searchFocus: widget.searchFocus,
  );

  /// The place's content, handed the shell's one way of leaving: the sidebar,
  /// the app bar and whatever the place itself draws all come through
  /// [_leave], so no path can leave a dirty place without asking.
  Widget _place() => widget.content(
    PlaceNavigation(
      go: (place) => _leave(() => widget.onSelect(place)),
      unsaved: widget.unsaved,
    ),
  );

  /// Narrows the tree — and the host a per-host place reads — to [host], or
  /// to every host when it is null.
  ///
  /// The switcher is a way of leaving the place as much as the sidebar is, so
  /// it goes through [_leave] too (design-system.md §9, decision 3): a place
  /// holding an unsaved edit is asked about before the host changes under it.
  void _setHost(String? host) => _leave(() => setState(() => _host = host));

  /// Opens [place], after asking about an unsaved edit when there is one:
  /// the shell is where "leaving" happens, so it is where the one guard is
  /// consulted (design-system.md §9).
  Future<void> _leave(VoidCallback go) async {
    final guard = widget.unsaved;
    if (guard != null && guard.hasUnsavedEdits) {
      final leave = await guard.confirm(context);
      if (!leave || !mounted) return;
    }
    if (!mounted) return;
    setState(() => _panel = false);
    go();
  }

  /// The bar the narrow layout needs: the way into the navigation, the place
  /// it is showing, and the Inbox one tap away.
  Widget _appBar() {
    final e = Ember.of(context);
    return Column(
      children: [
        SizedBox(
          height: Sizes.touch,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: Space.s8),
            child: Row(
              children: [
                IconBtn(
                  _panel ? Glyphs.xmark : Glyphs.sidebar_left,
                  tooltip: _panel
                      ? 'Close the navigation'
                      : 'Projects and threads',
                  onTap: () => setState(() => _panel = !_panel),
                ),
                const SizedBox(width: Space.s6),
                Expanded(
                  child: Text(
                    placeTitle(widget.place, widget.nav),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: e.bodyStrong,
                  ),
                ),
                if (widget.onNew != null) ...[
                  IconBtn(Glyphs.plus, tooltip: 'New', onTap: widget.onNew),
                  const SizedBox(width: Space.s6),
                ],
                IconBtn(
                  Glyphs.tray,
                  tooltip: 'Inbox',
                  onTap: () => _leave(
                    () => widget.onSelect(const Place(PlaceKind.inbox)),
                  ),
                ),
              ],
            ),
          ),
        ),
        const Hairline(),
      ],
    );
  }

  /// The place's content, or the state that stands in for it (§7).
  Widget _content() {
    final body = _body();
    final notice = widget.notice;
    if (notice == null) return body;
    // One line above the place, never a state of its own: the place it
    // opened is a real one, and the line says why it is not the one the link
    // named (F8 decision 2).
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _noticeLine(notice),
        const Hairline(),
        Expanded(child: body),
      ],
    );
  }

  /// The line a link from the old Studio shows: what happened, in the kit's
  /// quietest role, with no control of its own.
  Widget _noticeLine(String notice) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s12,
        Layout.pageSide,
        Space.s12,
      ),
      child: Text(notice, style: e.small.tint(e.muted)),
    );
  }

  /// The three places F7 built read one bridge each, so each is built for the
  /// host the switcher's scope resolves to rather than for every host at once
  /// (spec D1).
  static const _perHost = {
    PlaceKind.automations,
    PlaceKind.library,
    PlaceKind.settings,
  };

  /// The place when it is one the shell builds per host: the switcher above
  /// it, naming the host it read, and the place itself.
  ///
  /// Every other place answers null, and [_body] draws it as it did. A place
  /// this shell has no builder for answers null too: a switcher above a place
  /// that reads nothing would change nothing, and the place it draws — the
  /// promise, or whatever the content builder draws — is drawn without it.
  Widget? _hostPlace() {
    final kind = widget.place.kind;
    if (!_perHost.contains(kind)) return null;
    final build = switch (kind) {
      PlaceKind.automations => widget.automationsFor,
      PlaceKind.library => widget.libraryFor,
      PlaceKind.settings => widget.settingsFor,
      _ => null,
    };
    if (build == null) return null;
    // No host is known only while the fleet lists none, and that is a fleet
    // state of its own (§7): the place is drawn as it was before the scope
    // reached it, without a switcher over an empty list.
    final host = placeHost(widget.hosts, _host);
    if (host == null) return null;
    // A builder that answers nothing for this host leaves the place to the
    // content builder, which is what drew it before the scope reached it.
    final place =
        build(host, sectionIndex(widget.place), widget.state) ?? _place();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          // The place's own page inset, so the switcher sits on the title's
          // left edge, with the vertical spacing the sidebar's switcher takes.
          padding: const EdgeInsets.fromLTRB(
            Layout.pageSide,
            Space.s12,
            Layout.pageSide,
            Space.s8,
          ),
          // Aligned left rather than stretched: the switcher is as wide as its
          // label, as it is in the sidebar.
          child: Align(
            alignment: Alignment.centerLeft,
            child: HostSwitcher(
              hosts: widget.hosts,
              selected: host,
              onSelect: _setHost,
              now: _now,
            ),
          ),
        ),
        Expanded(child: place),
      ],
    );
  }

  /// The place's content, without the notice.
  Widget _body() {
    final content = _hostPlace() ?? _place();
    final state = widget.state;
    if (state == null) return content;
    if (state == StateKind.stale) {
      // Stale keeps the last data and marks it: never present old data as
      // current, and never infer failure from a lost connection.
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Layout.pageSide,
              Layout.pageTop,
              Layout.pageSide,
              0,
            ),
            child: StateMessage.stale(
              age: _oldestContact(),
              detail: 'The fleet has not answered; this is what it last said.',
            ),
          ),
          Expanded(child: content),
        ],
      );
    }
    return PageBody(
      title: placeTitle(widget.place, widget.nav),
      children: [_stateMessage(state)],
    );
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the fleet again',
      onTap: widget.onRetry ?? () {},
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message:
            'No projects yet: a project appears here once a daemon reports '
            'one.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The fleet could not be read.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: widget.hosts.isEmpty ? 'The daemon' : widget.hosts.first.name,
        lastSeen: _oldestContact(),
        action: retry,
      ),
      // A refused token is why nothing is here, and no retry clears one: the
      // state names the host that refused and how to sign in again.
      StateKind.unauthorized => StateMessage.unauthorized(
        host: _refusingHost(),
      ),
      StateKind.stale => StateMessage.stale(age: _oldestContact()),
      StateKind.planned => StateMessage.planned(
        name: placeTitle(widget.place, widget.nav),
        note: 'Not built yet',
      ),
    };
  }

  /// How long ago the fleet last said anything: the age a stale row states.
  String _oldestContact() {
    DateTime? oldest;
    for (final host in widget.hosts) {
      final contact = host.lastContact;
      if (contact == null) continue;
      if (oldest == null || contact.isBefore(oldest)) oldest = contact;
    }
    return relativeAge(oldest ?? _now, _now);
  }

  /// The host that refused this page's token, for the state that says so.
  String _refusingHost() {
    for (final host in widget.hosts) {
      if (host.refused) return host.name;
    }
    return widget.hosts.isEmpty ? 'The daemon' : widget.hosts.first.name;
  }
}

/// The shell's state, from what the fleet holds (§7).
///
/// A host whose token the bridge refused is `unauthorized`, never
/// `unavailable`: the two look the same from the page — nothing arrives — but
/// only one of them is about reachability, and sending a reader to check the
/// network when they need to sign in is the wrong answer to give.
StateKind? fleetStateKind({
  required bool read,
  required bool failed,
  required bool anyAgents,
  required List<HostView> hosts,
}) {
  if (!read) return StateKind.loading;
  if (hosts.isEmpty) return failed ? StateKind.error : StateKind.empty;
  if (!anyAgents && hosts.every((host) => !host.live)) {
    // Nothing is showing, so the page says why. A refusal is the one cause a
    // reader can act on, so it is named ahead of the silence beside it.
    return hosts.any((host) => host.refused)
        ? StateKind.unauthorized
        : StateKind.unavailable;
  }
  if (hosts.any((host) => !host.live)) return StateKind.stale;
  return null;
}

/// The Studio: what the fleet holds, drawn at one place.
///
/// It is the one place that reads the stores, and it hands the shell data
/// (F2 decision 3): the sidebar, the Overview and the Inbox never touch a
/// store themselves.
class Studio extends StatefulWidget {
  const Studio({
    super.key,
    required this.store,
    required this.inbox,
    this.pulls,
    this.factory,
    this.read,
    this.readAudit,
    this.readUsage,
    this.threadStore,
    this.composerFor,
    this.newWorkFor,
    this.shareFor,
    this.inspectorFor,
    this.connectRepoFor,
    this.planningFor,
    this.handoffFor,
    this.runFor,
    this.answerRunFor,
    this.rosterFor,
    this.projectFor,
    this.automationsFor,
    this.libraryFor,
    this.settingsFor,
    this.signals,
    this.density = Density.comfortable,
    this.initialPlace = const Place.overview(),
    this.startPage,
  });

  final FleetStore store;
  final InboxModel inbox;

  /// The pull requests: their place, their ready count in the sidebar, and
  /// the one click a failing card offers. Null leaves the place planned, so a
  /// host that wires no bridge never draws one as working.
  final PullsModel? pulls;

  /// The work items, when the app holds them: a work item is a thread of its
  /// own kind, with its stage, its activity and its five tabs (F6 decisions
  /// 13 and 14). Null leaves a work item's place unbuilt rather than drawing
  /// an empty thread.
  final FactoryStore? factory;

  /// Reads the fleet: discovery when the app starts, and again when a person
  /// asks.
  final Future<void> Function()? read;

  /// Reads the newest audit events from the first host that answers, for the
  /// Overview's activity feed. Null leaves the feed empty rather than
  /// promising one.
  final Future<List<AuditEvent>> Function()? readAudit;

  /// Reads the usage report the Overview's spend rail draws, grouped by [by]:
  /// the day series for its spark and the role series for its split. Null
  /// leaves the rail with today's spend alone.
  final Future<UsageReport> Function({String by})? readUsage;

  /// Builds the store a thread place reads its session into. Null draws a
  /// thread place as planned: a test or an embedded host that supplies none
  /// gets the promise, never a socket.
  final StackStore Function(model.ThreadRef thread)? threadStore;

  /// Builds a thread place's composer. Null leaves the thread without one.
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;

  /// Builds the new-work dialog's model for one host and project root. Null
  /// leaves `⌘N` and every "New" without a dialog.
  ///
  /// It is keyed by the project, not by a thread: the Overview and a project
  /// page start work too, and neither has a thread to start it from. Those
  /// entry points are the design's (design.md, "Starting work", and the mock's
  /// `Btn('New', primary: true, icon: plus)` in both page headers).
  final NewWorkModel Function(String host, String project)? newWorkFor;

  /// Builds the connect-repository sheet's model for one host (F9 B1). Null
  /// leaves the sidebar's plus out: a host that wires no bridge cannot
  /// register a repository.
  final ConnectRepoModel Function(String host)? connectRepoFor;

  /// Builds a thread's Share model, from the host's client (F7 decision 7).
  /// Null leaves the header's Share disabled: a control that cannot work is
  /// shown as unavailable, never as working.
  final ShareModel Function(String host, String agentId)? shareFor;

  /// Builds a thread's inspector, from the host's client (F9 decision 4).
  /// Null leaves a thread without the panel, which is what every thread had
  /// before F9.
  final InspectorBuilder? inspectorFor;

  /// Builds a planning thread's model, for a planner a person started (F6
  /// decision 8). Null leaves the planning place unbuilt rather than drawing
  /// an empty one.
  final PlanningModel Function(String host, String plannerID)? planningFor;

  /// Builds the handoff review for a plan (F6 decision 8). Null leaves the one
  /// action that hands a plan off out, rather than offering a sheet that
  /// cannot create an item.
  ///
  /// It is asynchronous because what the review inherits is the project's own
  /// settings, read when the person hands the plan off (F6 decision 12).
  final HandoffBuilder? handoffFor;

  /// Builds a plan run's model for one agent (F6 decision 11). Null leaves a
  /// plan run's place unbuilt rather than drawing an empty run.
  final RunModel Function(String host, String agentID)? runFor;

  /// Answers the question a run stopped on (F6 decision 11). Null leaves a
  /// run's question as a fact: no action is offered that could not be taken.
  final Future<void> Function(String host, String agentID, String answer)?
  answerRunFor;

  /// Reads a session's roster, so a plan run's lanes name the roles they drew
  /// and the models behind them (GET /api/sessions/{id}/roster). Null leaves
  /// the lanes without a roles strip rather than drawing bindings nobody read.
  final Future<Roster> Function(String host, String agentID)? rosterFor;

  /// Builds a project's page model for one host and root (F6 decision 12).
  /// Null leaves the project place planned, so a host that wires no settings
  /// never draws an editor that cannot save.
  final ProjectModel Function(String host, String root)? projectFor;

  /// Builds the Automations place for one host at one of its sections (F7
  /// decision 4, D1). Null leaves the place planned, so a host that wires no
  /// bridge never draws a section whose read could only fail; the builder
  /// itself answers null while no host has answered yet.
  final HostPlaceBuilder? automationsFor;

  /// Builds the Library place for one host at one of its sections (F7
  /// decision 5).
  final HostPlaceBuilder? libraryFor;

  /// Builds the Settings place for one host at one of its sections (F7
  /// decision 7).
  final HostPlaceBuilder? settingsFor;

  /// Every host's tripped watches, blocked requests and held drafts (spec D4).
  /// Null leaves the Overview's Automations card with nothing to list, so a
  /// test or an embedded host that supplies none still builds.
  final SignalsStore? signals;

  /// The density a thread opens at, as Settings' General section set it: what
  /// General picks is what a transcript starts as. A thread's own switch still
  /// overrides it for that thread, which is the only place it can be changed.
  final Density density;

  /// The place a deep link names.
  final Place initialPlace;

  /// The place the page's own address names, when the app was started by
  /// opening a link (F8 decision 2). Null leaves [initialPlace] in charge: a
  /// test, and a window the tray opened, name their place.
  final StartPlace? startPage;

  @override
  State<Studio> createState() => _StudioState();
}

class _StudioState extends State<Studio> with WidgetsBindingObserver {
  late Place _place = widget.initialPlace;

  /// The place the page's own address names, once this screen has written it
  /// (F8 decision 2). Null until then: a screen the page handed no place never
  /// writes one, so a test and the tray's window leave the address alone.
  Place? _written;

  /// What to say above the place when the link that opened this screen named
  /// nothing this build knows (F8 decision 2).
  String? _notice;
  bool _read = false;
  String? _failure;

  /// The place Settings was entered from, so its Back row returns there
  /// (F2: Settings is a place, and Back leaves it the way it was entered).
  Place? _beforeSettings;

  /// The newest audit events the last read answered, newest first: the
  /// Overview's activity feed. Empty when no read has succeeded.
  List<AuditEvent> _audit = const [];

  /// The spend's day series and role split, as the last read answered them;
  /// null when no read has succeeded.
  UsageReport? _spendByDay;
  UsageReport? _spendByRole;

  /// The usage ledger read by project: what the projects table joins for its
  /// spend column. Null when no read has succeeded.
  UsageReport? _spendByProject;

  /// The one guard every place leaving this screen consults: the shell asks
  /// it, and a place that edits watches the models it holds (F7 decision 3).
  final _unsaved = UnsavedGuard();

  /// The node the sidebar's search field draws its keyboard from. The screen
  /// owns it so `⌘K` can put the keyboard there from anywhere — including
  /// from inside another text field, which loses nothing typed by moving
  /// focus.
  final _searchFocus = FocusNode();

  /// The planning threads this screen started, by planner id (F6 decision 8).
  /// A planner is none of the things the fleet lists, so the screen that
  /// started it is the only one that can name its place.
  final _planners = <String, model.ThreadRef>{};

  /// The plan runs this screen opened, by agent id (F6 decision 11). A run is
  /// an agent's session, so the agent id is its identity.
  final _runs = <String, model.ThreadRef>{};

  /// The digest each run this screen opened carried, by agent id: a run a
  /// project page lists is drawn from the list's own digest rather than a
  /// second read.
  final _runDigests = <String, Map<String, dynamic>>{};

  @override
  void initState() {
    super.initState();
    // A page with an address keeps one history entry per place the person
    // opens, so Back walks them rather than leaving the Studio, and the
    // address's own arrivals are this screen's to open (F8 decision 2).
    //
    // Only a page that was handed the address it opened at does either: a
    // test, and a window the tray opened, have none to keep, and must not
    // touch the platform's navigation at all.
    if (widget.startPage != null) {
      unawaited(SystemNavigator.selectMultiEntryHistory());
      WidgetsBinding.instance.addObserver(this);
    }
    _followPage();
    _reread();
  }

  /// Opens the place the page's own address names, and writes that place back
  /// as this build's path for it (F8 decision 2), so a reload and Back land on
  /// it.
  ///
  /// The entry the page opened on is always replaced: a hash written for the
  /// Svelte Studio, and a bare `/` that names the overview only by being the
  /// root, are both addresses this screen would not be given on the way back —
  /// `/` is the root route, which the surrounding app answers itself — so the
  /// address must name the place the way this build writes it.
  ///
  /// A screen that was handed a place — a test, the tray — is left alone: only
  /// a page that named no place is followed.
  void _followPage() {
    final page = widget.startPage;
    if (page == null) return;
    _place = page.place;
    // The address names this place once it is written below, so the first
    // navigation after it is a push rather than a second write of the same.
    _written = page.place;
    _notice = page.notice;
    SystemNavigator.routeInformationUpdated(
      uri: Uri.parse(formatPlace(page.place)),
      replace: true,
    );
  }

  @override
  void dispose() {
    if (widget.startPage != null) {
      WidgetsBinding.instance.removeObserver(this);
    }
    _unsaved.dispose();
    _searchFocus.dispose();
    super.dispose();
  }

  Future<void> _reread() async {
    final read = widget.read;
    if (read != null) {
      try {
        await read();
      } catch (error) {
        _failure = describeFailure(error);
      }
    }
    final readAudit = widget.readAudit;
    if (readAudit != null) {
      try {
        _audit = await readAudit();
      } catch (_) {
        // The feed is a nicety: a bridge that will not answer leaves the card
        // empty rather than failing the page.
        _audit = const [];
      }
    }
    final readUsage = widget.readUsage;
    if (readUsage != null) {
      try {
        _spendByProject = await readUsage(by: 'project');
        _spendByDay = await readUsage(by: 'day');
        _spendByRole = await readUsage(by: 'role');
      } catch (_) {
        // The rail keeps today's spend; the table's column, the trend and the
        // split say nothing rather than something untrue.
        _spendByProject = null;
        _spendByDay = null;
        _spendByRole = null;
      }
    }
    if (!mounted) return;
    setState(() => _read = true);
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([
        widget.store,
        widget.inbox,
        if (widget.signals != null) widget.signals!,
      ]),
      builder: (context, _) {
        final store = widget.store;
        final place = _place;
        final nav = buildNavModel(
          place,
          store,
          pullsReady: widget.pulls?.readyCount ?? 0,
          factory: widget.factory,
        );
        final shell = Shell(
          nav: nav,
          hosts: store.hosts,
          place: place,
          state: _state(store),
          content: (navigation) => _content(place, nav, navigation),
          onSelect: _go,
          onRetry: _reread,
          now: store.now,
          unsaved: _unsaved,
          notice: _notice,
          automationsFor: widget.automationsFor,
          libraryFor: widget.libraryFor,
          settingsFor: widget.settingsFor,
          signals: widget.signals,
          onConnectRepo: widget.connectRepoFor == null
              ? null
              : () => unawaited(_openConnectRepo(context)),
          // The place-level New (F9 B3): the narrow app bar and the sidebar
          // start work the way the page headers do, through the same
          // resolution `⌘N` uses — but unguarded, because a tap does not take
          // the keyboard from a composer and a button that asked the key path
          // would do nothing while one held it.
          onNew: _firstProject() == null ? null : _newWork,
          onLeaveSettings: _leaveSettings,
          searchFocus: _searchFocus,
        );
        // The shell's keys (f9-parity decision 6): one Shortcuts above the
        // place, so `⌘N` and `⌘1/⌘2/⌘3` work wherever a person is rather than
        // only inside a thread. Nothing below gains keyboard policy.
        return Shortcuts(
          shortcuts: shellShortcuts,
          child: Actions(
            actions: {
              NewWorkIntent: CallbackAction<NewWorkIntent>(
                onInvoke: (_) {
                  _newWorkFromKeys();
                  return null;
                },
              ),
              // A text field keeps its own keys, so `⌘1/⌘2/⌘3` typed into a
              // composer are text rather than navigation, exactly as `⌘N` is
              // (f9-parity decision 6). One guard, one spelling.
              GoOverviewIntent: CallbackAction<GoOverviewIntent>(
                onInvoke: (_) {
                  if (_editingText()) return null;
                  _go(const Place.overview());
                  return null;
                },
              ),
              GoInboxIntent: CallbackAction<GoInboxIntent>(
                onInvoke: (_) {
                  if (_editingText()) return null;
                  _go(const Place(PlaceKind.inbox));
                  return null;
                },
              ),
              GoPullsIntent: CallbackAction<GoPullsIntent>(
                onInvoke: (_) {
                  if (_editingText()) return null;
                  _go(const Place(PlaceKind.pulls));
                  return null;
                },
              ),
              // `⌘K` is the one key a text field does not keep (f9-parity
              // decision 6): moving the keyboard to the search loses nothing
              // typed, so this asks no guard and works from a composer.
              FocusSearchIntent: CallbackAction<FocusSearchIntent>(
                onInvoke: (_) {
                  _searchFocus.requestFocus();
                  return null;
                },
              ),
            },
            child: Focus(autofocus: true, child: shell),
          ),
        );
      },
    );
  }

  /// Opens [next] as the screen's place, whether a row, a control or a key
  /// asked for it: one path, so every entry point clears the notice and sets
  /// the place the same way (f9-parity decision 6).
  void _go(Place next) {
    setState(() {
      // The notice was about the link that opened the screen, not about the
      // place the person has chosen since.
      _notice = null;
      // Entering Settings from elsewhere is remembered, so its Back row can
      // return there: a section is a place, and moving between them stays
      // inside Settings, where the way in has already been recorded.
      if (next.kind == PlaceKind.settings &&
          _place.kind != PlaceKind.settings) {
        _beforeSettings = _place;
      }
      _place = next;
    });
    _writeAddress(next);
  }

  /// Settings' Back row: the place Settings was opened from, or the Overview
  /// when nothing was — a link that names Settings has no place before it to
  /// return to (F8 decision 2).
  void _leaveSettings() => _go(_beforeSettings ?? const Place.overview());

  /// Writes [place] into the page's address as a new history entry, so Back
  /// returns to the place before it (F8 decision 2). A push, not a replace:
  /// the place a person opens is a page they can go back from.
  ///
  /// A place the address already names is not written again, and a screen the
  /// page handed no address writes nothing at all — a test and the tray's
  /// window have no page to name.
  void _writeAddress(Place place, {bool replace = false}) {
    if (widget.startPage == null) return;
    if (!replace && _written == place) return;
    _written = place;
    SystemNavigator.routeInformationUpdated(
      uri: Uri.parse(formatPlace(place)),
      replace: replace,
    );
  }

  /// Opens the place the address has arrived at: Back, Forward and a reload
  /// all come through here, so each lands on the place the address names
  /// (F8 decision 2).
  ///
  /// The address names it already, so nothing is written back. A place with an
  /// unsaved edit asks first, through the guard a click asks; a person who
  /// keeps editing stays put, and the address that moved on is put back to the
  /// place still on screen, replacing the entry that arrived.
  @override
  Future<bool> didPushRouteInformation(
    RouteInformation routeInformation,
  ) async {
    // The whole address is read, not just its path: a place carries its
    // inspector keys in the query and a link written for the old Studio in the
    // fragment, and both are how the entry names what it opens.
    final uri = routeInformation.uri;
    final place = startPlace(
      path: uri.hasQuery ? '${uri.path}?${uri.query}' : uri.path,
      hash: uri.fragment.isEmpty ? '' : '#${uri.fragment}',
    ).place;
    if (place != _place && !await _mayLeave()) {
      _writeAddress(_place, replace: true);
      return true;
    }
    if (!mounted) return true;
    _written = place;
    _go(place);
    return true;
  }

  /// Whether the place on screen may be left: yes when nothing is being
  /// edited, and otherwise whatever the person says to the one guard every
  /// place leaving this screen asks (F7 decision 3).
  Future<bool> _mayLeave() async {
    if (!_unsaved.hasUnsavedEdits) return true;
    return _unsaved.confirm(context);
  }

  /// The shell's state, from what the store holds.
  StateKind? _state(FleetStore store) => fleetStateKind(
    read: _read,
    failed: _failure != null,
    anyAgents: store.agents().isNotEmpty,
    hosts: store.hosts,
  );

  /// What the place draws. A place whose screen a later step of F2 brings
  /// says so (design-system.md §7).
  ///
  /// The Overview and the Inbox leave through the shell's guarded navigation,
  /// never by setting the place here: a row or an ask can no more leave a
  /// dirty place without asking than the sidebar can.
  Widget _content(Place place, NavModel nav, PlaceNavigation navigation) {
    if (place.kind == PlaceKind.overview) {
      return Overview(
        overview: buildOverview(
          widget.store,
          widget.inbox,
          audit: _audit,
          pulls: widget.pulls?.views ?? const [],
          usageByProject: _spendByProject,
          usageByDay: _spendByDay,
          usageByRole: _spendByRole,
        ),
        now: widget.store.now,
        hosts: widget.store.hosts,
        automations: widget.signals?.tripped ?? const [],
        manyHosts: widget.signals?.manyHosts ?? false,
        pulls: widget.pulls?.views ?? const [],
        state: _state(widget.store),
        onSelect: navigation.go,
        onAnswer: widget.inbox.answer,
        onRetry: _reread,
        // The Overview's "New" (design.md, "Starting work"): it starts in the
        // first project the fleet lists, and the dialog's Project chip offers
        // the rest, so the default is a starting point rather than a limit.
        // A place without a builder draws no button: the same guard the
        // thread and the project place use.
        onNewWork: widget.newWorkFor == null
            ? null
            : switch (_firstProject()) {
                (final host, final project) => () => unawaited(
                  _openNewWork(navigation, host, project),
                ),
                null => null,
              },
      );
    }
    if (place.kind == PlaceKind.inbox) {
      return Inbox(
        asks: widget.inbox.asks,
        pulls: widget.pulls?.views ?? const [],
        now: widget.store.now,
        state: _state(widget.store),
        sources: sourceStates(widget.store, factory: widget.factory),
        onSelect: navigation.go,
        onAnswer: widget.inbox.answer,
        onRetry: _reread,
      );
    }
    if (place.kind == PlaceKind.pulls) {
      final pulls = widget.pulls;
      // A host with no pull model has no index to draw: the place says which
      // phase wires it rather than drawing an empty list (§7).
      if (pulls == null) return plannedPlace(place, nav);
      return PullsPlace(
        groups: pulls.groups,
        now: widget.store.now,
        onRetry: _reread,
        actionsFor: (context, pull) => pullCardActions(
          context,
          pull: pull,
          model: pulls,
          // Reviewing and opening the source land on the thread: its review
          // head is where a pull request is read (decision 13).
          review: _openThread(navigation, pull),
          openSource: _openThread(navigation, pull),
        ),
      );
    }
    if (place.kind == PlaceKind.project) {
      final root = place.id;
      final build = widget.projectFor;
      // A host that wires no settings leaves the place planned rather than
      // drawing an editor whose Save could only fail.
      if (root == null || build == null) return plannedPlace(place, nav);
      final host = _hostOfProject(root);
      return _ProjectPlace(
        key: ValueKey('project/$host/$root'),
        root: root,
        host: host,
        build: build,
        factory: widget.factory,
        store: widget.store,
        guard: navigation.unsaved,
        now: widget.store.now,
        onOpenItem: (item) =>
            navigation.go(Place(PlaceKind.thread, host: host, id: item.id)),
        onOpenAgent: (agent) =>
            navigation.go(Place(PlaceKind.thread, host: host, id: agent.id)),
        onOpenSession: (session) => navigation.go(
          Place(PlaceKind.thread, host: session.host, id: session.id),
        ),
        onOpenRun: (run) => _openRun(navigation, host, run),
        // The tab a row names, or the place's first tab when it names none.
        onOpenAutomations: (tab) => navigation.go(
          tab == null
              ? const Place(PlaceKind.automations)
              : Place(PlaceKind.automations, section: automationsSections[tab]),
        ),
        onNewWork: widget.newWorkFor == null
            ? null
            : () => unawaited(_openNewWork(navigation, host, root)),
      );
    }
    if (place.kind == PlaceKind.thread) {
      final build = widget.threadStore;
      final thread = _threadRef(place);
      final factory = widget.factory;
      if (thread != null && thread.kind == model.ThreadKind.planRun) {
        final digest = _runDigests[thread.id];
        final build = widget.runFor;
        if (build != null || digest != null) {
          final answerRun = widget.answerRunFor;
          return _RunPlace(
            key: ValueKey('run/${thread.host}/${thread.id}'),
            thread: thread,
            run: build?.call(thread.host, thread.id),
            digest: digest,
            answer: answerRun == null
                ? null
                : (words) => answerRun(thread.host, thread.id, words),
            roster: widget.rosterFor == null
                ? null
                : () => widget.rosterFor!(thread.host, thread.id),
            now: widget.store.now,
          );
        }
      }
      if (thread != null && thread.planning) {
        final plan = widget.planningFor?.call(thread.host, thread.id);
        if (plan != null) {
          return _PlanningPlace(
            key: ValueKey('planning/${thread.host}/${thread.id}'),
            thread: thread,
            plan: plan,
            handoffFor: widget.handoffFor,
            now: widget.store.now,
            onOpenItem: (id) => navigation.go(
              Place(PlaceKind.thread, host: thread.host, id: id),
            ),
          );
        }
      }
      if (thread != null &&
          factory != null &&
          thread.kind == model.ThreadKind.workItem) {
        return _WorkItemPlace(
          key: ValueKey('work-item/${thread.host}/${thread.id}'),
          thread: thread,
          factory: factory,
          pulls: widget.pulls,
          threadStore: build,
          composerFor: widget.composerFor,
          now: widget.store.now,
          density: widget.density,
          onRetry: () => unawaited(factory.refresh(thread.host)),
        );
      }
      if (build != null && thread != null) {
        return _ThreadPlace(
          key: ValueKey('${thread.host}/${thread.id}'),
          thread: thread,
          buildStore: build,
          composerFor: widget.composerFor,
          density: widget.density,
          // The thread's own ⌘N opens the Studio's one dialog, naming the
          // thread's host and project; the dialog may then move to another
          // host (spec D10, P8 plan 2 decision 6).
          shareFor: widget.shareFor,
          inspectorFor: widget.inspectorFor,
          // The thread's New buttons open the Studio's one dialog; its own ⌘N
          // does too, but behind the shell's guard against a composer that has
          // the keyboard (F9 decision 6). The dialog opens on this thread's
          // host and may move to another (spec D10, P8 plan 2 decision 6).
          onNewWork: widget.newWorkFor == null ? null : _newWork,
          onNewWorkFromKeys: widget.newWorkFor == null
              ? null
              : _newWorkFromKeys,
          asks: [
            for (final ask in widget.inbox.asks)
              if (ask.thread?.host == thread.host &&
                  ask.thread?.id == thread.id)
                ask,
          ],
          onAnswer: widget.inbox.answer,
          now: widget.store.now,
          pulls: widget.pulls,
        );
      }
      return plannedPlace(place, nav);
    }
    return plannedPlace(place, nav);
  }

  /// The thread a place names: a planning thread the person started, a work
  /// item the factory reports, or an agent the fleet still lists. A work item
  /// and a planner are looked up first, because their ids are their own
  /// namespaces and no agent can share one.
  model.ThreadRef? _threadRef(Place place) {
    final id = place.id;
    if (id == null) return null;
    final host = place.host;
    // A link that named no host — one written for the one-bridge Studio (F8
    // decision 2) — is answered by whichever host lists the thread. The
    // screen's own bookkeeping is asked first: a planner or a run this screen
    // started is not in the fleet's lists.
    if (host != null) return _threadOn(host, id);
    for (final candidate in _namesEveryHost()) {
      final thread = _threadOn(candidate, id);
      if (thread != null) return thread;
    }
    return null;
  }

  /// Every host the fleet reports, in discovery order.
  Iterable<String> _namesEveryHost() => [
    for (final host in widget.store.hosts) host.name,
  ];

  /// The thread [id] on [host], when that host lists it.
  model.ThreadRef? _threadOn(String host, String id) {
    final planner = _planners[id];
    if (planner != null && planner.host == host) return planner;
    final run = _runs[id];
    if (run != null && run.host == host) return run;
    final item = widget.factory?.byId(host, id);
    if (item != null) return threadOfItem(host, item);
    // A session the bridge's index lists is a thread of its own (P7). The two
    // cannot collide: the index leaves out the sessions agents run in, so an
    // agent's id and a session's name the same thread only by impossible
    // accident.
    final session = widget.store.session(host, id);
    if (session != null) {
      return model.threadOfSession(host, session.root, session.row);
    }
    for (final agent in widget.store.agents(host: host)) {
      if (agent.id == id) return model.threadFor(host, agent);
    }
    return null;
  }

  /// Remembers a planner a person started, so its place resolves to a planning
  /// thread (F6 decision 8): a planner is none of the things the fleet lists,
  /// so the screen that started it is the only one that can name it.
  void _rememberPlanner(String host, String project, StartedPlanner planner) {
    setState(() {
      _planners[planner.id] = model.ThreadRef(
        host: host,
        id: planner.id,
        project: planner.projectRoot.isEmpty ? project : planner.projectRoot,
        title: 'Planning',
        kind: model.ThreadKind.agent,
        status: model.ThreadStatus.running,
        headline: 'Planning in ${planner.workspaceID}',
        lastActive: widget.store.now,
        planning: true,
      );
    });
  }

  /// Opens the thread a pull request came from, when the fleet still lists it.
  /// Null leaves the card's button disabled rather than opening nothing.
  VoidCallback? _openThread(PlaceNavigation navigation, PullView pull) {
    final thread = pull.thread;
    if (thread == null) return null;
    return () => navigation.go(
      Place(PlaceKind.thread, host: thread.host, id: thread.id),
    );
  }

  /// The host whose fleet lists [root]. A project place carries no host of its
  /// own, and its page's model needs one client; a project no host lists yet
  /// is read from the first host there is.
  String _hostOfProject(String root) {
    for (final host in widget.store.hosts) {
      for (final project in widget.store.projects(host: host.name)) {
        if (project.root == root) return host.name;
      }
    }
    final hosts = widget.store.hosts;
    return hosts.isEmpty ? 'local' : hosts.first.name;
  }

  /// A project to start new work in when the place names none: the first the
  /// fleet lists, with the host that lists it.
  ///
  /// The Overview is every host's, so it has no project of its own. The
  /// dialog's Project chip then offers the rest of that host's projects, which
  /// makes this a starting point rather than a restriction. Null means no host
  /// reports a project, and a spawn would have nowhere to run.
  (String, String)? _firstProject() {
    for (final host in widget.store.hosts) {
      final projects = widget.store.projects(host: host.name);
      if (projects.isEmpty) continue;
      return (host.name, projects.first.root);
    }
    return null;
  }

  /// The host a repository connected from the sidebar is registered on: the
  /// place's own, else the one that lists the first project, else the first
  /// host there is. Null only when the fleet lists no host at all.
  String? _hostForConnect() {
    final place = _place;
    if (place.kind == PlaceKind.thread) {
      final thread = _threadRef(place);
      if (thread != null) return thread.host;
    }
    if (place.kind == PlaceKind.project) {
      final root = place.id;
      if (root != null) return _hostOfProject(root);
    }
    final first = _firstProject();
    if (first != null) return first.$1;
    final hosts = widget.store.hosts;
    return hosts.isEmpty ? null : hosts.first.name;
  }

  /// Opens the sheet that registers a repository for issue intake (F9 B1), on
  /// the host the place belongs to.
  Future<void> _openConnectRepo(BuildContext context) async {
    final build = widget.connectRepoFor;
    if (build == null) return;
    final host = _hostForConnect();
    if (host == null) return;
    final model = build(host);
    await showConnectRepoSheet(context, model);
    model.dispose();
  }

  /// Whether a text field has the keyboard, so that a key the shell would
  /// otherwise act on belongs to the field instead.
  ///
  /// A text field keeps its own keys. In this Flutter the field's focus node
  /// is attached by a Focus widget inside EditableText, so the field is an
  /// ancestor of the focused context rather than that context's own widget.
  /// `⌘N` and `⌘1/⌘2/⌘3` all ask here, so a composer that has the keyboard is
  /// not navigated away from mid-sentence.
  bool _editingText() {
    final focused = FocusManager.instance.primaryFocus?.context;
    return focused != null &&
        focused.findAncestorWidgetOfExactType<EditableText>() != null;
  }

  /// ⌘N from anywhere (f9-parity decision 6): a key press does nothing at all
  /// while a text field has the keyboard, so typing a `⌘N` into a composer is
  /// not a key press, and otherwise opens the new-work dialog ([_newWork]).
  ///
  /// The guard belongs to the key path alone. A pointer tap does not take the
  /// keyboard from a composer, so a New button that asked here would be dead
  /// while a field held the keyboard: every button calls [_newWork] directly.
  void _newWorkFromKeys() {
    if (_editingText()) return;
    _newWork();
  }

  /// Opens the new-work dialog for the project in view — the thread's, the
  /// project place's, else the first the fleet lists — for every caller but
  /// the key path, which guards a focused text field first.
  void _newWork() {
    final place = _place;
    String? host;
    String? project;
    if (place.kind == PlaceKind.thread) {
      final thread = _threadRef(place);
      host = thread?.host;
      project = thread?.project;
    } else if (place.kind == PlaceKind.project) {
      final root = place.id;
      if (root != null) {
        host = _hostOfProject(root);
        project = root;
      }
    }
    if (host == null || project == null) {
      final first = _firstProject();
      // A dialog with no project to belong to is not opened at all.
      if (first == null) return;
      host = first.$1;
      project = first.$2;
    }
    unawaited(
      _openNewWork(PlaceNavigation(go: _go, unsaved: _unsaved), host, project),
    );
  }

  /// Opens the Studio's one new-work dialog for one project, and the place the
  /// answer names (design.md, "Starting work").
  ///
  /// A thread's `⌘N`, the Overview's "New" and a project page's "New" all come
  /// here: an agent opens its thread, "Plan first" opens the planning thread
  /// ([_rememberPlanner] is what lets its place resolve), and "Work item" opens
  /// the handoff review.
  Future<void> _openNewWork(
    PlaceNavigation navigation,
    String host,
    String project,
  ) async {
    final build = widget.newWorkFor;
    if (build == null) return;
    final model = build(host, project);
    await Sheet.show<void>(
      context,
      newWorkDialog(
        model,
        onCancel: () => Navigator.maybePop(context),
        // The dialog may move to another host after it opens (spec D10), so
        // every answer lands on the host the model names now, not the one the
        // sheet opened on.
        onStarted: (agentId) {
          Navigator.maybePop(context);
          navigation.go(Place(PlaceKind.thread, host: model.host, id: agentId));
        },
        onPlanned: (planner) {
          Navigator.maybePop(context);
          _rememberPlanner(model.host, project, planner);
          navigation.go(
            Place(PlaceKind.thread, host: model.host, id: planner.id),
          );
        },
        onWorkItem: (prompt) {
          Navigator.maybePop(context);
          unawaited(_openNewWorkItem(navigation, model.host, project, prompt));
        },
      ),
    );
    model.dispose();
  }

  /// Opens the handoff review with [prompt] as the plan's starting text, and
  /// the item approving it creates (F6 decision 9).
  ///
  /// The dialog's "Work item" is the same review a planning thread hands off
  /// to, opened without a capture: the text the person typed is the plan, and
  /// nothing has frozen it.
  Future<void> _openNewWorkItem(
    PlaceNavigation navigation,
    String host,
    String project,
    String prompt,
  ) async {
    final build = widget.handoffFor;
    if (build == null) return;
    final model = await build(host, project, _titleOf(prompt), prompt, '');
    // The project's settings are read before the sheet opens, so the place may
    // be gone by now; the review is disposed rather than opened on a dead
    // context.
    if (!mounted) {
      model.dispose();
      return;
    }
    final item = await showHandoffSheet(context, model);
    model.dispose();
    if (item != null) {
      navigation.go(Place(PlaceKind.thread, host: host, id: item));
    }
  }

  /// The title a work item started from a prompt takes: its first line, so the
  /// board reads as the person wrote it rather than as "Work item" repeated.
  static String _titleOf(String prompt) {
    final line = prompt.trim().split('\n').first.trim();
    if (line.isEmpty) return 'Work item';
    return line.length <= 72 ? line : '${line.substring(0, 71)}…';
  }

  /// Opens a run a project page listed (F6 decision 12): remembers it as a
  /// plan-run thread and the digest the list carried, so its place draws the
  /// run's lanes rather than the agent's transcript.
  void _openRun(PlaceNavigation navigation, String host, ProjectRun run) {
    setState(() {
      _runs[run.agentID] = model.ThreadRef(
        host: host,
        id: run.agentID,
        project: run.project,
        title: run.name.isEmpty ? 'Plan run' : run.name,
        kind: model.ThreadKind.planRun,
        status: run.status,
        headline: run.headline,
        lastActive: run.at,
      );
      final digest = run.digest;
      if (digest != null) _runDigests[run.agentID] = digest;
    });
    navigation.go(Place(PlaceKind.thread, host: host, id: run.agentID));
  }
}

/// One thread place: the store and the view state a thread's screen owns,
/// started when the place opens and stopped when it closes.
class _ThreadPlace extends StatefulWidget {
  const _ThreadPlace({
    super.key,
    required this.thread,
    required this.buildStore,
    this.composerFor,
    this.shareFor,
    this.inspectorFor,
    this.onNewWork,
    this.onNewWorkFromKeys,
    this.asks = const [],
    this.onAnswer,
    this.now,
    this.pulls,
    this.density = Density.comfortable,
  });

  final model.ThreadRef thread;
  final StackStore Function(model.ThreadRef thread) buildStore;
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;

  /// What this thread opens as, from Settings' General section: the density a
  /// transcript starts at. The thread's own switch still overrides it, for
  /// this thread alone.
  final Density density;

  /// Builds this thread's Share model, once, when the place opens. Null
  /// leaves the header's Share disabled.
  final ShareModel Function(String host, String agentId)? shareFor;

  /// Builds this thread's inspector, once, when the place opens: the panel
  /// beside the transcript (F9 decision 4). Null leaves the thread without
  /// one, as every thread was before F9.
  final InspectorBuilder? inspectorFor;

  /// Opens the Studio's one new-work dialog for this thread's host and
  /// project: the thread's empty transcript and outline ask for it. Null
  /// leaves those actions without a dialog.
  final VoidCallback? onNewWork;

  /// What this thread's own `⌘N` asks for: the same dialog, behind the
  /// shell's guard against a composer that holds the keyboard (F9 decision 6).
  /// Null falls back to [onNewWork].
  final VoidCallback? onNewWorkFromKeys;
  final List<inbox.Ask> asks;
  final AnswerAsk? onAnswer;
  final DateTime? now;

  /// The pull requests this thread shipped: the review head or the failing
  /// card at the top of its conversation (F5 decision 13).
  final PullsModel? pulls;

  @override
  State<_ThreadPlace> createState() => _ThreadPlaceState();
}

class _ThreadPlaceState extends State<_ThreadPlace> {
  late final StackStore _store = widget.buildStore(widget.thread);
  late final ThreadViewState _view = ThreadViewState()
    ..density = widget.density;

  /// The inspector this thread's tabs read, built once so a rebuild does not
  /// rebuild five models (F9 decision 4).
  late final ThreadInspector? _inspector = widget.inspectorFor?.call(
    widget.thread.host,
    widget.thread,
  );
  ComposerModel? _composer;
  ShareModel? _share;

  @override
  void initState() {
    super.initState();
    _store.start();
    // The holder owns its five models, so it starts their first read: nothing
    // else would, and Changes would draw loading forever.
    _inspector?.start();
    _composer = widget.composerFor?.call(widget.thread, _view);
    _share = widget.shareFor?.call(widget.thread.host, widget.thread.id);
    // A thread opened while its agent is mid-turn offers Steer and Stop from
    // the first frame, from the fleet's own report of the agent.
    _composer?.setRunning(_turnRunning);
    // The composer's chip names the session's own access, which only a read
    // reports: the first frame draws the composer's default until it lands.
    final composer = _composer;
    if (composer != null) unawaited(composer.refreshAccess());
  }

  @override
  void didUpdateWidget(_ThreadPlace old) {
    super.didUpdateWidget(old);
    // The fleet's report of the agent is the session's own state: a turn that
    // ended (ready, idle or failed) confirms a requested stop, and one that is
    // running, or parked on a person, keeps Steer and Stop.
    if (old.thread.status != widget.thread.status) {
      _composer?.setRunning(_turnRunning);
      // The chip is read again at a turn boundary: the mode a session commits
      // is announced as it happens, but the Studio's one place to re-read it
      // without a subscription is here, so a chip can be stale until the turn
      // ends (best-effort, per the plan's non-goal on switching mode).
      final composer = _composer;
      if (composer != null) unawaited(composer.refreshAccess());
    }
  }

  /// Whether the thread's agent is mid-turn, as the fleet reports it. A
  /// pending request means the turn is parked on a person, not finished.
  bool get _turnRunning =>
      widget.thread.status == model.ThreadStatus.running ||
      widget.thread.status == model.ThreadStatus.needsYou;

  @override
  void dispose() {
    // The inspector's tab models and its state outlive no thread: they are
    // released before the store the screen was reading.
    _inspector?.dispose?.call();
    _store
      ..stop()
      ..dispose();
    _view.dispose();
    _composer?.dispose();
    _share?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final composer = _composer;
    return ThreadScreen(
      thread: widget.thread,
      store: _store,
      view: _view,
      asks: widget.asks,
      onAnswer: widget.onAnswer,
      now: widget.now,
      pull: _pull,
      pullActions: _pullActions(context),
      onMerge: _mergeFor(context),
      onSendBack: _pull == null || widget.pulls == null
          ? null
          : () => _sendBack(context),
      onNewWork: widget.onNewWork,
      onNewWorkFromKeys: widget.onNewWorkFromKeys,
      // The panel beside the transcript, when a host built one (F9
      // decision 4). Null leaves the screen as it was before F9.
      inspector: _inspector?.state,
      inspectorBody: _inspector?.body,
      // Share mints this thread's status link on the host's client. Null
      // leaves the header's Share disabled (F7 decision 7).
      onShare: _share == null
          ? null
          : () => unawaited(showShareSheet(context, _share!)),
      // Stop ends the turn through the composer's own route. Hold is not
      // passed: no bridge route holds an agent, so the header would offer an
      // item that cannot work (f9-parity.md decision 2).
      onStop: _composer == null ? null : () => unawaited(_composer!.stop()),
      composer: composer == null
          ? null
          : ThreadComposer(model: composer, view: _view),
    );
  }

  /// The pull request this thread shipped, when it shipped one: the newest, so
  /// a reopened pull request is the one at the head.
  PullView? get _pull => widget.pulls?.forThread(widget.thread).firstOrNull;

  /// The failing card's actions: the one click, the caret and the rest.
  PrActions? _pullActions(BuildContext context) {
    final pull = _pull;
    final pulls = widget.pulls;
    if (pull == null || pulls == null) return null;
    return pullCardActions(context, pull: pull, model: pulls);
  }

  /// Merges the pull request, after the confirmation that names it.
  ///
  /// A factory item's merge is not offered: it needs the reviewed identity a
  /// pull record does not carry (F6). The review head draws it disabled.
  VoidCallback? _mergeFor(BuildContext context) {
    final pull = _pull;
    final pulls = widget.pulls;
    if (pull == null || pulls == null || pull.fromFactoryItem) return null;
    return () => unawaited(confirmMerge(context, pull: pull, model: pulls));
  }

  /// Sends the pull request back to this thread's own agent (decision 13).
  /// The person's words reach a turn in flight as steering, and an idle agent
  /// as the next thing it is asked to do.
  Future<void> _sendBack(BuildContext context) async {
    final pulls = widget.pulls;
    if (pulls == null) return;
    final e = Ember.of(context);
    final controller = TextEditingController();
    final words = await Sheet.show<String>(
      context,
      Sheet(
        title: 'Send back to the agent',
        actions: [
          Btn.primary(
            'Send it',
            onTap: () => Navigator.maybePop(context, controller.text.trim()),
          ),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Field(
              label: 'What should change?',
              controller: controller,
              hint: 'Pin the clock in the fixture rather than widening it',
            ),
            const SizedBox(height: Space.s8),
            Text(
              'The agent that shipped this pull request reads it in this '
              'thread.',
              style: e.small.tint(e.muted),
            ),
          ],
        ),
      ),
    );
    final text = words?.trim() ?? '';
    controller.dispose();
    if (text.isEmpty) return;
    await pulls.sendBack(_pull!, text: text);
  }
}

/// What the shell hands the place it draws: its one way of leaving, which
/// asks the unsaved-edit guard first, and the guard itself, so a place that
/// edits watches the models it holds while it is mounted.
class PlaceNavigation {
  const PlaceNavigation({required this.go, required this.unsaved});

  /// Opens a place, after asking about an unsaved edit when there is one.
  final void Function(Place place) go;

  /// The shell's one guard. A place watches the models it is editing when it
  /// mounts and drops them when it goes.
  final UnsavedGuard? unsaved;
}

/// One work item's place: the item as the factory store holds it, with the
/// asks it waits on and the pull requests it reached.
///
/// The place keeps the item current — the store polls — so a stage, an ask and
/// a card move without the person leaving the thread.
class _WorkItemPlace extends StatefulWidget {
  const _WorkItemPlace({
    super.key,
    required this.thread,
    required this.factory,
    this.pulls,
    this.threadStore,
    this.composerFor,
    this.now,
    this.density = Density.comfortable,
    this.onRetry,
  });

  final model.ThreadRef thread;
  final FactoryStore factory;
  final PullsModel? pulls;

  /// The transcript a worker's conversation is read from, and its composer:
  /// F3's screen, embedded (F6 decision 6).
  final StackStore Function(model.ThreadRef thread)? threadStore;
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;
  final DateTime? now;

  /// The density the item's worker transcript opens at, passed on from the
  /// Studio: what Settings' General section chose.
  final Density density;

  final VoidCallback? onRetry;

  @override
  State<_WorkItemPlace> createState() => _WorkItemPlaceState();
}

class _WorkItemPlaceState extends State<_WorkItemPlace> {
  /// The bridge's refusal of the last action, in its own words: a conflict is
  /// not an error the person made, and the action stays available.
  String? _error;

  /// The worker whose conversation is open in the centre, or null while the
  /// item's activity is.
  WorkItemWorker? _worker;

  @override
  void initState() {
    super.initState();
    widget.factory.addListener(_changed);
  }

  @override
  void dispose() {
    widget.factory.removeListener(_changed);
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final host = widget.thread.host;
    final item = widget.factory.byId(host, widget.thread.id);
    if (item == null) return _missing(context, host);
    final pulls = widget.pulls?.forThread(widget.thread) ?? const <PullView>[];
    final view = WorkItemView.of(host, item);
    final store = widget.factory;
    final worker = _worker;
    final build = widget.threadStore;
    return WorkItemScreen(
      center: worker == null || build == null
          ? null
          : _ThreadPlace(
              key: ValueKey('worker/${worker.sessionID}'),
              thread: model.ThreadRef(
                host: host,
                id: worker.sessionID,
                project: item.projectRoot,
                title: worker.words,
                kind: model.ThreadKind.agent,
                status: model.ThreadStatus.running,
                headline: '',
                lastActive: widget.now ?? DateTime.now(),
              ),
              buildStore: build,
              composerFor: widget.composerFor,
              now: widget.now,
              density: widget.density,
              pulls: widget.pulls,
            ),
      onBack: () => setState(() => _worker = null),
      onOpenWorker: (opened) => setState(() => _worker = opened),
      onProposeChange: build == null
          ? null
          : () => unawaited(_propose(context, host, view)),

      item: view,
      now: widget.now,
      pulls: pulls,
      conflict: _error ?? store.conflictOf(host, item.id),
      onRetry: widget.onRetry,
      onApprove: () => _command(() => store.approve(host, item.id)),
      onAnswer: (answer) => _command(
        () => store.answerDecision(
          host,
          item.id,
          decisionID: view.decisionID,
          answer: answer,
        ),
      ),
      onRecover: (choice) => _command(
        () => store.recover(
          host,
          item.id,
          model: choice.model,
          fresh: choice.fresh,
          extraRounds: choice.rounds,
        ),
      ),
      onComplete: () => _command(() => store.complete(host, item.id)),
      onMergeAndComplete: () => _merge(host, item.id),
      onReopen: () => _command(() => store.reopen(host, item.id)),
      onStop: () => _command(() => store.stopItem(host, item.id)),
      pullActionsFor: widget.pulls == null
          ? null
          : (context, pull) =>
                pullCardActions(context, pull: pull, model: widget.pulls!),
    );
  }

  /// Opens the plan-revision sheet: the approved plan against the proposed
  /// one, and the one action that sends it (F6 decision 6).
  Future<void> _propose(
    BuildContext context,
    String host,
    WorkItemView view,
  ) async {
    final model = RevisionModel(
      approved: view.plan,
      spec: view.spec,
      baseRevision: view.revision,
      baseHash: view.baseHash,
      submit: (plan) async {
        await widget.factory.proposeRevision(
          host,
          view.id,
          plan: plan,
          spec: view.spec,
          baseRevision: view.revision,
          baseHash: view.baseHash,
        );
      },
    );
    await showRevisionSheet(context, model);
    model.dispose();
  }

  /// Runs one command, keeping the bridge's refusal for the thread to say and
  /// leaving the action where it was, so the person can decide again.
  Future<void> _command(Future<Object?> Function() action) async {
    try {
      await action();
      if (mounted) setState(() => _error = null);
    } catch (error) {
      if (mounted) setState(() => _error = describeFailure(error));
    }
  }

  /// Merges the reviewed pull request and completes the item. The identity is
  /// the item's own record of the reviewed pull request, resolved inside the
  /// store, so nothing the surface assembled can be merged (F6 decision 5).
  Future<void> _merge(String host, String id) async {
    await _command(() => widget.factory.mergeAndComplete(host, id));
  }

  /// The item until the store has it: loading while the first read is in
  /// flight, and the bridge's own words when the read failed (§7).
  Widget _missing(BuildContext context, String host) {
    final retry = StateAction(
      'Read the factory again',
      onTap: widget.onRetry ?? () {},
    );
    final failure = widget.factory.failure(host);
    return PageBody(
      title: widget.thread.title,
      children: [
        if (failure == null)
          const StateMessage.loading(lines: 4)
        else
          StateMessage.error(
            message: 'The work item could not be read.',
            retry: retry,
          ),
      ],
    );
  }
}

/// The review a planning thread hands off to: the capture the person just
/// reviewed, the plan and specification it froze, and the planner it belongs
/// to.
///
/// It is a named function beside the place rather than three arguments written
/// inline, because this binding is the whole of "the draft is built from the
/// capture": a screen that passed no snapshot would still open a review, and
/// only a test that reads these arguments can tell the difference.
Future<HandoffModel> reviewForPlanner(
  HandoffBuilder build,
  model.ThreadRef thread,
  PlanningModel plan,
) => build(
  thread.host,
  thread.project,
  thread.title,
  plan.latest?.plan ?? '',
  plan.latest?.spec ?? '',
  plannerID: plan.plannerID,
  snapshot: plan.latest,
);

/// One planning thread's place (F6 decision 8): the planner's conversation,
/// the requests it is parked on, and the plan its last snapshot froze.
///
/// It owns the model it was built with, so leaving the place lets the planner
/// alone rather than leaving its reader running.
class _PlanningPlace extends StatefulWidget {
  const _PlanningPlace({
    super.key,
    required this.thread,
    required this.plan,
    this.handoffFor,
    this.now,
    this.onOpenItem,
  });

  final model.ThreadRef thread;
  final PlanningModel plan;
  final HandoffBuilder? handoffFor;
  final DateTime? now;
  final void Function(String itemID)? onOpenItem;

  @override
  State<_PlanningPlace> createState() => _PlanningPlaceState();
}

class _PlanningPlaceState extends State<_PlanningPlace> {
  @override
  void dispose() {
    widget.plan.dispose();
    super.dispose();
  }

  /// Opens the handoff review for the plan the last snapshot froze, bound to
  /// that capture: what the sheet shows as evidence and what the draft is
  /// built from are the bytes the bridge froze, not a copy this screen holds.
  Future<void> _handOff(BuildContext context) async {
    final build = widget.handoffFor;
    if (build == null) return;
    final model = await reviewForPlanner(build, widget.thread, widget.plan);
    // What the review inherits is read before the sheet opens, so the place
    // may be gone by now; the review is disposed rather than opened on a dead
    // context.
    if (!context.mounted) {
      model.dispose();
      return;
    }
    final item = await showHandoffSheet(context, model);
    model.dispose();
    if (item != null) widget.onOpenItem?.call(item);
  }

  @override
  Widget build(BuildContext context) => PlanningScreen(
    plan: widget.plan,
    title: widget.thread.title,
    subtitle: widget.thread.headline,
    now: widget.now,
    onRetry: () => unawaited(widget.plan.refresh()),
    onHandToFactory: widget.handoffFor == null
        ? null
        : () => unawaited(_handOff(context)),
  );
}

/// One project's place (F6 decision 12): its work, its automations, its
/// defaults and its health.
///
/// It owns the page's model, built from the app's seam and disposed when the
/// place goes, and it reads the board's items from the one factory store that
/// polls them (F6 decision 14) rather than fetching a list of its own.
class _ProjectPlace extends StatefulWidget {
  const _ProjectPlace({
    super.key,
    required this.root,
    required this.host,
    required this.build,
    required this.store,
    this.factory,
    this.guard,
    this.now,
    this.onOpenItem,
    this.onOpenAgent,
    this.onOpenSession,
    this.onOpenRun,
    this.onOpenAutomations,
    this.onNewWork,
  });

  final String root;
  final String host;
  final ProjectModel Function(String host, String root) build;
  final FleetStore store;
  final FactoryStore? factory;
  final UnsavedGuard? guard;
  final DateTime? now;
  final void Function(WorkItemView item)? onOpenItem;
  final void Function(model.ThreadRef agent)? onOpenAgent;
  final void Function(model.ThreadRef session)? onOpenSession;
  final void Function(ProjectRun run)? onOpenRun;
  final void Function(int? tab)? onOpenAutomations;

  /// Opens the Studio's new-work dialog for this project: the page's "New".
  final VoidCallback? onNewWork;

  @override
  State<_ProjectPlace> createState() => _ProjectPlaceState();
}

class _ProjectPlaceState extends State<_ProjectPlace> {
  /// The page's model: built once for this project, and disposed with it.
  late final ProjectModel _model = widget.build(widget.host, widget.root);

  /// The repository the evidence beside the editor was last read for.
  String _repo = '';

  @override
  void initState() {
    super.initState();
    widget.factory?.addListener(_changed);
    widget.store.addListener(_changed);
    // The project's own settings are where its intake repository is, and that
    // is what the evidence offered beside the editor is filtered by.
    _model.addListener(_repositoryChanged);
  }

  @override
  void dispose() {
    widget.store.removeListener(_changed);
    widget.factory?.removeListener(_changed);
    _model.removeListener(_repositoryChanged);
    _model.dispose();
    super.dispose();
  }

  /// Rebuilds when the project names another repository.
  ///
  /// Only that changes what the place hands the page: the page reads its own
  /// model and draws itself, so a draft edit needs no rebuild here. It also
  /// keeps the first read's notice — which the page posts while the place is
  /// mounting — from rebuilding the tree mid-build, when nothing has changed
  /// yet.
  void _repositoryChanged() {
    if (!mounted || _model.repoID == _repo) return;
    _repo = _model.repoID;
    setState(() {});
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final factory = widget.factory;
    // The board draws views of the items the one factory store holds (F6
    // decision 14).
    final items = [
      for (final item
          in factory?.items(host: widget.host, projectRoot: widget.root) ??
              const [])
        WorkItemView.of(widget.host, item),
    ];
    // A factory worker is not an independent agent (F6 decision 13).
    final workers = factory?.workerAgentIds ?? const <String>{};
    final agents = [
      for (final agent in widget.store.agents(host: widget.host))
        if (agent.project == widget.root && !workers.contains(agent.id))
          model.threadFor(widget.host, agent),
    ];
    return ProjectPage(
      model: _model,
      items: items,
      agents: agents,
      sessions: _sessions(),
      sessionsError: widget.store.sessionsError(widget.host, widget.root),
      guard: widget.guard,
      now: widget.now,
      onOpenItem: widget.onOpenItem,
      onOpenAgent: widget.onOpenAgent,
      onOpenSession: widget.onOpenSession,
      onOpenRun: widget.onOpenRun,
      onOpenAutomations: widget.onOpenAutomations,
      onRetrySessions: () => unawaited(
        widget.store.refreshSessions(widget.host, root: widget.root),
      ),
      onNewWork: widget.onNewWork,
      observedChecks:
          factory?.observedChecks(
            projectRoot: widget.root,
            // Evidence comes from the same project *and* repository (F6
            // decision 12), so the project's intake repository filters it; a
            // project that names none filters by nothing.
            repoID: _model.repoID,
          ) ??
          const [],
    );
  }

  /// This project's sessions on this host: open first, then closed, each part
  /// newest first, and a tie broken by id as the sidebar breaks one
  /// (P7 decision 8).
  List<model.ThreadRef> _sessions() {
    final sessions = [
      for (final hosted in widget.store.sessions(host: widget.host))
        if (hosted.root == widget.root)
          model.threadOfSession(hosted.host, hosted.root, hosted.row),
    ];
    sessions.sort((a, b) {
      if (a.closed != b.closed) return a.closed ? 1 : -1;
      final byTime = b.lastActive.compareTo(a.lastActive);
      return byTime != 0 ? byTime : a.id.compareTo(b.id);
    });
    return sessions;
  }
}

/// One plan run's place (F6 decision 11): the run's tasks as lanes, graph or
/// timeline, with the live step beneath and the question it stopped on pinned
/// above.
///
/// It takes either a model the app built ([run]) or the digest a project page
/// listed ([digest]), which it owns and disposes when the place goes.
class _RunPlace extends StatefulWidget {
  const _RunPlace({
    super.key,
    required this.thread,
    this.run,
    this.digest,
    this.answer,
    this.roster,
    this.now,
  }) : assert(
         run != null || digest != null,
         'a run place needs a model or a digest',
       );

  final model.ThreadRef thread;
  final RunModel? run;
  final Map<String, dynamic>? digest;

  /// Answers the question the run stopped on, when the app can post one.
  final Future<void> Function(String answer)? answer;

  /// Reads the session's roster, so the lanes name the roles they drew.
  final Future<Roster> Function()? roster;
  final DateTime? now;

  @override
  State<_RunPlace> createState() => _RunPlaceState();
}

class _RunPlaceState extends State<_RunPlace> {
  /// The model this place draws: the one it was given, or the one it built
  /// from the digest. It disposes only what it built.
  late final RunModel _model =
      widget.run ??
      RunModel.fromDigest(
        widget.thread.id,
        widget.digest!,
        answer: widget.answer,
        roster: widget.roster,
      );

  bool get _owned => widget.run == null;

  @override
  void dispose() {
    if (_owned) _model.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) =>
      RunScreen(model: _model, now: widget.now);
}
