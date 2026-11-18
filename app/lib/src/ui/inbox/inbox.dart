import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../../model/place.dart';
import '../../model/pulls_model.dart';
import '../../model/source_state.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';
import 'answerable_ask.dart';

/// Which asks the Inbox shows: everything, what blocks work, what is there to
/// look at, and — from F5 — the pull requests that wait for review.
enum InboxFilter { all, blocking, lookAt, ready }

/// The Inbox: every ask the fleet reported, oldest first, answered from its
/// own card.
///
/// It draws the asks it is given and posts through [onAnswer]; it never
/// fetches (design-system.md §5). An answer shows as pending on its card and
/// the card leaves only when the asks it is given stop carrying it (§7).
class Inbox extends StatefulWidget {
  const Inbox({
    super.key,
    required this.asks,
    required this.now,
    this.pulls = const [],
    this.state,
    this.sources = const [],
    this.onSelect,
    this.onAnswer,
    this.onRetry,
  });

  final List<model.Ask> asks;

  /// The pull requests waiting for review (F5 decision 14): the Ready filter
  /// lists them, because a ready pull request is something to look at rather
  /// than something blocking work.
  final List<PullView> pulls;

  /// What a relative age is measured against.
  final DateTime now;

  /// The fleet's data state, when it is not ready (§7).
  final StateKind? state;

  /// Each feed's freshness, by host then source: one line per source that is
  /// not answering, so a dead feed sits beside a healthy one instead of
  /// replacing it.
  final List<SourceState> sources;

  final void Function(Place place)? onSelect;

  /// Posts an answer. [reply] is the free text of a question's reply, and
  /// [answers] the answer collected for each question of a request that asks
  /// more than one.
  final AnswerAsk? onAnswer;

  final VoidCallback? onRetry;

  @override
  State<Inbox> createState() => _InboxState();
}

class _InboxState extends State<Inbox> {
  InboxFilter _filter = InboxFilter.all;

  /// The pull requests the Ready filter lists: the ready ones only, whatever
  /// the host hands over.
  List<PullView> get _ready => [
    for (final pull in widget.pulls)
      if (pull.group == PullGroup.ready) pull,
  ];

  List<model.Ask> get _shown => switch (_filter) {
    InboxFilter.all => widget.asks,
    InboxFilter.blocking => widget.asks.where((ask) => ask.blocking).toList(),
    InboxFilter.lookAt => widget.asks.where((ask) => !ask.blocking).toList(),
    // The Ready filter is not about asks: [build] draws pull requests for it.
    InboxFilter.ready => const [],
  };

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    // A state that would replace the page yields to any source that answered:
    // a dead feed must not hide the asks a healthy one reported.
    final anyLoaded = widget.sources.any((source) => source.loaded);
    if (state != null && state != StateKind.stale && !anyLoaded) {
      return PageBody(title: 'Inbox', children: [_stateMessage(state)]);
    }
    final shown = _shown;
    final freshness = _freshness();
    return PageBody(
      title: 'Inbox',
      subtitle: _subtitle(),
      width: PageWidth.list,
      children: [
        if (freshness.isNotEmpty) ...[
          ...freshness,
          const SizedBox(height: Space.s16),
        ],
        Tabs(
          index: _filter.index,
          onChanged: (index) =>
              setState(() => _filter = InboxFilter.values[index]),
          tabs: [
            TabItem('All', count: widget.asks.length),
            TabItem(
              'Blocking',
              count: widget.asks.where((ask) => ask.blocking).length,
            ),
            TabItem(
              'To look at',
              count: widget.asks.where((ask) => !ask.blocking).length,
            ),
            TabItem('Ready', count: _ready.length),
          ],
        ),
        const SizedBox(height: Space.s16),
        if (_filter == InboxFilter.ready)
          ..._readyCards()
        else if (shown.isEmpty)
          _emptyState()
        else
          for (final ask in shown)
            Padding(
              padding: const EdgeInsets.only(bottom: Space.s12),
              child: AnswerableAsk(
                key: ValueKey('${ask.host}|${ask.id}'),
                ask: ask,
                placement: AskPlacement.inbox,
                now: widget.now,
                onAnswer: widget.onAnswer,
                onOpenSource: _thread(ask),
              ),
            ),
      ],
    );
  }

  /// What an empty list says: when a filter is what emptied it, it names that
  /// filter and offers the way back to every ask.
  Widget _emptyState() {
    final all = StateAction(
      'Show every ask',
      onTap: () => setState(() => _filter = InboxFilter.all),
    );
    return switch (_filter) {
      InboxFilter.blocking => StateMessage.empty(
        message: 'Nothing is blocking work.',
        action: all,
      ),
      InboxFilter.lookAt => StateMessage.empty(
        message: 'Nothing is waiting to be looked at.',
        action: all,
      ),
      InboxFilter.all || InboxFilter.ready => StateMessage.empty(
        message: 'Nothing is waiting on you.',
        action: StateAction(
          'Read the fleet again',
          onTap: widget.onRetry ?? () {},
        ),
      ),
    };
  }

  /// One row per source that is not well: what is held, how old it is, and
  /// what did not arrive (§8). A healthy source says nothing here, and one
  /// still making its first attempt says nothing either.
  List<Widget> _freshness() {
    final retry = StateAction('Read again', onTap: widget.onRetry ?? () {});
    final rows = [
      for (final source in widget.sources)
        if (source.stale || source.unavailable)
          Padding(
            padding: const EdgeInsets.only(bottom: Space.s12),
            child: _freshnessRow(source, retry),
          ),
    ];
    // A screen the shell calls stale whose sources all count as healthy (a
    // stream that stopped without a failed read) still marks the age: §7
    // never presents old data as current.
    if (rows.isEmpty && widget.state == StateKind.stale) {
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
    for (final source in widget.sources) {
      final read = source.lastRead;
      if (read == null) continue;
      if (oldest == null || read.isBefore(oldest)) oldest = read;
    }
    return oldest == null ? null : relativeAge(oldest, widget.now);
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
        age: relativeAge(lastRead, widget.now),
        detail: source.words(widget.now),
      );
    }
    return StateMessage.error(message: source.words(widget.now), retry: retry);
  }

  /// The Ready filter's list: one card per pull request that waits for
  /// review, or the empty state when none does.
  List<Widget> _readyCards() {
    final ready = _ready;
    if (ready.isEmpty) {
      return [
        StateMessage.empty(
          message: 'No pull request is waiting for review.',
          action: StateAction(
            'Open pull requests',
            onTap: widget.onSelect == null
                ? (widget.onRetry ?? () {})
                : () => widget.onSelect!(const Place(PlaceKind.pulls)),
          ),
        ),
      ];
    }
    return [
      for (final pull in ready)
        Padding(
          padding: const EdgeInsets.only(bottom: Space.s12),
          child: PrCard(
            pull: pullFactsOf(pull, now: widget.now),
            actions: PrActions(openSource: _pullSource(pull)),
          ),
        ),
    ];
  }

  /// Opens the thread a pull request came from, when the fleet lists it.
  VoidCallback? _pullSource(PullView pull) {
    final thread = pull.thread;
    final select = widget.onSelect;
    if (thread == null || select == null) return null;
    return () =>
        select(Place(PlaceKind.thread, host: thread.host, id: thread.id));
  }

  VoidCallback? _thread(model.Ask ask) {
    final thread = ask.thread;
    final select = widget.onSelect;
    if (thread == null || select == null) return null;
    return () =>
        select(Place(PlaceKind.thread, host: thread.host, id: thread.id));
  }

  /// The line under the title: what is waiting, in the design system's words.
  String _subtitle() {
    final waiting = widget.asks.length;
    if (waiting == 0) return 'Nothing is waiting on you';
    final blocking = widget.asks.where((ask) => ask.blocking).length;
    final noun = waiting == 1 ? 'ask' : 'asks';
    if (blocking == 0) return '$waiting $noun to look at';
    return '$waiting $noun · $blocking blocking work';
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the fleet again',
      onTap: widget.onRetry ?? () {},
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message: 'Nothing is waiting on you.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The inbox could not be read.',
        retry: retry,
      ),
      StateKind.unauthorized => const StateMessage.unauthorized(),
      StateKind.unavailable => StateMessage.unavailable(
        host: 'The daemon',
        // No read has made a snapshot, so there is no age to give, and the
        // kit says "Not seen yet" rather than inventing one.
        lastSeen: _oldestSeen(),
        action: retry,
      ),
      // Unreachable: a stale screen keeps its content and draws each source's
      // own row (`_freshness`), never a whole-page message.
      StateKind.stale => const SizedBox.shrink(),
      StateKind.planned => const StateMessage.planned(
        name: 'Inbox',
        note: 'Not built yet',
      ),
    };
  }
}
