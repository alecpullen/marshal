import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';
import 'fleet_store.dart';
import 'thread_ref.dart';
import 'factory_store.dart';
import 'work_item_model.dart' show asksOf;

/// What an ask is (F2 decision 7). The fleet contract carries an agent's
/// approval, an agent's question and an intake submission; F5 adds a pull
/// request's failed fix; F7 adds the review bot's held draft, which a person
/// posts, sends to the author's agent or discards; F6 adds the four things a
/// work item waits on a person for.
enum AskKind {
  approval,
  question,
  intake,
  pull,
  reviewDraft,

  /// A work item's draft is awaiting approval (F6 decision 5).
  approvalDraft,

  /// A worker asked a question that stops the item.
  decision,

  /// The work stalled or ran out of rounds, and a person decides how it goes
  /// on.
  recovery,

  /// The work reached its pull request, and a person says whether it is done.
  completion,
}

/// Where an answer has got to. A posted answer stays sending until the
/// stream stops reporting the ask: a sent message is not shown as done work
/// (design-system.md §7).
enum AskState { waiting, sending, failed }

/// What choosing an action does, which is how its answer is built.
enum AskActionKind {
  /// Answers one question with the option's own wire value.
  option,

  /// Answers one question with what the person typed.
  reply,

  /// Posts the answer collected for every question of a multi-question ask.
  answer,

  /// Declines the whole request. The agent reads `declined` as the sentinel
  /// answer for each of its questions, so every question is declined.
  decline,

  /// Opens the pull request the ask names. It posts nothing: the screen
  /// navigates, because a place is not something a model holds.
  open,

  /// Asks a worker again for the same failing check.
  retry,

  /// Closes the ask here. The bridge has no dismissal route, so the ask
  /// comes back when the bridge reports a new one.
  dismiss,
}

/// One answer an ask offers. The first is the primary one (§5: at most one
/// primary in a group, and it comes first).
@immutable
class AskAction {
  const AskAction(
    this.label, {
    this.primary = false,
    this.kind = AskActionKind.option,
  });

  final String label;
  final bool primary;
  final AskActionKind kind;

  @override
  bool operator ==(Object other) =>
      other is AskAction &&
      other.label == label &&
      other.primary == primary &&
      other.kind == kind;

  @override
  int get hashCode => Object.hash(label, primary, kind);

  @override
  String toString() => 'AskAction("$label"${primary ? ', primary' : ''})';
}

/// One option a question offers, as the request carries it: the label a
/// person reads, and the value the agent reads. They are the same when the
/// payload names no value.
@immutable
class AskOption {
  const AskOption(this.label, {String? value}) : value = value ?? label;

  final String label;
  final String value;

  @override
  String toString() => 'AskOption("$label" = "$value")';
}

/// One question of a request, projected from its payload.
@immutable
class AskQuestion {
  const AskQuestion({
    required this.text,
    this.options = const [],
    this.multi = false,
  });

  /// What is asked.
  final String text;

  /// The answers offered, in the request's order. Empty for a free-text
  /// question.
  final List<AskOption> options;

  /// The answer names several options, joined with commas as the TUI records
  /// them.
  final bool multi;

  @override
  String toString() =>
      'AskQuestion("$text", ${options.length} options'
      '${multi ? ', multi' : ''})';
}

/// Something waiting on the user, projected from what the fleet reports.
@immutable
class Ask {
  const Ask({
    required this.host,
    required this.id,
    required this.kind,
    required this.thread,
    required this.title,
    required this.body,
    required this.actions,
    required this.since,
    this.step,
    this.questions = const [],
    this.pull,
    this.state = AskState.waiting,
    this.error,
  });

  /// The host whose fleet reported it.
  final String host;

  /// The id an answer is posted to: the tool call, the question, or the
  /// submission.
  final String id;

  final AskKind kind;

  /// The thread it belongs to; null for an intake, which has no agent yet.
  final ThreadRef? thread;

  /// What is being asked: a command for an approval, the question itself, a
  /// submission's title.
  final String title;

  /// The context an answer needs, in one line. Empty when there is none.
  final String body;

  final List<AskAction> actions;

  /// The step that raised it, when the request names one: the transcript
  /// pins the card there (F3 decision 9).
  final String? step;

  /// The questions a question ask carries, in the request's order; empty for
  /// an approval or an intake.
  ///
  /// The agent requires exactly one answer per question, in this order, and
  /// treats anything else as declined (`internal/acp/questions.go`), so a
  /// card for a request with more than one must collect all of them.
  final List<AskQuestion> questions;

  /// When it was asked.
  final DateTime since;

  /// The pull request's failed fix, when this is one (F5 decision 6): which
  /// check, what became of it, what was tried, what it found and what the
  /// person can do. Null for every other kind of ask.
  final PullAsk? pull;

  final AskState state;

  /// Why the last answer failed, in the bridge's words.
  final String? error;

  Ask withState(AskState state, {String? error}) => Ask(
    host: host,
    id: id,
    kind: kind,
    thread: thread,
    title: title,
    body: body,
    actions: actions,
    since: since,
    step: step,
    questions: questions,
    pull: pull,
    state: state,
    error: error,
  );

  @override
  String toString() => 'Ask($host/$id, ${kind.name}, ${state.name})';
}

/// The fleet's asks as cards a person can answer (F2 decision 7).
///
/// It projects the store's needs-you queue and each agent's pending request
/// into [Ask]s, oldest first, and posts an answer through the bridge. It
/// writes no fleet state: the store is still the only writer, and an ask
/// leaves when the stream stops reporting it.
class InboxModel extends ChangeNotifier {
  InboxModel(this._store, {required this.client, this.factory}) {
    _store.addListener(_rebuild);
    factory?.addListener(_rebuild);
    _rebuild();
  }

  final FleetStore _store;

  /// The work items, when the app holds them: their asks join the queue, so a
  /// draft or a decision is never waiting with nothing showing it (F6
  /// decision 5). Null leaves the inbox to the fleet alone.
  final FactoryStore? factory;

  /// The client that reaches a host, for the answer an ask posts.
  final BridgeClient Function(String host) client;
  final _overlays = <String, ({AskState state, String? error})>{};

  /// The review bot's held drafts, by host, as `showHeldDrafts` last reported
  /// them.
  final _drafts = <String, List<ReviewDraft>>{};

  /// The pull request asks this app closed, by host, pull and the ask's own
  /// time. The bridge has no dismissal route (F5 plan 1), so a dismissal is
  /// the app's: the ask stays gone until the bridge reports a new one, which
  /// carries a new time.
  final _dismissals = <String>{};
  List<Ask> _asks = const [];

  /// Every ask, oldest first.
  List<Ask> get asks => _asks;

  /// The review bot's held drafts, per host, as the Bots section reports
  /// them: each is an ask of kind [AskKind.reviewDraft] until it is posted,
  /// sent to the author's agent or discarded (F7 decision 4).
  ///
  /// A draft is not fleet state — it lives in the bridge's own store — so the
  /// caller hands it over rather than the store reporting it.
  void showHeldDrafts(String host, List<ReviewDraft> drafts) {
    _drafts[host] = List.unmodifiable(drafts);
    _rebuild();
  }

  /// Posts [action] for [ask], with [reply] when the answer is free text and
  /// [answers] when a multi-question ask collected one per question.
  ///
  /// The ask shows as sending at once; a failure keeps it with the bridge's
  /// own words and a retry. Nothing is removed here: only the stream saying
  /// the ask is gone removes it.
  Future<void> answer(
    Ask ask,
    AskAction action, {
    String? reply,
    List<String>? answers,
  }) async {
    final key = _key(ask);
    if (ask.state == AskState.sending) return;
    switch (ask.kind) {
      case AskKind.approvalDraft:
      case AskKind.decision:
      case AskKind.recovery:
      case AskKind.completion:
        // A work item's ask is answered in its own thread, where the action
        // carries the item's version (F6 decision 5): the list only shows it,
        // and opening it is the screen's.
        return;
      case AskKind.approval:
      case AskKind.question:
      case AskKind.intake:
      case AskKind.reviewDraft:
      case AskKind.pull:
        break;
    }
    if (ask.kind == AskKind.pull) {
      switch (action.kind) {
        case AskActionKind.open:
          // Opening a pull request is the screen's, not this model's: a
          // place is not something a model holds.
          return;
        case AskActionKind.dismiss:
          final pull = ask.pull!;
          _dismissals.add(_pullKey(ask.host, pull));
          _rebuild();
          return;
        case AskActionKind.retry:
          break;
        case AskActionKind.option:
        case AskActionKind.reply:
        case AskActionKind.answer:
        case AskActionKind.decline:
          return;
      }
    }
    // A question's body is built here, before the card is marked as sending:
    // an answer with nothing in it is not worth a round trip, and a partial
    // one is worse than none — the agent reads it as every question declined
    // (internal/acp/questions.go).
    final Answers? body = ask.kind == AskKind.question
        ? _answersToPost(ask, action, reply: reply, answers: answers)
        : null;
    if (ask.kind == AskKind.question && body == null) return;
    _overlays[key] = (state: AskState.sending, error: null);
    _reflow();
    try {
      await _post(ask, action, body);
    } on BridgeFailure catch (failure) {
      _overlays[key] = (
        state: AskState.failed,
        error: describeFailure(failure),
      );
      _reflow();
      return;
    } catch (error) {
      _overlays[key] = (state: AskState.failed, error: error.toString());
      _reflow();
      return;
    }
    // Posted: it stays sending until the store says it is gone.
  }

  /// The body a question's answer posts: exactly one [Answer] per question,
  /// in the request's order, as the agent requires.
  ///
  /// Null when there is nothing to send: a reply with no words, or a
  /// collected answer that does not cover every question. Posting a short
  /// list is what the agent reads as a decline, so it is never posted.
  Answers? _answersToPost(
    Ask ask,
    AskAction action, {
    String? reply,
    List<String>? answers,
  }) {
    switch (action.kind) {
      case AskActionKind.decline:
        return const Answers(declined: true);
      case AskActionKind.open:
      case AskActionKind.retry:
      case AskActionKind.dismiss:
        // A pull request's ask offers these; a question's does not.
        return null;
      case AskActionKind.answer:
        final questions = ask.questions;
        if (answers == null || answers.length != questions.length) {
          return null;
        }
        final words = [
          for (final (index, answer) in answers.indexed)
            _wireValue(questions[index], answer),
        ];
        if (words.any((word) => word.isEmpty)) return null;
        return Answers(
          answers: [
            for (final (index, question) in questions.indexed)
              Answer(question: question.text, answer: words[index]),
          ],
        );
      case AskActionKind.option:
      case AskActionKind.reply:
        final value = action.kind == AskActionKind.reply
            ? (reply ?? '').trim()
            : _optionValue(ask, action.label) ?? action.label;
        if (value.isEmpty) return null;
        // A question carries its own text; a request this build could not
        // read falls back to the ask's title, so the answer still names it.
        final text = ask.questions.isEmpty
            ? ask.title
            : ask.questions.first.text;
        return Answers(
          answers: [Answer(question: text, answer: value)],
        );
    }
  }

  Future<void> _post(Ask ask, AskAction action, Answers? body) async {
    final client = this.client(ask.host);
    switch (ask.kind) {
      case AskKind.approval:
        await client.answerPermission(
          ask.id,
          Decision(approved: action.label != _deny),
        );
      case AskKind.question:
        await client.answerQuestion(ask.id, body!);
      case AskKind.intake:
        if (action.label == _decline) {
          await client.denyIntake(ask.id);
        } else {
          await client.approveIntake(ask.id);
        }
      case AskKind.reviewDraft:
        switch (action.label) {
          case _postLabel:
            await client.postReviewDraft(ask.id);
          case _sendLabel:
            await client.sendDraftToAuthor(ask.id);
          case _discardLabel:
            await client.discardReviewDraft(ask.id);
        }
      case AskKind.pull:
        // A pull request's ask was answered in [answer]: open and dismiss post
        // nothing, so only a retry reaches here.
        await client.fixPull(
          ask.id,
          PullFixRequest(check: ask.pull?.check ?? ''),
        );
      case AskKind.approvalDraft:
      case AskKind.decision:
      case AskKind.recovery:
      case AskKind.completion:
        // Answered in the item's thread; nothing is posted from the list.
        return;
    }
  }

  /// The wire value of the option labelled [label], when the request carries
  /// one: a `{label, value}` pair may name them differently, and it is the
  /// value the agent reads.
  static String? _optionValue(Ask ask, String label) {
    if (ask.questions.isEmpty) return null;
    return _labelValue(ask.questions.first, label);
  }

  /// One collected answer, as the agent reads it: a typed option label
  /// becomes that option's wire value, and free text is kept as typed.
  ///
  /// A question that accepts several is answered with commas, as the TUI
  /// records it (internal/app/tui/question.go), so each part is mapped.
  static String _wireValue(AskQuestion question, String answer) {
    final text = answer.trim();
    if (question.options.isEmpty) return text;
    if (!question.multi) return _labelValue(question, text) ?? text;
    return [
      for (final part in text.split(','))
        if (part.trim().isNotEmpty)
          _labelValue(question, part.trim()) ?? part.trim(),
    ].join(', ');
  }

  /// The wire value of the option [question] offers under [label].
  static String? _labelValue(AskQuestion question, String label) {
    for (final option in question.options) {
      if (option.label == label) return option.value;
    }
    return null;
  }

  String _key(Ask ask) => '${ask.host}|${ask.id}';

  void _reflow() {
    _asks = List.unmodifiable([for (final ask in _asks) _withOverlay(ask)]);
    notifyListeners();
  }

  Ask _withOverlay(Ask ask) {
    final overlay = _overlays[_key(ask)];
    return overlay == null
        ? ask
        : ask.withState(overlay.state, error: overlay.error);
  }

  /// Projects what the store holds into asks: the queue's entries, and any
  /// request an agent is parked on that the queue has not caught up with, so
  /// a person is never left with work waiting that nothing shows.
  void _rebuild() {
    if (_disposed) return;
    final asks = <Ask>[];
    final seen = <String>{};
    for (final host in _store.hosts) {
      final agents = {
        for (final agent in _store.agents(host: host.name)) agent.id: agent,
      };
      for (final item in _store.inbox(host: host.name)) {
        final ask = _askForItem(host.name, item, agents);
        if (ask == null) continue;
        if (seen.add(_key(ask))) asks.add(_withOverlay(ask));
      }
      for (final agent in agents.values) {
        final pending = agent.pending;
        if (pending == null) continue;
        final ask = _askForPending(host.name, agent, pending, null);
        if (seen.add(_key(ask))) asks.add(_withOverlay(ask));
      }
      for (final draft in _drafts[host.name] ?? const <ReviewDraft>[]) {
        final ask = _askForDraft(host.name, draft);
        if (seen.add(_key(ask))) asks.add(_withOverlay(ask));
      }
      for (final item
          in factory?.items(host: host.name) ?? const <FactoryItem>[]) {
        for (final ask in asksOf(host.name, item)) {
          if (seen.add(_key(ask))) asks.add(_withOverlay(ask));
        }
      }
    }
    asks.sort((a, b) {
      final byTime = a.since.compareTo(b.since);
      return byTime != 0 ? byTime : a.id.compareTo(b.id);
    });
    _asks = List.unmodifiable(asks);
    _overlays.removeWhere((key, _) => !seen.contains(key));
    notifyListeners();
  }

  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    _store.removeListener(_rebuild);
    factory?.removeListener(_rebuild);
    super.dispose();
  }

  /// One queue entry as an ask, or null when it names something this build
  /// cannot answer — an agent that is not in the fleet, or an item kind it
  /// does not know.
  Ask? _askForItem(
    String host,
    InboxItem item,
    Map<String, AgentStatus> agents,
  ) {
    switch (item.kind) {
      case InboxItemKind.inboxItemAgent:
        final agent = agents[item.agentID];
        final pending = agent?.pending;
        if (agent == null || pending == null) return null;
        return _askForPending(host, agent, pending, _time(item.at));
      case InboxItemKind.inboxItemIntake:
        final submission = item.submission;
        if (submission == null) return null;
        return _askForIntake(host, submission, _time(item.at));
      case InboxItemKind.inboxItemPull:
        final pull = item.pull;
        if (pull == null) return null;
        if (_dismissals.contains(_pullKey(host, pull))) return null;
        return _askForPull(host, pull, _time(item.at));
      case InboxItemKind.unknown:
        return null;
    }
  }

  /// Where a pull request ask sits in [_dismissals]: the host, the record and
  /// the time the bridge wrote the ask at. A new ask about the same pull
  /// request carries a new time, so it shows again.
  static String _pullKey(String host, PullAsk pull) =>
      '$host|${pull.pull}|${pull.at ?? 0}';

  /// One pull request's failed fix as an ask (F5 decision 6): the pull
  /// request and the check, what the worker tried, what it found, and the
  /// three things a person can do with it.
  Ask _askForPull(String host, PullAsk pull, DateTime since) {
    final number = pull.pull.split('#').last;
    final what = _whatHappened(pull.outcome);
    return Ask(
      host: host,
      id: pull.pull,
      kind: AskKind.pull,
      thread: null,
      title: '#$number · $what',
      body: _pullBody(pull),
      actions: _pullActions,
      since: since,
      pull: pull,
    );
  }

  /// What became of a fix, in the design system's words (§8: say what
  /// happened, not what the system is).
  static String _whatHappened(String outcome) => switch (outcome) {
    'not_reproduced' => 'the worker did not reproduce it',
    'gave_up' => 'the worker gave up',
    'forbidden_change' => 'the fix was refused as a forbidden change',
    'stopped' => 'the fix was stopped',
    'head_moved' => 'the head moved before the worker started',
    'investigated' => 'the worker investigated it',
    _ => 'the fix did not finish',
  };

  /// The context a pull request ask needs: the check, what was tried, what it
  /// found, and what the person can do next.
  static String _pullBody(PullAsk pull) => [
    if (pull.check.trim().isNotEmpty) pull.check.trim(),
    if ((pull.tried ?? '').trim().isNotEmpty) pull.tried!.trim(),
    if ((pull.finding ?? '').trim().isNotEmpty) pull.finding!.trim(),
    if ((pull.action ?? '').trim().isNotEmpty) pull.action!.trim(),
  ].join(' · ');

  static const _open = 'Open';
  static const _retry = 'Try again';
  static const _dismissPull = 'Dismiss';

  /// A failed fix offers the three things worth doing with it: open the pull
  /// request, ask a worker again, or close the ask.
  static const _pullActions = [
    AskAction(_open, primary: true, kind: AskActionKind.open),
    AskAction(_retry, kind: AskActionKind.retry),
    AskAction(_dismissPull, kind: AskActionKind.dismiss),
  ];

  /// One agent's parked request as an ask.
  Ask _askForPending(
    String host,
    AgentStatus agent,
    PendingRequest pending,
    DateTime? since,
  ) {
    final approval = pending.kind != PendingKind.pendingQuestion;
    final questions = approval ? const <AskQuestion>[] : _questionsOf(pending);
    return Ask(
      host: host,
      id: pending.id,
      kind: approval ? AskKind.approval : AskKind.question,
      thread: threadFor(host, agent),
      title: describePending(pending),
      body: approval ? _approvalBody(pending) : _questionBody(questions),
      actions: approval ? _approvalActions : _questionActions(questions),
      questions: questions,
      since: since ?? lastActiveOf(agent),
      step: _stepOf(pending),
    );
  }

  /// The questions a request carries, in its own order.
  ///
  /// The payload's shape is `{question, options, multi}`, where an option is
  /// a plain string or a `{label, value}` pair
  /// (`internal/app/session/pending.go`). A question this build cannot read
  /// is dropped rather than guessed at.
  static List<AskQuestion> _questionsOf(PendingRequest pending) {
    final params = pending.params;
    if (params is! Map) return const [];
    final questions = params['questions'];
    if (questions is! List) return const [];
    final projected = <AskQuestion>[];
    for (final question in questions) {
      if (question is! Map) continue;
      final text = question['question'];
      if (text is! String || text.trim().isEmpty) continue;
      final options = <AskOption>[];
      final raw = question['options'];
      if (raw is List) {
        for (final option in raw) {
          if (option is String && option.trim().isNotEmpty) {
            options.add(AskOption(option.trim()));
          } else if (option is Map) {
            final label = option['label'] ?? option['value'];
            if (label is! String || label.trim().isEmpty) continue;
            final value = option['value'];
            options.add(
              AskOption(
                label.trim(),
                value: value is String && value.trim().isNotEmpty
                    ? value.trim()
                    : null,
              ),
            );
          }
        }
      }
      projected.add(
        AskQuestion(
          text: text.trim(),
          options: List.unmodifiable(options),
          multi: question['multi'] == true,
        ),
      );
    }
    return List.unmodifiable(projected);
  }

  /// One submission as an ask. A submission's title comes from the calling
  /// agent and is therefore text, never markup (web/bridge/intake.go).
  Ask _askForIntake(String host, PendingSubmission submission, DateTime since) {
    final ask = Ask(
      host: host,
      id: submission.id,
      kind: AskKind.intake,
      thread: null,
      title: submission.title,
      body: '${submission.repoID} · ${_submissionSummary(submission)}',
      actions: _intakeActions,
      since: since,
    );
    return ask;
  }

  /// The step a pending request names, when it names one.
  static String? _stepOf(PendingRequest pending) {
    final params = pending.params;
    if (params is! Map) return null;
    final step = params['stepId'];
    if (step == null) return null;
    return '$step';
  }

  /// The context an approval needs: why the agent asks, when it said.
  static String _approvalBody(PendingRequest pending) {
    final params = pending.params;
    if (params is! Map) return '';
    final reason = params['reason'];
    if (reason is String && reason.trim().isNotEmpty) return reason.trim();
    final tool = params['toolName'];
    if (tool is String && tool.trim().isNotEmpty) return tool.trim();
    return '';
  }

  /// The context a question needs: how many more it asks, when it asks more
  /// than one.
  static String _questionBody(List<AskQuestion> questions) {
    if (questions.length < 2) return '';
    final more = questions.length - 1;
    return more == 1 ? '1 more question' : '$more more questions';
  }

  /// A plan is summarised by its task count, so the operator can judge the
  /// scale before opening it (web/ui/src/lib/pending.ts).
  static String _submissionSummary(PendingSubmission submission) {
    final plan = submission.plan;
    if (plan.isNotEmpty) {
      final tasks = RegExp(
        r'^#{2,3} Task \d+',
        multiLine: true,
      ).allMatches(plan).length;
      return 'plan · $tasks ${tasks == 1 ? 'task' : 'tasks'}';
    }
    return 'prompt';
  }

  static DateTime _time(String at) =>
      DateTime.tryParse(at) ??
      DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);

  /// An approval offers the decision the bridge can carry: allow this one
  /// action, or deny it.
  ///
  /// Approving "always, with the derived pattern" is not offered: the
  /// permission route carries `approved`, `edited` and a typed capability
  /// choice and nothing else (web/bridge/contract/answers.go), so an action
  /// labelled for a standing grant would report a one-off approval. It is a
  /// wire change first.
  static const _approvalActions = [
    AskAction('Approve once', primary: true),
    AskAction('Deny'),
  ];

  static const _deny = 'Deny';
  static const _reply = 'Reply';
  static const _decline = 'Decline';
  static const _postLabel = 'Post';
  static const _sendLabel = 'Send to the author’s agent';
  static const _discardLabel = 'Discard';

  /// A held review offers the three things a person does with it: post it,
  /// hand its findings to the agent that owns the pull request, or discard it
  /// (F7 decision 4). Editing it is the Bots section's, where its findings
  /// are.
  static const _draftActions = [
    AskAction(_postLabel, primary: true),
    AskAction(_sendLabel),
    AskAction(_discardLabel),
  ];

  /// One held review as an ask: its title, the pull request it belongs to,
  /// and how many findings it holds.
  Ask _askForDraft(String host, ReviewDraft draft) {
    final findings = draft.findings.length;
    return Ask(
      host: host,
      id: draft.id,
      kind: AskKind.reviewDraft,
      thread: null,
      title: draft.title ?? 'Pull request #${draft.number}',
      body:
          '${draft.repoID} #${draft.number} · $findings '
          '${findings == 1 ? 'finding' : 'findings'}',
      actions: _draftActions,
      since: _time(draft.createdAt),
    );
  }

  static const _answer = 'Answer';

  /// A question with one answer offers its own options, then a free reply:
  /// choosing an option is the answer.
  ///
  /// A request that asks more than one question cannot answer on a single
  /// tap — the agent wants one [Answer] per question, in order, and reads
  /// anything else as a decline — so it offers one action that posts the
  /// answer collected for each of them, and a decline that says so outright.
  static List<AskAction> _questionActions(List<AskQuestion> questions) {
    if (questions.length > 1) {
      return const [
        AskAction(_answer, primary: true, kind: AskActionKind.answer),
        AskAction(_decline, kind: AskActionKind.decline),
      ];
    }
    final options = questions.isEmpty
        ? const <AskOption>[]
        : questions.first.options;
    return [
      for (final (index, option) in options.indexed)
        AskAction(option.label, primary: index == 0),
      const AskAction(_reply, kind: AskActionKind.reply),
    ];
  }

  /// An intake offers to start the work or to refuse it.
  ///
  /// Editing the submission first is not offered: the approve route takes no
  /// body (web/bridge/http.go), so there is nothing to send an edit through.
  static const _intakeActions = [
    AskAction('Start', primary: true),
    AskAction(_decline),
  ];
}
