import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';
import 'fleet_store.dart';
import 'thread_ref.dart';

/// What an ask is (F2 decision 7). The fleet contract carries exactly these
/// three in this phase: an agent's approval, an agent's question, and an
/// intake submission. Factory asks join in F6 and review drafts in F7, which
/// is an addition here, not a rewrite.
enum AskKind { approval, question, intake }

/// Where an answer has got to. A posted answer stays sending until the
/// stream stops reporting the ask: a sent message is not shown as done work
/// (design-system.md §7).
enum AskState { waiting, sending, failed }

/// One answer an ask offers. The first is the primary one (§5: at most one
/// primary in a group, and it comes first).
@immutable
class AskAction {
  const AskAction(this.label, {this.primary = false});

  final String label;
  final bool primary;

  @override
  bool operator ==(Object other) =>
      other is AskAction && other.label == label && other.primary == primary;

  @override
  int get hashCode => Object.hash(label, primary);

  @override
  String toString() => 'AskAction("$label"${primary ? ', primary' : ''})';
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

  /// When it was asked.
  final DateTime since;

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
  InboxModel(this._store, {required this.client}) {
    _store.addListener(_rebuild);
    _rebuild();
  }

  final FleetStore _store;

  /// The client that reaches a host, for the answer an ask posts.
  final BridgeClient Function(String host) client;
  final _overlays = <String, ({AskState state, String? error})>{};

  /// The parameters behind each ask, so an answer can name the question it
  /// answers and the option's wire value rather than its label.
  final _params = <String, Object?>{};
  List<Ask> _asks = const [];

  /// Every ask, oldest first.
  List<Ask> get asks => _asks;

  /// Posts [action] for [ask], with [reply] when the answer is free text.
  ///
  /// The ask shows as sending at once; a failure keeps it with the bridge's
  /// own words and a retry. Nothing is removed here: only the stream saying
  /// the ask is gone removes it.
  Future<void> answer(Ask ask, AskAction action, {String? reply}) async {
    final key = _key(ask);
    if (ask.state == AskState.sending) return;
    final answer = reply?.trim() ?? '';
    if (ask.kind == AskKind.question &&
        answer.isEmpty &&
        action.label == _reply) {
      // Nothing to send: a reply with no words is not an answer.
      return;
    }
    _overlays[key] = (state: AskState.sending, error: null);
    _reflow();
    try {
      await _post(ask, action, answer);
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

  Future<void> _post(Ask ask, AskAction action, String reply) async {
    final client = this.client(ask.host);
    switch (ask.kind) {
      case AskKind.approval:
        await client.answerPermission(
          ask.id,
          Decision(approved: action.label != _deny),
        );
      case AskKind.question:
        final question = _questionOf(ask);
        final value = action.label == _reply
            ? reply
            : _optionValue(ask, action.label) ?? action.label;
        await client.answerQuestion(
          ask.id,
          Answers(
            answers: [Answer(question: question, answer: value)],
          ),
        );
      case AskKind.intake:
        if (action.label == _decline) {
          await client.denyIntake(ask.id);
        } else {
          await client.approveIntake(ask.id);
        }
    }
  }

  /// What the question asks, from the request behind the ask.
  String _questionOf(Ask ask) {
    final questions = _questions(ask);
    if (questions.isEmpty) return ask.title;
    final first = questions.first;
    final text = first['question'];
    return text is String && text.trim().isNotEmpty ? text.trim() : ask.title;
  }

  /// The wire value of the option labelled [label], when the request carries
  /// one: a `{label, value}` pair may name them differently, and it is the
  /// value the agent reads.
  String? _optionValue(Ask ask, String label) {
    if (_questions(ask).isEmpty) return null;
    final options = _questions(ask).first['options'];
    if (options is! List) return null;
    for (final option in options) {
      if (option is Map && option['label'] == label) {
        final value = option['value'];
        if (value is String) return value;
      }
    }
    return null;
  }

  /// The first question's map from the request's own payload.
  List<Map<Object?, Object?>> _questions(Ask ask) {
    final params = _params[_key(ask)];
    if (params is! Map) return const [];
    final questions = params['questions'];
    if (questions is! List) return const [];
    return [
      for (final question in questions)
        if (question is Map) question,
    ];
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
    }
    asks.sort((a, b) {
      final byTime = a.since.compareTo(b.since);
      return byTime != 0 ? byTime : a.id.compareTo(b.id);
    });
    _asks = List.unmodifiable(asks);
    _overlays.removeWhere((key, _) => !seen.contains(key));
    _params.removeWhere((key, _) => !seen.contains(key));
    notifyListeners();
  }

  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    _store.removeListener(_rebuild);
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
      case InboxItemKind.unknown:
        return null;
    }
  }

  /// One agent's parked request as an ask.
  Ask _askForPending(
    String host,
    AgentStatus agent,
    PendingRequest pending,
    DateTime? since,
  ) {
    final approval = pending.kind != PendingKind.pendingQuestion;
    final ask = Ask(
      host: host,
      id: pending.id,
      kind: approval ? AskKind.approval : AskKind.question,
      thread: threadFor(host, agent),
      title: describePending(pending),
      body: approval ? _approvalBody(pending) : _questionBody(pending),
      actions: approval ? _approvalActions : _questionActions(pending),
      since: since ?? lastActiveOf(agent),
      step: _stepOf(pending),
    );
    _params[_key(ask)] = pending.params;
    return ask;
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
  static String _questionBody(PendingRequest pending) {
    final params = pending.params;
    if (params is! Map) return '';
    final questions = params['questions'];
    if (questions is! List || questions.length < 2) return '';
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

  /// A question offers its own options, then a free reply.
  static List<AskAction> _questionActions(PendingRequest pending) {
    final params = pending.params;
    final options = <String>[];
    if (params is Map) {
      final questions = params['questions'];
      if (questions is List && questions.isNotEmpty) {
        final first = questions.first;
        if (first is Map && first['options'] is List) {
          for (final option in first['options'] as List) {
            final label = option is Map
                ? option['label'] ?? option['value']
                : option;
            if (label is String && label.trim().isNotEmpty) {
              options.add(label.trim());
            }
          }
        }
      }
    }
    return [
      for (final (index, label) in options.indexed)
        AskAction(label, primary: index == 0),
      const AskAction(_reply),
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
