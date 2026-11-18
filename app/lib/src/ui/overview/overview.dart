import '../../model/age.dart';
import '../../model/fleet_store.dart' show HostView;
import '../../model/inbox_model.dart' as model;
import '../../model/overview_model.dart';
import '../../model/place.dart';
import '../../model/pulls_model.dart';
import '../../model/source_state.dart';
import '../../model/thread_ref.dart' as model;
import '../../model/watches_model.dart' show TrippedWatch;
import '../kit/kit.dart';
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
    this.pulls = const [],
    this.state,
    this.onSelect,
    this.onAnswer,
    this.onRetry,
  });

  final OverviewModel overview;

  /// What a relative age is measured against.
  final DateTime now;

  /// The hosts as the shell knows them: a row's reach comes from here.
  final List<HostView> hosts;

  /// The watches that tripped, newest first: the Automations card, and
  /// nothing when none has (F7 plan 1, Task 3).
  final List<TrippedWatch> automations;

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
  final void Function(
    model.Ask ask,
    model.AskAction action, {
    String? reply,
    List<String>? answers,
  })?
  onAnswer;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    // A state that would replace the page yields to any source that answered:
    // a dead feed must not hide the projects and asks a healthy one reported.
    final anyLoaded = overview.sources.any((source) => source.loaded);
    if (state != null && state != StateKind.stale && !anyLoaded) {
      return PageBody(title: 'Overview', children: [_stateMessage(state)]);
    }
    final freshness = _freshness();
    return PageBody(
      title: 'Overview',
      subtitle: _subtitle(),
      width: PageWidth.full,
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
                if (pulls.isNotEmpty) const SizedBox(height: Space.s24),
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
            name: watch.name,
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
  /// them, and the Inbox one link away.
  List<Widget> _needsYou(BuildContext context) {
    final asks = overview.needsYou;
    final more = overview.needsYouTotal - asks.length;
    return [
      Label(
        'Needs you',
        count: overview.needsYouTotal,
        link: LabelLink(
          'Open inbox',
          onTap: _open(const Place(PlaceKind.inbox)) ?? () {},
        ),
      ),
      const SizedBox(height: Space.s12),
      if (asks.isEmpty)
        StateMessage.empty(
          message: 'Nothing is waiting on you.',
          action: StateAction(
            'Open the inbox',
            onTap: _open(const Place(PlaceKind.inbox)) ?? () {},
          ),
        )
      else
        for (final ask in asks) ...[
          AskCard(
            kind: _askKind(ask.kind),
            title: ask.title,
            body: ask.body.isEmpty ? null : ask.body,
            waiting: relativeAge(ask.since, now),
            waitingSince: ask.since.toLocal().toString(),
            source: ask.provenance,
            onOpenSource: ask.thread == null ? null : _thread(ask.thread!),
            placement: AskPlacement.overview,
            actions: ask.answeredInThread
                // A work item's asks carry its version and reviewed identity,
                // so they are answered in the item's own thread: the card
                // opens it and offers nothing that would post nothing here.
                ? [
                    AskAction(
                      'Open in the factory',
                      onTap: ask.thread == null ? null : _thread(ask.thread!),
                      note: ask.thread == null
                          ? 'This ask names no work item to open.'
                          : null,
                    ),
                  ]
                : ask.questions.length > 1
                // Several questions need an answer for each, and the
                // Overview has no room to collect them: the card sends the
                // person to the Inbox, which draws every question of the
                // request. Answering here would post a partial shape, which
                // the agent reads as declined (internal/acp/questions.go).
                ? [
                    AskAction(
                      'Open inbox',
                      onTap: _open(const Place(PlaceKind.inbox)),
                    ),
                  ]
                : [
                    for (final action in ask.actions)
                      AskAction(
                        action.label,
                        onTap: ask.state == model.AskState.sending
                            ? null
                            : () => onAnswer?.call(ask, action),
                      ),
                  ],
          ),
          const SizedBox(height: Space.s8),
        ],
      if (more > 0)
        Text(
          '$more more in the inbox.',
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
    if (shown.isEmpty) return const [];
    final open = _open(const Place(PlaceKind.pulls));
    return [
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
                      if (thread.headline.isNotEmpty) ...[
                        const SizedBox(height: Space.s4),
                        Text(
                          thread.headline,
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
              description: _counts(project),
              value: '${project.threads}',
              onTap: onSelect == null
                  ? null
                  : () => onSelect!(Place(PlaceKind.project, id: project.id)),
            ),
        ]),
    ];
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
          Meter.fault(
            label: 'Today',
            value:
                '\$${spend.spentUSD.toStringAsFixed(2)} of '
                '\$${spend.capUSD.toStringAsFixed(2)}',
            fraction: spend.fraction,
            tooltip: spend.blocked
                ? 'The daily cap is holding work back'
                : 'Today’s spend against the daily cap',
          ),
          // The days behind today, as the mock's rail draws them: a shape,
          // not a table (the spark says nothing about values).
          if (overview.spendDays.isNotEmpty) ...[
            const SizedBox(height: Space.s8),
            Spark(
              [for (final point in overview.spendDays) point.costUSD],
              semanticLabel: 'Daily spend',
            ),
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
  /// the event's own detail, and nothing when it has neither.
  static String _eventDetail(AuditEvent event) {
    final reason = event.reason;
    if (reason != null && reason.isNotEmpty) return reason;
    return event.detail ?? '';
  }

  /// How long ago the event happened, in the sidebar's words.
  String _eventAge(String ts) {
    final at = DateTime.tryParse(ts);
    return at == null ? '' : relativeAge(at, now);
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

  /// An ask's kind in the kit's vocabulary (§5: one card, every ask).
  ///
  /// A work item's four asks are drawn with the kit's existing kinds (F6):
  /// the card's title and body say what is being decided, and four new kinds
  /// would be a design-system change that says nothing more.
  static AskKind _askKind(model.AskKind kind) => switch (kind) {
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
    model.AskKind.pull => AskKind.pull,
    model.AskKind.reviewDraft => AskKind.review,
    model.AskKind.network => AskKind.network,
    model.AskKind.approvalDraft => AskKind.approval,
    model.AskKind.decision => AskKind.decision,
    model.AskKind.recovery => AskKind.decision,
    model.AskKind.completion => AskKind.decision,
  };
}
