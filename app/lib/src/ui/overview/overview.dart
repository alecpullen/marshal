import '../../model/age.dart';
import '../../model/fleet_store.dart' show HostView;
import '../../model/inbox_model.dart' as model;
import '../../model/overview_model.dart';
import '../../model/place.dart';
import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';

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

  /// The fleet's data state, when it is not ready (§7).
  final StateKind? state;

  /// Opens a thread, a project or the Inbox.
  final void Function(Place place)? onSelect;

  /// Answers an ask in place, without leaving the Overview.
  final void Function(model.Ask ask, model.AskAction action)? onAnswer;

  /// Reads the fleet again, for the one action a state offers.
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: 'Overview', children: [_stateMessage(state)]);
    }
    return PageBody(
      title: 'Overview',
      subtitle: _subtitle(),
      width: PageWidth.full,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: _oldestContact(),
            detail: 'The fleet has not answered; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        LayoutBuilder(
          builder: (context, constraints) {
            final main = Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                ..._needsYou(context),
                const SizedBox(height: Space.s24),
                ..._running(context),
                const SizedBox(height: Space.s24),
                ..._projects(context),
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

  /// What is waiting, oldest first: at most three, and the Inbox one link
  /// away.
  List<Widget> _needsYou(BuildContext context) {
    // The three oldest, whatever the model hands over: the rest are the
    // Inbox's, one link away.
    final asks = overview.needsYou.take(overviewAsks).toList();
    return [
      Label(
        'Needs you',
        count: asks.length,
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
            source: ask.thread == null
                ? null
                : '${ask.thread!.project.split('/').last} › ${ask.thread!.title}',
            onOpenSource: ask.thread == null ? null : _thread(ask.thread!),
            placement: AskPlacement.overview,
            actions: [
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
    ];
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
    final e = Ember.of(context);
    final spend = overview.spend;
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
        ],
        const SizedBox(height: Space.s12),
        Text(
          'Activity arrives with the thread screen in F3.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
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
      StateKind.unavailable => StateMessage.unavailable(
        host: hosts.isEmpty ? 'The daemon' : hosts.first.name,
        lastSeen: _oldestContact(),
        action: retry,
      ),
      StateKind.stale => StateMessage.stale(age: _oldestContact()),
      StateKind.planned => const StateMessage.planned(
        name: 'Overview',
        note: 'Not built yet',
      ),
    };
  }

  /// The line under the title: how much is running and how much waits.
  String _subtitle() {
    final running = overview.running.length;
    final waiting = overview.needsYou.length;
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

  /// How long ago the fleet last said anything.
  String _oldestContact() {
    DateTime? oldest;
    for (final host in hosts) {
      final contact = host.lastContact;
      if (contact == null) continue;
      if (oldest == null || contact.isBefore(oldest)) oldest = contact;
    }
    return relativeAge(oldest ?? now, now);
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
  static AskKind _askKind(model.AskKind kind) => switch (kind) {
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
  };
}
