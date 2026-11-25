import '../../model/age.dart';
import '../../model/fleet_store.dart' show HostView;
import '../../model/overview_model.dart';
import '../../model/place.dart';
import '../../model/pulls_model.dart';
import '../../model/source_state.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/watches_model.dart' show TrippedWatch;
import '../kit/kit.dart';
import '../inbox/answerable_ask.dart';
import '../pulls/pull_facts.dart';

/// The Overview (F2 decision 8): what needs you, what is running, every
/// project, and — beside them, or below on a narrow window — the hosts'
/// capacity and the day's spend.
///
/// It draws the [OverviewModel] it is given (design-system.md §5: a screen
/// takes data, never a store) and every number in it is a count of real
/// items.
class Overview extends StatelessWidget {
  const Overview({
    super.key,
    required this.overview,
    required this.now,
    this.hosts = const [],
    this.automations = const [],
    this.manyHosts = false,
    this.pulls = const [],
    this.state,
    this.onSelect,
    this.onAnswer,
    this.onRetry,
    this.onNewWork,
  });

  final OverviewModel overview;

  /// What a relative age is measured against.
  final DateTime now;

  /// The hosts as the shell knows them: a row's reach comes from here.
  final List<HostView> hosts;

  /// The watches that tripped, newest first: the Automations card, and
  /// nothing when none has (F7 plan 1, Task 3).
  final List<TrippedWatch> automations;

  /// Whether more than one host is known, so a tripped watch's row names its
  /// host before its name (spec D4, decision 5).
  final bool manyHosts;

  /// The pull requests that need a person: the ready and the failing ones
  /// (F5 decision 14). They sit directly under Needs you, and the place is one
  /// link away.
  final List<PullView> pulls;

  /// The fleet's data state, when it is not ready (§7).
  final StateKind? state;

  /// Opens a thread, a project or the Inbox.
  final void Function(Place place)? onSelect;

  /// Answers an ask in place, without leaving the Overview. [reply] is a
  /// question's free text and [answers] the one collected per question when
  /// a request asks more than one.
  final AnswerAsk? onAnswer;

  /// Opens the studio's new-work dialog: the page's primary action
  /// (design.md, "Starting work"). Null leaves the header without one rather
  /// than drawing a button that opens nothing.
  final VoidCallback? onNewWork;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

  /// The header's one primary action: start work. It comes first, and it is
  /// the only primary the page has (§5).
  List<Widget> _actions() {
    final newWork = onNewWork;
    if (newWork == null) return const [];
    return [Btn.primary('New', icon: Glyphs.plus, onTap: newWork)];
  }

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    // A state that would replace the page yields to any source that answered:
    // a dead feed must not hide the projects and asks a healthy one reported.
    final anyLoaded = overview.sources.any((source) => source.loaded);
    if (state != null && state != StateKind.stale && !anyLoaded) {
      return PageBody(
        title: 'Overview',
        actions: _actions(),
        children: [_stateMessage(state)],
      );
    }
    final freshness = _freshness();
    return PageBody(
      title: 'Overview',
      subtitle: _subtitle(),
      width: PageWidth.full,
      actions: _actions(),
      children: [
        if (freshness.isNotEmpty) ...[
          ...freshness,
          const SizedBox(height: Space.s16),
        ],
        LayoutBuilder(
          builder: (context, constraints) {
            final main = Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                ..._needsYou(context),
                const SizedBox(height: Space.s24),
                ..._pullRequests(context),
                if (pulls.isNotEmpty || overview.moreInProgress > 0)
                  const SizedBox(height: Space.s24),
                ..._running(context),
                const SizedBox(height: Space.s24),
                ..._projects(context),
                if (automations.isNotEmpty) ...[
                  const SizedBox(height: Space.s24),
                  ..._automations(context),
                ],
              ],
            );
            final rail = _rail(context);
            if (constraints.maxWidth < Layout.medium) {
              return Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  main,
                  const SizedBox(height: Space.s24),
                  rail,
                ],
              );
            }
            return Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(child: main),
                const SizedBox(width: Space.s24),
                SizedBox(width: Layout.inspector, child: rail),
              ],
            );
          },
        ),
      ],
    );
  }

  /// One row per source that is not well: what is held, how old it is, and
  /// what did not arrive (§8). A healthy source says nothing here, and one
  /// still making its first attempt says nothing either.
  List<Widget> _freshness() {
    final retry = StateAction('Read again', onTap: onRetry ?? () {});
    final rows = [
      for (final source in overview.sources)
        if (source.stale || source.unavailable)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s12),
            child: _freshnessRow(source, retry),
          ),
    ];
    // A screen the shell calls stale whose sources all count as healthy (a
    // stream that stopped without a failed read) still marks the age: §7
    // never presents old data as current.
    if (rows.isEmpty && state == StateKind.stale) {
      return [
        StateMessage.stale(
          age: _oldestSeen() ?? 'now',
          detail: 'The fleet has not answered; this is what it last said.',
        ),
      ];
    }
    return rows;
  }

  /// The age of the oldest snapshot any source holds, in the design system's
  /// words, or null when nothing has been read yet.
  String? _oldestSeen() {
    DateTime? oldest;
    for (final source in overview.sources) {
      final read = source.lastRead;
      if (read == null) continue;
      if (oldest == null || read.isBefore(oldest)) oldest = read;
    }
    return oldest == null ? null : relativeAge(oldest, now);
  }

  /// One source's row, in that source's own words.
  ///
  /// A stale source holds a snapshot, so its age is the age of the read that
  /// made it; a source that never answered holds nothing, so it says so
  /// rather than carrying an age that would be a guess.
  Widget _freshnessRow(SourceState source, StateAction retry) {
    final lastRead = source.lastRead;
    if (source.stale && lastRead != null) {
      return StateMessage.stale(
        age: relativeAge(lastRead, now),
        detail: source.words(now),
      );
    }
    return StateMessage.error(message: source.words(now), retry: retry);
  }

  /// The watches that tripped, newest first, and what each did when it did
  /// (F7 plan 1, Task 3). It opens the Automations place.
  List<Widget> _automations(BuildContext context) {
    return [
      Label('Automations', count: automations.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final watch in automations)
          RowItem(
            name: manyHosts ? '${watch.host} · ${watch.name}' : watch.name,
            description: '${watch.agent} · ${watch.action}',
            value: watch.age,
            onTap: onSelect == null
                ? null
                : () => onSelect!(const Place(PlaceKind.automations)),
          ),
      ]),
    ];
  }

  /// What is waiting: the cards the model hands over, the true total beside
  /// them, and Needs you one link away.
  List<Widget> _needsYou(BuildContext context) {
    final asks = overview.needsYou;
    final more = overview.needsYouTotal - asks.length;
    return [
      Label(
        'Needs you',
        count: overview.needsYouTotal,
        link: LabelLink(
          'Open Needs you',
          onTap: _open(const Place(PlaceKind.inbox)) ?? () {},
        ),
      ),
      const SizedBox(height: Space.s12),
      if (asks.isEmpty)
        StateMessage.empty(
          message: 'Nothing is waiting on you.',
          action: StateAction(
            'Open the Needs you queue',
            onTap: _open(const Place(PlaceKind.inbox)) ?? () {},
          ),
        )
      else
        for (final ask in asks) ...[
          AnswerableAsk(
            key: ValueKey('${ask.host}|${ask.id}'),
            ask: ask,
            placement: AskPlacement.overview,
            now: now,
            onAnswer: onAnswer,
            onOpenSource: ask.thread == null ? null : _thread(ask.thread!),
            onOpenInbox: _open(const Place(PlaceKind.inbox)),
          ),
          const SizedBox(height: Space.s8),
        ],
      if (more > 0)
        Text(
          '$more more in Needs you.',
          style: Ember.of(context).small.tint(Ember.of(context).muted),
        ),
    ];
  }

  /// Ready and failing pull requests, directly under Needs you (F5 decision
  /// 14): the two states where a person has something to do. The place is one
  /// link away, and a card opens the thread it came from.
  List<Widget> _pullRequests(BuildContext context) {
    final shown = [
      for (final pull in pulls)
        if (pull.group == PullGroup.ready || pull.group == PullGroup.failing)
          pull,
    ];
    final open = _open(const Place(PlaceKind.pulls));
    final more = overview.moreInProgress;
    if (shown.isEmpty && more == 0) return const [];
    return [
      if (shown.isNotEmpty) ...[
        Label(
          'Pull requests',
          count: shown.length,
          link: open == null
              ? null
              : LabelLink('Open pull requests', onTap: open),
        ),
        const SizedBox(height: Space.s12),
        for (final pull in shown)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s8),
            child: PrCard(
              pull: pullFactsOf(pull, now: now),
              actions: PrActions(openSource: _pullSource(pull)),
            ),
          ),
      ],
      // What the two cards leave out, in the mock's words: it is still work in
      // flight, so it is one line beneath them and never a card of its own
      // (F9 decision 7).
      if (more > 0)
        Text(
          '$more more in progress with checks passing or running',
          style: Ember.of(context).small.tint(Ember.of(context).muted),
        ),
    ];
  }

  /// Opens the thread a pull request came from, when the fleet lists it.
  VoidCallback? _pullSource(PullView pull) {
    final thread = pull.thread;
    return thread == null ? null : _thread(thread);
  }

  /// Every thread with a turn in flight, most recently active first.
  List<Widget> _running(BuildContext context) {
    final running = overview.running;
    return [
      Label('Running now', count: running.length),
      const SizedBox(height: Space.s12),
      if (running.isEmpty)
        Text(
          'No turn is in flight.',
          style: Ember.of(context).body.tint(Ember.of(context).muted),
        )
      else
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s8,
          children: [
            for (final thread in running)
              ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: Layout.inspector),
                child: Box(
                  onTap: _thread(thread),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          const Dot(Status.running),
                          const SizedBox(width: Space.s8),
                          Flexible(
                            child: Text(
                              thread.title,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: Ember.of(context).bodyStrong,
                            ),
                          ),
                          const SizedBox(width: Space.s8),
                          Text(
                            relativeAge(thread.lastActive, now),
                            style: Ember.of(context).monoSmall
                                .tint(Ember.of(context).muted),
                          ),
                        ],
                      ),
                      if (thread.headline.isNotEmpty ||
                          thread.stepsDone != null) ...[
                        const SizedBox(height: Space.s4),
                        Text(
                          _detail(thread),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: Ember.of(context).small
                              .tint(Ember.of(context).muted),
                        ),
                      ],
                    ],
                  ),
                ),
              ),
          ],
        ),
    ];
  }

  /// Every project and what its threads are doing.
  List<Widget> _projects(BuildContext context) {
    final e = Ember.of(context);
    final projects = overview.projects;
    return [
      const Label('Projects'),
      const SizedBox(height: Space.s12),
      if (projects.isEmpty)
        Text('No project yet.', style: e.body.tint(e.muted))
      else
        Rows([
          for (final project in projects)
            RowItem(
              name: project.name,
              description: _projectDetail(project),
              value: _projectWorth(project),
              onTap: onSelect == null
                  ? null
                  : () => onSelect!(Place(PlaceKind.project, id: project.id)),
            ),
        ]),
    ];
  }

  /// A project row's description: its thread total, then the counts it breaks
  /// into. The total is what the row's value used to carry; the value now
  /// belongs to what the bridge answered about the project (F9 decision 7).
  static String _projectDetail(ProjectSummary project) {
    final counts = _counts(project);
    if (project.threads == 0) return counts;
    final threads = project.threads == 1
        ? '1 thread'
        : '${project.threads} threads';
    return '$threads · $counts';
  }

  /// A project row's value: its pull requests' state, then what it spent over
  /// the usage window. Only the parts that are not zero are named, and a row
  /// with nothing to name draws an em dash rather than an empty slot.
  static String _projectWorth(ProjectSummary project) {
    final ci = [
      if (project.ciReady > 0) '${project.ciReady} ready',
      if (project.ciFailing > 0) '${project.ciFailing} failing',
    ].join(' · ');
    final spend = project.spendUSD > 0
        ? '\$${project.spendUSD.toStringAsFixed(2)} this week'
        : '';
    return [
      if (ci.isNotEmpty) ci else if (spend.isEmpty) '—',
      if (spend.isNotEmpty) spend,
    ].join(' · ');
  }

  /// The day's spend against its cap. The bar is `muted` unless the spend is
  /// holding work back — a host reported the cap blocking work, or the cap is
  /// used up — which is when a meter reports a fault (design-system.md §5).
  Widget _spendMeter(SpendSummary spend) {
    final value =
        '\$${spend.spentUSD.toStringAsFixed(2)} of '
        '\$${spend.capUSD.toStringAsFixed(2)}';
    final fault = spend.blocked || spend.fraction >= 1;
    final tooltip = spend.blocked
        ? 'The daily cap is holding work back'
        : spend.fraction >= 1
        ? 'The daily cap is used up'
        : 'Today’s spend against the daily cap';
    return fault
        ? Meter.fault(
            label: 'Today',
            value: value,
            fraction: spend.fraction,
            tooltip: tooltip,
          )
        : Meter(
            label: 'Today',
            value: value,
            fraction: spend.fraction,
            tooltip: tooltip,
          );
  }

  /// The side rail: what each host is carrying, and the day's spend.
  Widget _rail(BuildContext context) {
    final spend = overview.spend;
    final split = _roleSplit();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const Label('Capacity'),
        const SizedBox(height: Space.s12),
        for (final host in overview.capacity) ...[
          Meter(
            label: host.host,
            value: _capacity(host),
            // The share of the fleet's threads this host runs: the only
            // denominator the contract gives, so the only one drawn.
            fraction: _share(host),
            tooltip: '${host.threads} of the fleet’s threads · ${_reach(host)}',
          ),
          const SizedBox(height: Space.s12),
        ],
        if (spend != null) ...[
          const SizedBox(height: Space.s12),
          const Label('Spend'),
          const SizedBox(height: Space.s12),
          _spendMeter(spend),
          // The days behind today, as the mock's rail draws them: a shape,
          // not a table (the spark says nothing about values).
          if (overview.spendDays.isNotEmpty) ...[
            const SizedBox(height: Space.s8),
            Spark([
              for (final point in overview.spendDays) point.costUSD,
            ], semanticLabel: 'Daily spend'),
          ],
          if (split != null) ...[
            const SizedBox(height: Space.s8),
            Text(
              split,
              style: Ember.of(context).small.tint(Ember.of(context).muted),
            ),
          ],
        ],
        const SizedBox(height: Space.s24),
        const Label('Activity'),
        const SizedBox(height: Space.s12),
        if (overview.activity.isEmpty)
          // The rail card is not a page state: an empty feed says so in one
          // quiet line rather than offering a second action beside the
          // page's own (design-system.md §7 keeps one action per state).
          Text(
            'Nothing has happened yet.',
            style: Ember.of(context).small.tint(Ember.of(context).muted),
          )
        else
          Rows([
            for (final event in overview.activity)
              RowItem(
                name: _eventWords(event.event),
                description: _eventDetail(event),
                value: _eventAge(event.ts),
              ),
          ]),
      ],
    );
  }

  /// An audit event's kind, worded for a person: `agent.spawn` reads
  /// "Agent spawn". An event whose kind is one bare word keeps it.
  static String _eventWords(String event) {
    final words = [
      for (final word in event.split(RegExp(r'[._\-\s]+')))
        if (word.isNotEmpty) word,
    ];
    if (words.isEmpty) return event;
    final first = words.first;
    return [
      '${first[0].toUpperCase()}${first.substring(1)}',
      ...words.skip(1),
    ].join(' ');
  }

  /// What the event says beyond its kind: the operator's reason first, then
  /// the event's own detail, then the agent it happened to — a spawn carries
  /// nothing else, and a row that says only "Spawn" does not say what spawned
  /// — and nothing when it names none of them.
  String _eventDetail(AuditEvent event) {
    final reason = event.reason;
    if (reason != null && reason.isNotEmpty) return reason;
    final detail = event.detail;
    if (detail != null && detail.isNotEmpty) return detail;
    final agent = event.agentID;
    if (agent == null || agent.isEmpty) return '';
    // An agent the store no longer holds is named the way the sidebar names
    // one with no title: by its short id.
    return overview.agentNames[agent] ??
        'Agent ${agent.length > 8 ? agent.substring(0, 8) : agent}';
  }

  /// How long ago the event happened, in the sidebar's words.
  String _eventAge(String ts) {
    final at = DateTime.tryParse(ts);
    return at == null ? '' : relativeAge(at, now);
  }

  /// A running tile's one detail line: which step the turn is on, and what
  /// the agent is doing. A step is a number and never a fraction: a turn's
  /// steps are discovered as the model narrates, so there is no total to name
  /// (F9 decision 3).
  static String _detail(model.ThreadRef thread) {
    final step = thread.stepsDone;
    return [
      if (step != null) 'step $step',
      if (thread.headline.isNotEmpty) thread.headline,
    ].join(' · ');
  }

  /// The window's spend by role, largest first, as shares of the whole: at
  /// most three roles, and none too small to name a percentage of its own.
  /// The first word is capitalised so the line reads as a sentence.
  String? _roleSplit() {
    final roles = overview.spendByRole;
    final total = roles.fold<double>(0, (sum, role) => sum + role.costUSD);
    if (total <= 0) return null;
    final named = <String>[];
    for (final role in roles) {
      final share = role.costUSD / total * 100;
      if (share < 1) continue;
      named.add('${role.key} ${share.round()}%');
      if (named.length == 3) break;
    }
    if (named.isEmpty) return null;
    final line = named.join(' · ');
    return '${line[0].toUpperCase()}${line.substring(1)}';
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction('Read the fleet again', onTap: onRetry ?? () {});
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
      // Name the host that refused the token: it is the one to sign in to.
      StateKind.unauthorized => StateMessage.unauthorized(
        host: hosts.isEmpty
            ? 'The daemon'
            : hosts
                  .firstWhere((host) => host.refused, orElse: () => hosts.first)
                  .name,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: hosts.isEmpty ? 'The daemon' : hosts.first.name,
        // No read has made a snapshot, so there is no age to give; when one
        // has, its own age is the honest one.
        lastSeen: _oldestSeen(),
        action: retry,
      ),
      // Unreachable: a stale screen keeps its content and draws each source's
      // own row (`_freshness`), never a whole-page message.
      StateKind.stale => const SizedBox.shrink(),
      StateKind.planned => const StateMessage.planned(
        name: 'Overview',
        note: 'Not built yet',
      ),
    };
  }

  /// The line under the title: how much is running and how much waits.
  String _subtitle() {
    final running = overview.running.length;
    final waiting = overview.needsYouTotal;
    final threads = running == 1 ? '1 turn running' : '$running turns running';
    if (waiting == 0) return threads;
    return '$threads · ${waiting == 1 ? '1 waiting on you' : '$waiting waiting on you'}';
  }

  /// A project's threads, by status, in the design system's words.
  static String _counts(ProjectSummary project) {
    final parts = [
      if (project.needsYou > 0) '${project.needsYou} needs you',
      if (project.running > 0) '${project.running} running',
      if (project.ready > 0) '${project.ready} ready',
      if (project.failed > 0) '${project.failed} failed',
      if (project.idle > 0) '${project.idle} idle',
    ];
    return parts.isEmpty ? '0 threads' : parts.join(' · ');
  }

  /// What a host carries, in words: its threads, how many wait on you, and
  /// — when it is not answering — how long since it did.
  String _capacity(HostCapacity host) {
    final threads = host.threads == 1 ? '1 thread' : '${host.threads} threads';
    final parts = [
      threads,
      if (host.needsYou > 0) '${host.needsYou} needs you',
      if (!_live(host)) _reach(host),
    ];
    return parts.join(' · ');
  }

  /// Whether the host is answering now.
  bool _live(HostCapacity host) {
    for (final view in hosts) {
      if (view.name == host.host) return view.live;
    }
    return true;
  }

  /// The host's reach, as its row says it.
  String _reach(HostCapacity host) {
    for (final view in hosts) {
      if (view.name != host.host) continue;
      return view.reachWords(now);
    }
    return 'not listed';
  }

  double _share(HostCapacity host) {
    var total = 0;
    for (final other in overview.capacity) {
      total += other.threads;
    }
    return total == 0 ? 0 : host.threads / total;
  }

  /// Opening a thread by the place that names it.
  VoidCallback? _thread(model.ThreadRef thread) => onSelect == null
      ? null
      : () => onSelect!(
          Place(PlaceKind.thread, host: thread.host, id: thread.id),
        );

  VoidCallback? _open(Place place) =>
      onSelect == null ? null : () => onSelect!(place);
}
