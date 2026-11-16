import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../../model/place.dart';
import '../kit/kit.dart';

/// Which asks the Inbox shows: everything, what blocks work, and what is
/// there to look at (F2 decision 7).
enum InboxFilter { all, blocking, lookAt }

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
    this.state,
    this.onSelect,
    this.onAnswer,
    this.onRetry,
  });

  final List<model.Ask> asks;

  /// What a relative age is measured against.
  final DateTime now;

  /// The fleet's data state, when it is not ready (§7).
  final StateKind? state;

  final void Function(Place place)? onSelect;

  /// Posts an answer. [reply] is the free text of a question's reply.
  final void Function(model.Ask ask, model.AskAction action, {String? reply})?
  onAnswer;

  final VoidCallback? onRetry;

  @override
  State<Inbox> createState() => _InboxState();
}

class _InboxState extends State<Inbox> {
  InboxFilter _filter = InboxFilter.all;

  /// The ask whose reply field is open, if any.
  String? _replying;
  final _reply = TextEditingController();

  /// The action each ask was last answered with, so a failure's retry posts
  /// the same one rather than guessing.
  final _lastAction = <String, model.AskAction>{};

  @override
  void dispose() {
    _reply.dispose();
    super.dispose();
  }

  bool _blocking(model.Ask ask) => ask.kind != model.AskKind.intake;

  List<model.Ask> get _shown => switch (_filter) {
    InboxFilter.all => widget.asks,
    InboxFilter.blocking => widget.asks.where(_blocking).toList(),
    InboxFilter.lookAt => widget.asks.where((ask) => !_blocking(ask)).toList(),
  };

  @override
  Widget build(BuildContext context) {
    final state = widget.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: 'Inbox', children: [_stateMessage(state)]);
    }
    final shown = _shown;
    return PageBody(
      title: 'Inbox',
      subtitle: _subtitle(),
      width: PageWidth.list,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: 'now',
            detail: 'The fleet has not answered; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        Tabs(
          index: _filter.index,
          onChanged: (index) =>
              setState(() => _filter = InboxFilter.values[index]),
          tabs: [
            TabItem('All', count: widget.asks.length),
            TabItem('Blocking', count: widget.asks.where(_blocking).length),
            TabItem(
              'To look at',
              count: widget.asks.where((ask) => !_blocking(ask)).length,
            ),
          ],
        ),
        const SizedBox(height: Space.s16),
        if (shown.isEmpty)
          StateMessage.empty(
            message: 'Nothing is waiting on you.',
            action: StateAction(
              'Read the fleet again',
              onTap: widget.onRetry ?? () {},
            ),
          )
        else
          for (final ask in shown) _card(ask),
      ],
    );
  }

  /// One ask as its card, with its own pending or failed state under it.
  Widget _card(model.Ask ask) {
    final sending = ask.state == model.AskState.sending;
    final failed = ask.state == model.AskState.failed;
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          AskCard(
            kind: _kind(ask.kind),
            title: ask.title,
            body: ask.body.isEmpty ? null : ask.body,
            waiting: sending ? 'sending…' : relativeAge(ask.since, widget.now),
            waitingSince: ask.since.toLocal().toString(),
            source: _source(ask),
            onOpenSource: _thread(ask),
            placement: AskPlacement.inbox,
            actions: [
              for (final action in ask.actions)
                AskAction(
                  action.label,
                  onTap: sending ? null : () => _choose(ask, action),
                ),
            ],
          ),
          if (_replying == ask.id) _replyField(ask),
          if (failed) _failure(ask),
        ],
      ),
    );
  }

  /// A chosen action: a reply opens its field, and anything else is posted.
  void _choose(model.Ask ask, model.AskAction action) {
    if (ask.kind == model.AskKind.question && action.label == _replyLabel) {
      setState(() {
        _replying = ask.id;
        _reply.clear();
      });
      return;
    }
    _lastAction[ask.id] = action;
    widget.onAnswer?.call(ask, action);
  }

  /// The free reply, once it has words.
  Widget _replyField(model.Ask ask) {
    final reply = ask.actions.where((action) => action.label == _replyLabel);
    return Padding(
      padding: const EdgeInsets.only(top: Space.s8, left: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Field(
            label: 'Your answer',
            controller: _reply,
            hint: 'What the agent should hear',
            onSubmitted: (_) => _sendReply(ask, reply.firstOrNull),
          ),
          const SizedBox(height: Space.s8),
          Row(
            children: [
              Btn.primary(
                'Send',
                onTap: () => _sendReply(ask, reply.firstOrNull),
              ),
              const SizedBox(width: Space.s8),
              Btn('Cancel', onTap: () => setState(() => _replying = null)),
            ],
          ),
        ],
      ),
    );
  }

  void _sendReply(model.Ask ask, model.AskAction? reply) {
    if (reply == null) return;
    _lastAction[ask.id] = reply;
    widget.onAnswer?.call(ask, reply, reply: _reply.text);
    setState(() => _replying = null);
  }

  /// What went wrong, and the one action a failure offers: the same answer
  /// again.
  Widget _failure(model.Ask ask) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: Space.s8, left: Space.s12),
      child: Wrap(
        spacing: Space.s8,
        runSpacing: Space.s8,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text(
            ask.error ?? 'The answer did not go through.',
            style: e.small.tint(e.err),
          ),
          Btn('Try again', onTap: () => _retry(ask)),
        ],
      ),
    );
  }

  void _retry(model.Ask ask) {
    final action = _lastAction[ask.id] ?? ask.actions.firstOrNull;
    if (action == null) return;
    widget.onAnswer?.call(ask, action);
  }

  /// Where the ask came from, as the card's source line: its project and its
  /// thread.
  String? _source(model.Ask ask) {
    final thread = ask.thread;
    if (thread == null) return null;
    final project = thread.project.split('/').last;
    return '$project › ${thread.title}';
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
    final blocking = widget.asks.where(_blocking).length;
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
      StateKind.unavailable => StateMessage.unavailable(
        host: 'The daemon',
        lastSeen: '4m',
        action: retry,
      ),
      StateKind.stale => StateMessage.stale(age: '4m'),
      StateKind.planned => const StateMessage.planned(
        name: 'Inbox',
        note: 'Not built yet',
      ),
    };
  }

  static const _replyLabel = 'Reply';

  static AskKind _kind(model.AskKind kind) => switch (kind) {
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
  };
}
