import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'age.dart';
import 'stack_store.dart';

/// What a [ChangesModel] has read: nothing yet, what the agent changed, or a
/// read that failed (§7).
enum ChangesStatus { loading, ready, failed }

/// How a ship attempt ended (f4-inspector.md decision 7): five results, each
/// with what to do next. A gate or a forge that refused is a result, not an
/// error: nothing was pushed and the message says why.
enum ShipOutcome { pullRequest, pushed, gateRefused, forgeRefused, patch }

/// One ship attempt's result.
@immutable
class ShipResult {
  const ShipResult(this.outcome, {this.url, this.message = ''});

  final ShipOutcome outcome;

  /// The pull request or branch the forge named, when the outcome has one.
  final String? url;

  /// What happened, in the bridge's words.
  final String message;
}

/// One group of the file list: the whole change set, or one step's part of
/// it when the list is grouped by step (decision 5).
@immutable
class ChangeGroup {
  const ChangeGroup({
    required this.label,
    required this.diff,
    required this.files,
  });

  /// What the group is called: a step's headline. Empty for the whole
  /// change set.
  final String label;

  /// The group's unified diff.
  final String diff;

  /// The paths it changed, in the agent's order.
  final List<String> files;
}

/// What one of the gate's checks came to. The model's own vocabulary, so a
/// tab reads it without reaching for the bridge's contract (ui/ never touches
/// client/).
enum GateState { notRun, skipped, passed, failed }

/// One of the gate's checks: its name, what became of it, and one line of
/// detail.
@immutable
class GateCheckView {
  const GateCheckView(this.name, this.state, {this.detail = ''});

  final String name;
  final GateState state;
  final String detail;
}

/// One review comment as a tab draws it: its thread's identity, where it is
/// anchored, what it says, and whether it is still open.
@immutable
class CommentView {
  const CommentView({
    required this.id,
    required this.path,
    required this.line,
    required this.side,
    required this.quote,
    required this.body,
    required this.sent,
    required this.resolved,
  });

  final String id;
  final String path;
  final int line;

  /// `old` or `new`: the side of the diff the comment is on.
  final String side;

  /// The diff lines the comment is anchored to.
  final String quote;

  final String body;

  /// Whether the bridge has delivered it to the agent.
  final bool sent;

  /// Whether the thread is closed.
  final bool resolved;
}

/// An agent's changes (f4-inspector.md decision 5): the gate, the files with
/// their counts, the diff, the marks a person put on files they have read,
/// the review comments, and the way to ship it all.
///
/// It is a [ChangeNotifier] and imports nothing from Flutter beyond
/// `foundation.dart`, so its rules are plain unit tests (model/README.md).
/// The tabs draw it; it never draws.
class ChangesModel extends ChangeNotifier {
  ChangesModel(
    this._client, {
    required this.agentId,
    this.sessionId,
    this.live,
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;

  /// A model holding what a caller gives it, for a test or the gallery. It
  /// reads nothing and ships nothing.
  ChangesModel.seeded({
    this.gate,
    this.gateRanAt,
    this.files = const [],
    this.diff = '',
    this.steps = const [],
    this.draft,
    this.comments = const [],
    this.live,
  }) : _client = null,
       agentId = '',
       sessionId = null,
       _now = DateTime.now,
       _status = ChangesStatus.ready;

  final BridgeClient? _client;

  /// The live transcript this change set follows, when the host gives one:
  /// with nothing selected, the file the agent is editing is the one shown
  /// (decision 4).
  final StackStore? live;

  /// What "ran 2m ago" is measured against.
  final DateTime Function() _now;

  /// The agent whose changes these are.
  final String agentId;

  /// The session whose step diffs these are, when the caller knows it.
  final String? sessionId;

  ChangesStatus _status = ChangesStatus.loading;

  /// Whether the change set has been read.
  ChangesStatus get status => _status;

  /// What failed, in words, when [status] is [ChangesStatus.failed].
  String? failureMessage;

  /// The agent's latest verify result, or null when the gate has not run.
  GateResult? gate;

  /// When the gate last ran.
  DateTime? gateRanAt;

  /// The files the agent changed, with their counts.
  List<DiffFile> files = const [];

  /// The whole change set's unified diff.
  String diff = '';

  /// The session's steps, each with its part of the diff.
  List<StepDiff> steps = const [];

  /// The message the agent would commit.
  CommitDraft? draft;

  /// The review comments on this change set, oldest first.
  List<ReviewComment> comments = const [];

  bool _byStep = false;

  /// Whether the file list is grouped by step.
  bool get byStep => _byStep;

  final Set<String> _viewed = {};

  /// The threads this session closed, for a comment the bridge has not yet
  /// answered about.
  final Set<String> _resolved = {};

  /// The paths a person has marked read. It is client state (decision 5),
  /// held here so switching tabs does not lose it.
  Set<String> get viewed => Set.unmodifiable(_viewed);

  String? _selectedFile;

  /// The path a person chose, or null while the list follows the agent.
  String? get selectedFile => _selectedFile;

  Map<String, String>? _fileDiffs;
  bool _runningGate = false;

  /// Whether a gate run is in flight.
  bool get runningGate => _runningGate;

  /// The file the list is showing: the one a person chose, else the one the
  /// agent is editing, else the first the diff names (decision 4).
  String? get activeFile {
    final chosen = _selectedFile;
    if (chosen != null) return chosen;
    final edited = editedFile;
    if (edited != null && files.any((file) => file.path == edited)) {
      return edited;
    }
    return files.isEmpty ? null : files.first.path;
  }

  /// The file the agent is editing right now, from the live transcript: a
  /// running call's target, or the last target of the live step's call.
  String? get editedFile {
    final live = this.live;
    if (live == null) return null;
    // A call that has not finished is the file being edited right now.
    for (final node in live.nodes) {
      final path = _pathInTarget(node.tool?.running?.target);
      if (path != null) return path;
    }
    // Otherwise it is the live step's last finished call.
    for (final node in live.nodes) {
      if (node.live != true) continue;
      for (final child in node.children ?? const <String>[]) {
        final calls = live.node(child)?.tool?.calls;
        if (calls == null || calls.isEmpty) continue;
        final path = _pathInTarget(calls.last.target);
        if (path != null) return path;
      }
    }
    return null;
  }

  /// The groups the file list draws: one group for everything, or one per
  /// step (decision 5).
  List<ChangeGroup> get groups {
    if (!_byStep) {
      return [
        ChangeGroup(
          label: '',
          diff: diff,
          files: [for (final file in files) file.path],
        ),
      ];
    }
    return [
      for (final step in steps)
        ChangeGroup(label: step.headline, diff: step.diff, files: step.files),
    ];
  }

  /// The path's part of the change set, or the empty string when the diff
  /// does not name it.
  String diffOf(String path) {
    _fileDiffs ??= splitDiffByFile(diff);
    return _fileDiffs![path] ?? '';
  }

  /// How long ago the gate ran, in the product's short words.
  String get gateAge =>
      gateRanAt == null ? '' : relativeAge(gateRanAt!, _now());

  /// The gate's checks, in its order.
  ///
  /// The record carries one outcome — the repository's build and test — so
  /// the gate is one check today, and a gate that has not run says so with a
  /// pending mark rather than an empty list. F5's forge state brings gates
  /// with several.
  List<GateCheckView> get gateChecks {
    final record = gate;
    if (record == null) {
      return const [GateCheckView('The gate has not run', GateState.notRun)];
    }
    if (record.skipped) {
      return const [
        GateCheckView(
          'Gate skipped',
          GateState.skipped,
          detail: 'no verify command',
        ),
      ];
    }
    final failed = record.ok != true;
    final command = record.failedCommand ?? '';
    return [
      GateCheckView(
        failed ? 'Build and test failed' : 'Build and test',
        failed ? GateState.failed : GateState.passed,
        detail: failed ? command : (record.ok == true ? 'passed' : ''),
      ),
    ];
  }

  /// Reads the change set, the gate, the drafted message and the steps.
  Future<void> refresh() async {
    final client = _client;
    if (client == null) return;
    _status = ChangesStatus.loading;
    failureMessage = null;
    notifyListeners();
    try {
      final result = await client.diff(agentId);
      final record = await client.gate(agentId);
      final message = await client.commitDraft(agentId);
      final session = sessionId;
      final steps = session == null
          ? const StepDiffs(steps: [])
          : await client.stepDiffs(session);
      diff = result.diff ?? '';
      _fileDiffs = null;
      files = result.files;
      gate = record?.result;
      gateRanAt = record == null ? null : DateTime.tryParse(record.at);
      draft = message;
      this.steps = steps.steps;
      comments = await client.reviewComments(agentId);
      _status = ChangesStatus.ready;
    } catch (error) {
      _status = ChangesStatus.failed;
      failureMessage = '$error';
    }
    notifyListeners();
  }

  /// Runs the gate now: POST …/verify, then the record it answers with.
  Future<void> runGate() async {
    final client = _client;
    if (client == null) return;
    _runningGate = true;
    failureMessage = null;
    notifyListeners();
    try {
      final record = await client.verify(agentId);
      gate = record.result;
      gateRanAt = DateTime.tryParse(record.at);
      if (_status == ChangesStatus.failed) _status = ChangesStatus.ready;
    } catch (error) {
      failureMessage = '$error';
    } finally {
      _runningGate = false;
      notifyListeners();
    }
  }

  /// Groups the file list by step, or shows it whole.
  void setByStep(bool value) {
    if (_byStep == value) return;
    _byStep = value;
    notifyListeners();
  }

  /// Shows [path]'s diff. Null follows the agent again.
  void select(String? path) {
    if (_selectedFile == path) return;
    _selectedFile = path;
    notifyListeners();
  }

  /// Marks [path] read, or unmarks it.
  void toggleViewed(String path) {
    if (!_viewed.remove(path)) _viewed.add(path);
    notifyListeners();
  }

  /// Whether [path] is marked read.
  bool isViewed(String path) => _viewed.contains(path);

  /// The comments on [path], or on all of them when [line] is null.
  List<CommentView> commentsAt(String path, [int? line]) => [
    for (final comment in commentThreads)
      if (comment.path == path && (line == null || comment.line == line))
        comment,
  ];

  /// Every comment's thread, oldest first.
  List<CommentView> get commentThreads => [
    for (final comment in comments)
      CommentView(
        id: comment.id,
        path: comment.path,
        line: comment.line,
        side: comment.side,
        quote: comment.quote,
        body: comment.body,
        sent: comment.sentAt.isNotEmpty,
        resolved: isResolved(comment),
      ),
  ];

  /// Whether a comment's thread is closed: the bridge said so, or this
  /// session resolved it.
  bool isResolved(ReviewComment comment) =>
      comment.resolvedAt != null || _resolved.contains(comment.id);

  /// Leaves a comment on a line, which the bridge delivers to the agent as
  /// steering (decision 5). The comment it stored joins the list.
  Future<void> comment(
    String path,
    int line,
    String body, {
    String side = 'new',
    String quote = '',
  }) async {
    final client = _client;
    if (client == null) return;
    final stored = await client.addReviewComment(
      agentId,
      ReviewCommentRequest(
        path: path,
        line: line,
        side: side,
        quote: quote,
        body: body,
      ),
    );
    comments = [...comments, stored];
    notifyListeners();
  }

  /// Closes a comment's thread.
  Future<void> resolve(String commentId) async {
    final client = _client;
    if (client == null) return;
    await client.resolveReviewComment(agentId, commentId);
    _resolved.add(commentId);
    notifyListeners();
  }

  /// Ships the work (decision 6): the message, whether to open a pull
  /// request, and — over a gate that failed — the reason for overriding it.
  ///
  /// Every outcome is returned as a result, never thrown: a gate or a forge
  /// that refused pushed nothing, and the result says why (decision 7).
  Future<ShipResult> ship({
    required String message,
    required bool openPr,
    String? overrideReason,
  }) async {
    final client = _client;
    if (client == null) {
      return const ShipResult(
        ShipOutcome.pushed,
        message: 'Nothing was shipped: this is fixture data.',
      );
    }
    final reason = overrideReason?.trim() ?? '';
    try {
      final result = await client.exit(
        agentId,
        ExitRequest(
          commitMessage: message,
          noPR: !openPr,
          override: reason.isEmpty
              ? null
              : GateOverride(
                  reason: reason,
                  at: _now().toUtc().toIso8601String(),
                  by: '',
                ),
        ),
      );
      if (result.blocked == true) {
        final command = result.verify?.failedCommand ?? '';
        return ShipResult(
          ShipOutcome.gateRefused,
          message: command.isEmpty
              ? 'The gate refused to ship this work.'
              : 'The gate refused: $command failed.',
        );
      }
      if (result.destination == 'patch') {
        return const ShipResult(
          ShipOutcome.patch,
          message: 'This source is read-only, so the work leaves as a patch.',
        );
      }
      final url = result.prUrl ?? '';
      if (url.isNotEmpty) {
        return ShipResult(
          ShipOutcome.pullRequest,
          url: url,
          message: result.branch ?? '',
        );
      }
      return ShipResult(ShipOutcome.pushed, message: result.branch ?? '');
    } catch (error) {
      // The only refusal left is the forge's: the gate's is a blocked
      // result, and a source that is read-only is a patch.
      return ShipResult(ShipOutcome.forgeRefused, message: '$error');
    }
  }

  /// Throws the agent's work away. Nothing about the change set survives it.
  Future<void> discard() async {
    // A bridge that refused leaves the answer as it was: the work is still
    // there, and the tab still shows it.
    final client = _client;
    if (client != null) await client.discard(agentId);
    files = const [];
    diff = '';
    _fileDiffs = null;
    steps = const [];
    comments = const [];
    _viewed.clear();
    _resolved.clear();
    _selectedFile = null;
    gate = null;
    gateRanAt = null;
    notifyListeners();
  }

  /// The change set as a patch file's text, for Export patch.
  Future<String> exportPatch() async {
    final client = _client;
    if (client == null) return '';
    return client.patch(agentId);
  }

  /// The path a tool's target names: `file.go:12` is `file.go`.
  static final _targetPath = RegExp(r'^(.+?):\d+(?::\d+)?$');

  static String? _pathInTarget(String? target) {
    final text = target?.trim();
    if (text == null || text.isEmpty) return null;
    final match = _targetPath.firstMatch(text);
    final path = match == null ? text : match.group(1)!;
    return path.isEmpty ? null : path;
  }
}

/// The per-file sections of a unified [diff], keyed by the path after the
/// change: `diff --git a/x b/y` starts y's section.
Map<String, String> splitDiffByFile(String diff) {
  final out = <String, String>{};
  String? current;
  final body = <String>[];
  void flush() {
    final path = current;
    if (path != null) out[path] = body.join('\n');
    body.clear();
  }

  for (final line in diff.split('\n')) {
    if (line.startsWith('diff --git ')) {
      flush();
      current = _pathOfGitHeader(line);
      if (current == null) continue;
    }
    if (current != null) body.add(line);
  }
  flush();
  return out;
}

/// The new-side path of a `diff --git a/x b/y` line.
String? _pathOfGitHeader(String line) {
  final parts = line.split(' ');
  if (parts.length < 4) return null;
  final path = parts.last;
  if (path.startsWith('b/')) return path.substring(2);
  return path.isEmpty ? null : path;
}
