import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../../model/place.dart';
import '../../model/pulls_model.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';

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

  final void Function(Place place)? onSelect;

  /// Posts an answer. [reply] is the free text of a question's reply, and
  /// [answers] the answer collected for each question of a request that asks
  /// more than one.
  final void Function(
    model.Ask ask,
    model.AskAction action, {
    String? reply,
    List<String>? answers,
  })?
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

  /// The answer being collected for each question of a multi-question ask,
  /// by "host|ask id|question index".
  final _answers = <String, TextEditingController>{};

  /// The action each ask was last answered with, so a failure's retry posts
  /// the same one rather than guessing.
  final _lastAction = <String, model.AskAction>{};

  @override
  void didUpdateWidget(Inbox oldWidget) {
    super.didUpdateWidget(oldWidget);
    // An ask that is gone takes its drafts with it: only the asks on screen
    // keep a field, so a long session does not collect controllers.
    final live = {
      for (final ask in widget.asks)
        if (ask.questions.length > 1)
          for (var i = 0; i < ask.questions.length; i++) _answerKey(ask, i),
    };
    _answers.removeWhere((key, controller) {
      if (live.contains(key)) return false;
      controller.dispose();
      return true;
    });
  }

  @override
  void dispose() {
    _reply.dispose();
    for (final controller in _answers.values) {
      controller.dispose();
    }
    super.dispose();
  }

  bool _blocking(model.Ask ask) => ask.kind != model.AskKind.intake;

  /// The pull requests the Ready filter lists: the ready ones only, whatever
  /// the host hands over.
  List<PullView> get _ready => [
    for (final pull in widget.pulls)
      if (pull.group == PullGroup.ready) pull,
  ];

  List<model.Ask> get _shown => switch (_filter) {
    InboxFilter.all => widget.asks,
    InboxFilter.blocking => widget.asks.where(_blocking).toList(),
    InboxFilter.lookAt => widget.asks.where((ask) => !_blocking(ask)).toList(),
    // The Ready filter is not about asks: [build] draws pull requests for it.
    InboxFilter.ready => const [],
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
            TabItem('Ready', count: _ready.length),
          ],
        ),
        const SizedBox(height: Space.s16),
        if (_filter == InboxFilter.ready)
          ..._readyCards()
        else if (shown.isEmpty)
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

  /// One ask as its card, with its own pending or failed state under it.
  Widget _card(model.Ask ask) {
    final sending = ask.state == model.AskState.sending;
    final failed = ask.state == model.AskState.failed;
    final many = ask.questions.length > 1;
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
            // A request that asks several questions commits them together,
            // under the fields that collect them, so the card itself carries
            // no action row.
            actions: many
                ? const []
                : [
                    for (final action in ask.actions)
                      AskAction(action.label, onTap: _tap(ask, action)),
                  ],
          ),
          if (many) _questionFields(ask),
          if (_replying == ask.id) _replyField(ask),
          if (failed) _failure(ask),
        ],
      ),
    );
  }

  /// The questions of a multi-question ask, one answer each, and the actions
  /// that post them all.
  ///
  /// Every question is answered before anything is sent: the agent wants one
  /// answer per question, in the request's order, and reads a short list as
  /// a decline (`internal/acp/questions.go`), so a card that collected only
  /// some of them would silently lose the whole request. The kit has no
  /// single-select control (§10), so each answer is typed, with the request's
  /// own options named as the field's hint; a typed label is sent as its
  /// option's wire value.
  Widget _questionFields(model.Ask ask) {
    return Padding(
      padding: const EdgeInsets.only(top: Space.s8, left: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final (index, question) in ask.questions.indexed) ...[
            if (index > 0) const SizedBox(height: Space.s12),
            Field(
              label: question.text,
              controller: _answerFields(ask, index),
              hint: _answerHint(question),
              enabled: ask.state != model.AskState.sending,
              onChanged: (_) => setState(() {}),
            ),
          ],
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: [
              for (final (index, action) in ask.actions.indexed)
                index == 0
                    ? Btn.primary(action.label, onTap: _tap(ask, action))
                    : Btn(action.label, onTap: _tap(ask, action)),
            ],
          ),
        ],
      ),
    );
  }

  /// What [action] does when it is tapped, or null when it cannot be: an ask
  /// being sent is not answered twice, and the collected answer is posted
  /// only once every question has one.
  VoidCallback? _tap(model.Ask ask, model.AskAction action) {
    if (ask.state == model.AskState.sending) return null;
    if (action.kind == model.AskActionKind.answer && !_complete(ask)) {
      return null;
    }
    return () => _choose(ask, action);
  }

  /// The hint under a question's field: the answers the request offers, and
  /// that several may be named.
  static String? _answerHint(model.AskQuestion question) {
    if (question.options.isEmpty) return null;
    final labels = [for (final option in question.options) option.label];
    final list = labels.join(' · ');
    return question.multi ? '$list · several, comma-separated' : list;
  }

  String _answerKey(model.Ask ask, int index) => '${ask.host}|${ask.id}|$index';

  TextEditingController _answerFields(model.Ask ask, int index) =>
      _answers.putIfAbsent(_answerKey(ask, index), TextEditingController.new);

  /// The answer collected for one question.
  String _answerAt(model.Ask ask, int index) =>
      _answerFields(ask, index).text.trim();

  /// Whether every question of [ask] has an answer.
  bool _complete(model.Ask ask) =>
      [for (var i = 0; i < ask.questions.length; i++) _answerAt(ask, i)]
          .every((answer) => answer.isNotEmpty);

  /// Posts the answer collected for every question, in the request's order.
  void _submit(model.Ask ask) {
    final action = ask.actions
        .where((action) => action.kind == model.AskActionKind.answer)
        .firstOrNull;
    if (action == null || !_complete(ask)) return;
    _lastAction[ask.id] = action;
    widget.onAnswer?.call(
      ask,
      action,
      answers: [
        for (var i = 0; i < ask.questions.length; i++) _answerAt(ask, i),
      ],
    );
  }

  /// A chosen action: a reply opens its field, a collected answer posts every
  /// question's, and anything else is posted as it is.
  void _choose(model.Ask ask, model.AskAction action) {
    if (ask.kind == model.AskKind.question) {
      switch (action.kind) {
        case model.AskActionKind.reply:
          setState(() {
            _replying = ask.id;
            _reply.clear();
          });
          return;
        case model.AskActionKind.answer:
          _submit(ask);
          return;
        case model.AskActionKind.decline:
        case model.AskActionKind.option:
          break;
        case model.AskActionKind.open:
        case model.AskActionKind.retry:
        case model.AskActionKind.dismiss:
          // A pull request's ask offers these; a question's does not.
          return;
      }
    }
    _lastAction[ask.id] = action;
    widget.onAnswer?.call(ask, action);
  }

  /// The free reply, once it has words.
  Widget _replyField(model.Ask ask) {
    final reply = ask.actions.where(
      (action) => action.kind == model.AskActionKind.reply,
    );
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
    if (action.kind == model.AskActionKind.answer) {
      // The fields still hold what was typed, so the collected answer is
      // rebuilt rather than posted half-empty.
      _submit(ask);
      return;
    }
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

  static AskKind _kind(model.AskKind kind) => switch (kind) {
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
    model.AskKind.pull => AskKind.pull,
    model.AskKind.reviewDraft => AskKind.review,
  };
}
