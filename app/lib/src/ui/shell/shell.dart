import 'dart:async';

import '../../model/age.dart';
import '../../model/composer_model.dart';
import '../../model/factory_store.dart';
import '../../model/handoff_model.dart';
import '../../model/new_work_model.dart';
import '../../model/fleet_store.dart';
import '../../model/inbox_model.dart';
import '../../model/inbox_model.dart' as inbox;
import '../../model/nav_model.dart';
import '../../model/overview_model.dart' show buildOverview;
import '../../model/place.dart';
import '../../model/planning_model.dart';
import '../../model/pulls_model.dart';
import '../../model/revision_model.dart';
import '../../model/stack_store.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/thread_view_state.dart';
import '../../model/work_item_model.dart'
    show WorkItemView, WorkItemWorker, threadOfItem;
import '../inbox/inbox.dart' show Inbox;
import '../factory/handoff_sheet.dart';
import '../factory/planning_screen.dart';
import '../factory/revision_sheet.dart';
import '../factory/work_item_screen.dart';
import '../kit/kit.dart';
import '../overview/overview.dart' show Overview;
import '../pulls/fix_control.dart';
import '../pulls/merge_control.dart';
import '../pulls/pulls_place.dart';
import '../sections/unsaved_guard.dart';
import '../new_work/new_work_dialog.dart';
import '../thread/thread_composer.dart';
import '../thread/thread_screen.dart';
import 'placeholder.dart';
import 'sidebar.dart';

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
    onHost: (host) => setState(() {
      _host = host;
      _panel = false;
    }),
    onSelect: (place) => _leave(() => widget.onSelect(place)),
    now: widget.now,
    inPanel: inPanel,
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
    final content = _place();
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
    this.threadStore,
    this.composerFor,
    this.newWorkFor,
    this.planningFor,
    this.handoffFor,
    this.initialPlace = const Place.overview(),
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

  /// Builds the store a thread place reads its session into. Null draws a
  /// thread place as planned: a test or an embedded host that supplies none
  /// gets the promise, never a socket.
  final StackStore Function(model.ThreadRef thread)? threadStore;

  /// Builds a thread place's composer. Null leaves the thread without one.
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;

  /// Builds the new-work dialog's model for a thread's project. Null leaves
  /// `⌘N` without a dialog.
  final NewWorkModel Function(model.ThreadRef thread)? newWorkFor;

  /// Builds a planning thread's model, for a planner a person started (F6
  /// decision 8). Null leaves the planning place unbuilt rather than drawing
  /// an empty one.
  final PlanningModel Function(String host, String plannerID)? planningFor;

  /// Builds the handoff review for a plan (F6 decision 8). Null leaves the one
  /// action that hands a plan off out, rather than offering a sheet that
  /// cannot create an item.
  final HandoffModel Function(model.ThreadRef thread, String plan, String spec)?
  handoffFor;

  /// The place a deep link names.
  final Place initialPlace;

  @override
  State<Studio> createState() => _StudioState();
}

class _StudioState extends State<Studio> {
  late Place _place = widget.initialPlace;
  bool _read = false;
  String? _failure;

  /// The one guard every place leaving this screen consults: the shell asks
  /// it, and a place that edits watches the models it holds (F7 decision 3).
  final _unsaved = UnsavedGuard();

  /// The planning threads this screen started, by planner id (F6 decision 8).
  /// A planner is none of the things the fleet lists, so the screen that
  /// started it is the only one that can name its place.
  final _planners = <String, model.ThreadRef>{};

  @override
  void initState() {
    super.initState();
    _reread();
  }

  @override
  void dispose() {
    _unsaved.dispose();
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
    if (!mounted) return;
    setState(() => _read = true);
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: Listenable.merge([widget.store, widget.inbox]),
      builder: (context, _) {
        final store = widget.store;
        final place = _place;
        final nav = buildNavModel(
          place,
          store,
          pullsReady: widget.pulls?.readyCount ?? 0,
          factory: widget.factory,
        );
        return Shell(
          nav: nav,
          hosts: store.hosts,
          place: place,
          state: _state(store),
          content: (navigation) => _content(place, nav, navigation),
          onSelect: (next) => setState(() => _place = next),
          onRetry: _reread,
          now: store.now,
          unsaved: _unsaved,
        );
      },
    );
  }

  /// The shell's state, from what the store holds.
  StateKind? _state(FleetStore store) {
    if (!_read) return StateKind.loading;
    if (store.hosts.isEmpty) {
      return _failure == null ? StateKind.empty : StateKind.error;
    }
    if (store.agents().isEmpty && store.hosts.every((host) => !host.live)) {
      return StateKind.unavailable;
    }
    if (store.hosts.any((host) => !host.live)) return StateKind.stale;
    return null;
  }

  /// What the place draws. A place whose screen a later step of F2 brings
  /// says so (design-system.md §7).
  ///
  /// The Overview and the Inbox leave through the shell's guarded navigation,
  /// never by setting the place here: a row or an ask can no more leave a
  /// dirty place without asking than the sidebar can.
  Widget _content(Place place, NavModel nav, PlaceNavigation navigation) {
    if (place.kind == PlaceKind.overview) {
      return Overview(
        overview: buildOverview(widget.store, widget.inbox),
        now: widget.store.now,
        hosts: widget.store.hosts,
        pulls: widget.pulls?.views ?? const [],
        onSelect: navigation.go,
        onAnswer: widget.inbox.answer,
        onRetry: _reread,
      );
    }
    if (place.kind == PlaceKind.inbox) {
      return Inbox(
        asks: widget.inbox.asks,
        pulls: widget.pulls?.views ?? const [],
        now: widget.store.now,
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
    if (place.kind == PlaceKind.thread) {
      final build = widget.threadStore;
      final thread = _threadRef(place);
      final factory = widget.factory;
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
          onRetry: () => unawaited(factory.refresh(thread.host)),
        );
      }
      if (build != null && thread != null) {
        return _ThreadPlace(
          key: ValueKey('${thread.host}/${thread.id}'),
          thread: thread,
          buildStore: build,
          composerFor: widget.composerFor,
          newWorkFor: widget.newWorkFor,
          onThreadStarted: (id) =>
              navigation.go(Place(PlaceKind.thread, host: thread.host, id: id)),
          handoffFor: widget.handoffFor,
          onOpenItem: (id) =>
              navigation.go(Place(PlaceKind.thread, host: thread.host, id: id)),
          onPlannerStarted: (planner) {
            _rememberPlanner(thread.host, thread.project, planner);
            navigation.go(
              Place(PlaceKind.thread, host: thread.host, id: planner.id),
            );
          },
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
    final host = place.host;
    final id = place.id;
    if (host == null || id == null) return null;
    final planner = _planners[id];
    if (planner != null && planner.host == host) return planner;
    final item = widget.factory?.byId(host, id);
    if (item != null) return threadOfItem(host, item);
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
}

/// One thread place: the store and the view state a thread's screen owns,
/// started when the place opens and stopped when it closes.
class _ThreadPlace extends StatefulWidget {
  const _ThreadPlace({
    super.key,
    required this.thread,
    required this.buildStore,
    this.composerFor,
    this.newWorkFor,
    this.onThreadStarted,
    this.onPlannerStarted,
    this.handoffFor,
    this.onOpenItem,
    this.asks = const [],
    this.onAnswer,
    this.now,
    this.pulls,
  });

  final model.ThreadRef thread;
  final StackStore Function(model.ThreadRef thread) buildStore;
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;

  /// Builds the new-work dialog's model for a thread's project. Null leaves
  /// `⌘N` without a dialog.
  final NewWorkModel Function(model.ThreadRef thread)? newWorkFor;
  final void Function(String agentId)? onThreadStarted;

  /// Opens the planning thread a "Plan first" started (F6 decision 8).
  final void Function(StartedPlanner planner)? onPlannerStarted;

  /// The handoff review's model, and where an approved item opens.
  final HandoffModel Function(model.ThreadRef thread, String plan, String spec)?
  handoffFor;
  final void Function(String itemID)? onOpenItem;
  final List<inbox.Ask> asks;
  final void Function(inbox.Ask ask, inbox.AskAction action)? onAnswer;
  final DateTime? now;

  /// The pull requests this thread shipped: the review head or the failing
  /// card at the top of its conversation (F5 decision 13).
  final PullsModel? pulls;

  @override
  State<_ThreadPlace> createState() => _ThreadPlaceState();
}

class _ThreadPlaceState extends State<_ThreadPlace> {
  late final StackStore _store = widget.buildStore(widget.thread);
  late final ThreadViewState _view = ThreadViewState();
  ComposerModel? _composer;

  @override
  void initState() {
    super.initState();
    _store.start();
    _composer = widget.composerFor?.call(widget.thread, _view);
  }

  @override
  void dispose() {
    _store
      ..stop()
      ..dispose();
    _view.dispose();
    _composer?.dispose();
    super.dispose();
  }

  /// Opens the new-work dialog, and the thread it starts.
  Future<void> _openNewWork(BuildContext context) async {
    final build = widget.newWorkFor;
    if (build == null) return;
    final model = build(widget.thread);
    await Sheet.show<void>(
      context,
      newWorkDialog(
        model,
        onCancel: () => Navigator.maybePop(context),
        onStarted: (agentId) {
          Navigator.maybePop(context);
          widget.onThreadStarted?.call(agentId);
        },
        onPlanned: (planner) {
          Navigator.maybePop(context);
          widget.onPlannerStarted?.call(planner);
        },
        onWorkItem: (prompt) {
          Navigator.maybePop(context);
          unawaited(_openHandoff(context, prompt, ''));
        },
      ),
    );
    model.dispose();
  }

  /// Opens the handoff review for [plan], and the item it creates.
  Future<void> _openHandoff(
    BuildContext context,
    String plan,
    String spec,
  ) async {
    final build = widget.handoffFor;
    if (build == null) return;
    final model = build(widget.thread, plan, spec);
    final item = await showHandoffSheet(context, model);
    model.dispose();
    if (item != null) widget.onOpenItem?.call(item);
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
      onNewWork: widget.newWorkFor == null ? null : () => _openNewWork(context),
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
  VoidCallback? _mergeFor(BuildContext context) {
    final pull = _pull;
    final pulls = widget.pulls;
    if (pull == null || pulls == null) return null;
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
      onMergeAndComplete: () => _merge(host, item.id, pulls),
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
  /// the card's own, so nothing the surface assembled can be merged.
  Future<void> _merge(String host, String id, List<PullView> pulls) async {
    if (pulls.isEmpty) {
      if (mounted) {
        setState(() => _error = 'There is no pull request to merge yet.');
      }
      return;
    }
    final pull = pulls.first.record;
    await _command(
      () => widget.factory.mergeAndComplete(
        host,
        id,
        repoID: pull.repoID,
        number: pull.number,
        headSHA: pull.headSHA,
        baseRef: pull.baseRef,
      ),
    );
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
  final HandoffModel Function(model.ThreadRef thread, String plan, String spec)?
  handoffFor;
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

  /// Opens the handoff review for the plan the last snapshot froze.
  Future<void> _handOff(BuildContext context) async {
    final build = widget.handoffFor;
    if (build == null) return;
    final latest = widget.plan.latest;
    final model = build(widget.thread, latest?.plan ?? '', latest?.spec ?? '');
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
