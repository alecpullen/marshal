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
    StackStore? live,
    DateTime Function()? now,
  }) : _live = live,
       _now = now ?? DateTime.now;

  /// A model holding what a caller gives it, for a test or the gallery. It
  /// reads nothing and ships nothing.
  ChangesModel.seeded({
    GateResult? gate,
    DateTime? gateRanAt,
    List<DiffFile> files = const [],
    String diff = '',
    List<StepDiff> steps = const [],
    CommitDraft? draft,
    List<ReviewComment> comments = const [],
    StackStore? live,
  }) : _client = null,
       agentId = '',
       sessionId = null,
       _live = live,
       _now = DateTime.now,
       _status = ChangesStatus.ready {
    this.gate = gate;
    this.gateRanAt = gateRanAt;
    this.files = files;
    this.diff = diff;
    this.steps = steps;
    this.draft = draft;
    this.comments = comments;
  }

  final BridgeClient? _client;
  final StackStore? _live;

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
    final live = _live;
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
