import '../../model/age.dart';
import '../../model/composer_model.dart';
import '../../model/new_work_model.dart';
import '../../model/fleet_store.dart';
import '../../model/inbox_model.dart';
import '../../model/inbox_model.dart' as inbox;
import '../../model/nav_model.dart';
import '../../model/place.dart';
import '../../model/stack_store.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/thread_view_state.dart';
import '../inbox/inbox.dart' show Inbox;
import '../kit/kit.dart';
import '../overview/overview.dart' show Overview;
import '../../model/overview_model.dart' show buildOverview;
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
  });

  final NavModel nav;
  final List<HostView> hosts;
  final Place place;

  /// What the place draws: the Overview, the Inbox, or a place's promise.
  final Widget content;

  /// Opens a place.
  final void Function(Place place) onSelect;

  /// The fleet's data state, when it is not ready (§7). The sidebar is still
  /// drawn; the content area says what the state is.
  final StateKind? state;

  /// What ages are measured against.
  final DateTime? now;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

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
    onSelect: (place) {
      widget.onSelect(place);
      setState(() => _panel = false);
    },
    now: widget.now,
    inPanel: inPanel,
  );

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
                  onTap: () => widget.onSelect(const Place(PlaceKind.inbox)),
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
    final state = widget.state;
    if (state == null) return widget.content;
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
          Expanded(child: widget.content),
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
    this.read,
    this.threadStore,
    this.composerFor,
    this.newWorkFor,
    this.initialPlace = const Place.overview(),
  });

  final FleetStore store;
  final InboxModel inbox;

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

  /// The place a deep link names.
  final Place initialPlace;

  @override
  State<Studio> createState() => _StudioState();
}

class _StudioState extends State<Studio> {
  late Place _place = widget.initialPlace;
  bool _read = false;
  String? _failure;

  @override
  void initState() {
    super.initState();
    _reread();
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
        final nav = buildNavModel(place, store);
        return Shell(
          nav: nav,
          hosts: store.hosts,
          place: place,
          state: _state(store),
          content: _content(place, nav),
          onSelect: (next) => setState(() => _place = next),
          onRetry: _reread,
          now: store.now,
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

  /// Opens a place, from the content as well as from the sidebar.
  void _go(Place place) => setState(() => _place = place);

  /// What the place draws. A place whose screen a later step of F2 brings
  /// says so (design-system.md §7).
  Widget _content(Place place, NavModel nav) {
    if (place.kind == PlaceKind.overview) {
      return Overview(
        overview: buildOverview(widget.store, widget.inbox),
        now: widget.store.now,
        hosts: widget.store.hosts,
        onSelect: _go,
        onAnswer: widget.inbox.answer,
        onRetry: _reread,
      );
    }
    if (place.kind == PlaceKind.inbox) {
      return Inbox(
        asks: widget.inbox.asks,
        now: widget.store.now,
        onSelect: _go,
        onAnswer: widget.inbox.answer,
        onRetry: _reread,
      );
    }
    if (place.kind == PlaceKind.thread) {
      final build = widget.threadStore;
      final thread = _threadRef(place);
      if (build != null && thread != null) {
        return _ThreadPlace(
          key: ValueKey('${thread.host}/${thread.id}'),
          thread: thread,
          buildStore: build,
          composerFor: widget.composerFor,
          newWorkFor: widget.newWorkFor,
          onThreadStarted: (id) =>
              _go(Place(PlaceKind.thread, host: thread.host, id: id)),
          asks: [
            for (final ask in widget.inbox.asks)
              if (ask.thread?.host == thread.host &&
                  ask.thread?.id == thread.id)
                ask,
          ],
          onAnswer: widget.inbox.answer,
          now: widget.store.now,
        );
      }
      return plannedPlace(place, nav);
    }
    return plannedPlace(place, nav);
  }

  /// The thread a place names, when the fleet still lists its agent.
  model.ThreadRef? _threadRef(Place place) {
    final host = place.host;
    final id = place.id;
    if (host == null || id == null) return null;
    for (final agent in widget.store.agents(host: host)) {
      if (agent.id == id) return model.threadFor(host, agent);
    }
    return null;
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
    this.asks = const [],
    this.onAnswer,
    this.now,
  });

  final model.ThreadRef thread;
  final StackStore Function(model.ThreadRef thread) buildStore;
  final ComposerModel Function(model.ThreadRef thread, ThreadViewState view)?
  composerFor;

  /// Builds the new-work dialog's model for a thread's project. Null leaves
  /// `⌘N` without a dialog.
  final NewWorkModel Function(model.ThreadRef thread)? newWorkFor;
  final void Function(String agentId)? onThreadStarted;
  final List<inbox.Ask> asks;
  final void Function(inbox.Ask ask, inbox.AskAction action)? onAnswer;
  final DateTime? now;

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
      ),
    );
    model.dispose();
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
      onNewWork: widget.newWorkFor == null ? null : () => _openNewWork(context),
      composer: composer == null
          ? null
          : ThreadComposer(model: composer, view: _view),
    );
  }
}
