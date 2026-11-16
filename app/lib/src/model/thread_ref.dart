import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';

/// The three kinds of thread (design-system.md §6), told apart by icon and
/// tooltip, never by colour. F2 lists every agent as an agent thread; work
/// items and plan runs join the tree in F6 (F2 decision 5).
enum ThreadKind { agent, workItem, planRun }

/// What a thread is doing (design-system.md §6), as the sidebar and the
/// overview show it.
enum ThreadStatus { needsYou, running, ready, failed, idle }

/// One thread as the navigation, the overview and the inbox name it: where it
/// lives, what it is called, and what it is doing.
@immutable
class ThreadRef {
  const ThreadRef({
    required this.host,
    required this.id,
    required this.project,
    required this.title,
    required this.kind,
    required this.status,
    required this.headline,
    required this.lastActive,
  });

  /// The host whose fleet reported it.
  final String host;

  /// The agent's id, which is the id its asks and its deltas carry.
  final String id;

  /// The project root it works in.
  final String project;

  /// What a list calls it: the agent's name, or its id when it has none.
  final String title;

  final ThreadKind kind;
  final ThreadStatus status;

  /// One line about what it is doing now, for a tile or a tooltip. Empty when
  /// there is nothing to say beyond the status.
  final String headline;

  /// When it was last active: the agent's own last update.
  final DateTime lastActive;

  @override
  bool operator ==(Object other) =>
      other is ThreadRef &&
      other.host == host &&
      other.id == id &&
      other.project == project &&
      other.title == title &&
      other.kind == kind &&
      other.status == status &&
      other.headline == headline &&
      other.lastActive == lastActive;

  @override
  int get hashCode =>
      Object.hash(host, id, project, title, kind, status, headline, lastActive);

  @override
  String toString() => 'ThreadRef($host/$id, ${status.name})';
}

/// What a thread's status is, from what its agent reports (F2 Decisions):
/// needs you when it has a pending request; failed when its state is an
/// error state; ready when it finished with changes it has not shipped;
/// running when a turn is in flight; idle otherwise.
///
/// A pending request wins over everything: an agent parked on a question is
/// waiting for a person whatever else its state says. An agent whose state
/// this build does not know ranks last, as idle does.
ThreadStatus statusOfAgent(AgentStatus agent) {
  if (agent.pending != null) return ThreadStatus.needsYou;
  switch (agent.status) {
    case AgentState.agentAwaitingApproval:
    case AgentState.agentAwaitingQuestion:
      // Parked, even if the request's payload has not reached us: the state
      // alone is enough to know work waits on a person.
      return ThreadStatus.needsYou;
    case AgentState.agentError:
      return ThreadStatus.failed;
    case AgentState.agentRunning:
      return ThreadStatus.running;
    case AgentState.agentIdle:
      return _idleStatus(agent);
    case AgentState.unknown:
      return ThreadStatus.idle;
  }
}

/// An idle agent that finished with changes still on its branch is ready to
/// ship; one that has pushed a pull request, or has nothing to ship, is idle.
/// It is the bridge's own rule for its `ready` group (web/bridge/inbox.go).
ThreadStatus _idleStatus(AgentStatus agent) {
  final changed = agent.changedFiles ?? 0;
  final shipped = (agent.prUrl ?? '').isNotEmpty;
  return changed > 0 && !shipped ? ThreadStatus.ready : ThreadStatus.idle;
}

/// The thread one agent is, on [host].
ThreadRef threadFor(String host, AgentStatus agent) {
  final name = agent.name?.trim() ?? '';
  return ThreadRef(
    host: host,
    id: agent.id,
    project: agent.project,
    title: name.isEmpty ? agent.id : name,
    kind: ThreadKind.agent,
    status: statusOfAgent(agent),
    headline: headlineFor(agent),
    lastActive: lastActiveOf(agent),
  );
}

/// When an agent last did anything.
///
/// An update time this build cannot read is the oldest possible, so the
/// thread lands with the earlier work rather than claiming to be current.
DateTime lastActiveOf(AgentStatus agent) =>
    DateTime.tryParse(agent.updatedAt) ??
    DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);

/// One line about what an agent is doing: what it waits on, else what it was
/// last doing, else what it has changed. Empty when there is nothing to say.
String headlineFor(AgentStatus agent) {
  final pending = agent.pending;
  if (pending != null) return describePending(pending);
  final activity = agent.activity?.trim() ?? '';
  if (activity.isNotEmpty) return activity;
  final changed = agent.changedFiles ?? 0;
  if (changed > 0) {
    return changed == 1 ? '1 file changed' : '$changed files changed';
  }
  return '';
}

/// A pending request in one line, for a thread's headline and an ask's title.
///
/// The request's `params` is the agent's own ACP payload, whose shape varies
/// by tool, so every field is read defensively and a plain label is the
/// floor: this must never throw, or one odd payload would blank the list.
String describePending(PendingRequest pending) {
  final approval = pending.kind != PendingKind.pendingQuestion;
  final fallback = approval ? 'Approval required' : 'Answer required';
  final params = pending.params;
  if (params is! Map) return fallback;

  if (!approval) {
    final questions = params['questions'];
    if (questions is List && questions.isNotEmpty) {
      final first = questions.first;
      if (first is Map) {
        final text = first['question'];
        if (text is String && text.trim().isNotEmpty) return text.trim();
      }
    }
    return fallback;
  }

  // An approval is about a command: name it when the request carries one.
  for (final key in const ['command', 'toolName']) {
    final value = params[key];
    if (value is String && value.trim().isNotEmpty) return value.trim();
  }
  return fallback;
}
