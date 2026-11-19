import '../../model/age.dart';
import '../../model/inbox_model.dart' as model;
import '../kit/kit.dart';

/// Posts an answer: the action, with the reply's words or one answer per
/// question when the ask needs them. The signature of InboxModel.answer.
typedef AnswerAsk = void Function(
  model.Ask ask,
  model.AskAction action, {
  String? reply,
  List<String>? answers,
});

/// One ask a person can answer where it is drawn: the AskCard, a reply
/// field, one field per question of a multi-question ask, and the failure
/// with Try again. The Inbox, the Overview and a thread's pinned ask all
/// draw this.
///
/// Answering it is one action wherever the card is drawn (design-system.md
/// §5): a reply opens its field here, a collected answer posts one answer per
/// question, and a failure says why and offers the same answer again. What
/// the card itself offers differs by place, and only there: the Overview has
/// no room for a request that asks several questions, so it sends the person
/// to the Inbox, and a work item's ask is answered in the item's own thread.
class AnswerableAsk extends StatefulWidget {
  const AnswerableAsk({
    super.key,
    required this.ask,
    required this.placement,
    required this.now,
    this.onAnswer,
    this.onOpenSource,
    this.onOpenInbox,
  });

  final model.Ask ask;
  final AskPlacement placement;
  final DateTime now;
  final AnswerAsk? onAnswer;
  final VoidCallback? onOpenSource;

  /// The Overview's way out for an ask it has no room to collect: a
  /// multi-question ask offers Open inbox instead of its fields.
  final VoidCallback? onOpenInbox;

  @override
  State<AnswerableAsk> createState() => _AnswerableAskState();
}

class _AnswerableAskState extends State<AnswerableAsk> {
  /// Whether this card's reply field is open. One card answers one ask, so
  /// the field, its draft and the last answer are the card's own.
  bool _replying = false;
  final _reply = TextEditingController();

  /// The answer being collected for each question of a multi-question ask,
  /// by question index.
  final _answers = <int, TextEditingController>{};

  /// The action this ask was last answered with, so a failure's retry posts
  /// the same one rather than guessing.
  model.AskAction? _lastAction;

  @override
  void dispose() {
    _reply.dispose();
    for (final controller in _answers.values) {
      controller.dispose();
    }
    super.dispose();
  }

  model.Ask get _ask => widget.ask;

  /// Whether the ask wants one answer per question, collected together.
  bool get _many => _ask.questions.length > 1;

  @override
  Widget build(BuildContext context) {
    final ask = _ask;
    final sending = ask.state == model.AskState.sending;
    final failed = ask.state == model.AskState.failed;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        AskCard(
          kind: _kind(ask.kind),
          title: ask.title,
          body: ask.body.isEmpty ? null : ask.body,
          waiting: sending ? 'sending…' : relativeAge(ask.since, widget.now),
          waitingSince: ask.since.toLocal().toString(),
          source: ask.provenance,
          onOpenSource: widget.onOpenSource,
          placement: widget.placement,
          actions: _actions(ask),
        ),
        // The Overview has no room to collect them: it offers Open inbox
        // instead, so its fields are never drawn.
        if (_many && widget.placement != AskPlacement.overview)
          _questionFields(ask),
        if (_replying) _replyField(ask),
        if (failed) _failure(ask),
      ],
    );
  }

  /// The card's action row: the one thing that differs by where it is drawn.
  ///
  /// A work item's four asks are answered in the item's own thread — they
  /// carry its version and its reviewed identity — so the Inbox shows them
  /// without a control that would do nothing, and the Overview opens the
  /// item. In their thread they keep the actions, because that is the place
  /// they are answered at. A request that asks several questions commits
  /// them together, under the fields that collect them, so its card carries
  /// no action row in the places that collect them.
  List<AskAction> _actions(model.Ask ask) {
    if (ask.answeredInThread) {
      return switch (widget.placement) {
        AskPlacement.overview => [
          AskAction(
            'Open in the factory',
            onTap: widget.onOpenSource,
            note: ask.thread == null
                ? 'This ask names no work item to open.'
                : null,
          ),
        ],
        AskPlacement.inbox => const [],
        AskPlacement.thread => _offered(ask),
      };
    }
    if (_many) {
      if (widget.placement == AskPlacement.overview) {
        // Several questions need an answer for each, and the Overview has no
        // room to collect them: the card sends the person to the Inbox,
        // which draws every question of the request. Answering here would
        // post a partial shape, which the agent reads as declined
        // (internal/acp/questions.go).
        return [AskAction('Open inbox', onTap: widget.onOpenInbox)];
      }
      // The fields under the card carry the actions that post them.
      return const [];
    }
    return _offered(ask);
  }

  /// The ask's own actions, each posting through the one answer path.
  List<AskAction> _offered(model.Ask ask) => [
    for (final action in ask.actions)
      AskAction(action.label, onTap: _tap(ask, action)),
  ];

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
              controller: _answerFields(index),
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
  /// with no answer path posts nothing, an ask being sent is not answered
  /// twice, and the collected answer is posted only once every question has
  /// one.
  VoidCallback? _tap(model.Ask ask, model.AskAction action) {
    if (widget.onAnswer == null) return null;
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

  TextEditingController _answerFields(int index) =>
      _answers.putIfAbsent(index, TextEditingController.new);

  /// The answer collected for one question.
  String _answerAt(int index) => _answerFields(index).text.trim();

  /// Whether every question of [ask] has an answer.
  bool _complete(model.Ask ask) =>
      [for (var i = 0; i < ask.questions.length; i++) _answerAt(i)]
          .every((answer) => answer.isNotEmpty);

  /// Posts the answer collected for every question, in the request's order.
  void _submit(model.Ask ask) {
    final action = ask.actions
        .where((action) => action.kind == model.AskActionKind.answer)
        .firstOrNull;
    if (action == null || !_complete(ask)) return;
    _lastAction = action;
    widget.onAnswer?.call(
      ask,
      action,
      answers: [for (var i = 0; i < ask.questions.length; i++) _answerAt(i)],
    );
  }

  /// A chosen action: a reply opens its field, a collected answer posts every
  /// question's, and anything else is posted as it is.
  void _choose(model.Ask ask, model.AskAction action) {
    if (ask.kind == model.AskKind.question) {
      switch (action.kind) {
        case model.AskActionKind.reply:
          setState(() {
            _replying = true;
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
    _lastAction = action;
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
              Btn('Cancel', onTap: () => setState(() => _replying = false)),
            ],
          ),
        ],
      ),
    );
  }

  void _sendReply(model.Ask ask, model.AskAction? reply) {
    if (reply == null) return;
    _lastAction = reply;
    widget.onAnswer?.call(ask, reply, reply: _reply.text);
    setState(() => _replying = false);
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
    final action = _lastAction ?? ask.actions.firstOrNull;
    if (action == null) return;
    if (action.kind == model.AskActionKind.answer) {
      // The fields still hold what was typed, so the collected answer is
      // rebuilt rather than posted half-empty.
      _submit(ask);
      return;
    }
    if (action.kind == model.AskActionKind.reply) {
      // A reply carries words, and the field still holds them: posting the
      // action alone would be an empty answer, which the model drops.
      widget.onAnswer?.call(ask, action, reply: _reply.text);
      return;
    }
    widget.onAnswer?.call(ask, action);
  }

  /// An ask's kind in the kit's vocabulary (§5: one card, every ask).
  ///
  /// A work item's four asks are drawn with the kit's existing kinds (F6):
  /// the card's title and body say what is being decided, and four new kinds
  /// would be a design-system change that says nothing more.
  static AskKind _kind(model.AskKind kind) => switch (kind) {
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
