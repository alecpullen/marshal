import 'dart:convert';

import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import 'fleet_store.dart' show describeFailure;

// The routes are functions and their fields stay private, so the parameters
// cannot be initializing formals: Dart has no private named parameter.
// ignore_for_file: prefer_initializing_formals

/// One line of a planning conversation (F6 decision 8): who said it, and what
/// they said. The planner's own words, a tool it ran, the prompts the person
/// sent and the requests it parked on are all lines of one stream, because the
/// journal is the authority on what happened.
@immutable
class PlannerMessage {
  const PlannerMessage({
    required this.label,
    required this.text,
    this.fromUser = false,
    this.at,
  });

  /// Who said it, in words: "You", "The planner", "Tool", "Permission".
  final String label;

  /// What was said, or what happened.
  final String text;

  /// Whether the person said it, which the transcript draws on the right.
  final bool fromUser;

  /// When the journal recorded it, when it says.
  final DateTime? at;

  @override
  bool operator ==(Object other) =>
      other is PlannerMessage &&
      other.label == label &&
      other.text == text &&
      other.fromUser == fromUser &&
      other.at == at;

  @override
  int get hashCode => Object.hash(label, text, fromUser, at);

  @override
  String toString() => 'PlannerMessage($label: $text)';
}

/// One request the planner is parked on, as a card the person answers once
/// (F6 decision 8). A permission is answered allow or deny; a question takes
/// the person's words.
@immutable
class PlannerRequest {
  const PlannerRequest({
    required this.requestID,
    required this.method,
    required this.permission,
    required this.title,
    required this.detail,
    this.questions = const [],
  });

  /// The id an answer is posted to.
  final String requestID;

  /// The agent method the request arrived as, for the record.
  final String method;

  /// Whether it is a permission request rather than a question.
  final bool permission;

  /// What is being asked: the question itself, or "Permission requested".
  final String title;

  /// The command a permission asks to run. Empty for a question, whose title
  /// is the question.
  final String detail;

  /// Every question the request asks, in the order they must be answered. A
  /// permission asks none.
  final List<String> questions;

  /// The response a permission answer posts: the one shape the agent reads as
  /// a settled permission.
  static Map<String, Object?> permissionAnswer({required bool allow}) => {
    'approved': allow,
  };

  /// The response a typed question answer posts.
  static Map<String, Object?> questionAnswer(String text) => {
    'answers': [
      {'question': '', 'answer': text},
    ],
  };
}

/// The plan as the last snapshot froze it (F6 decision 8): the exact text the
/// person approved and the files they selected, with the paths the capture
/// refused.
@immutable
class PlannerSnapshot {
  const PlannerSnapshot({
    required this.id,
    required this.sha256,
    required this.baseCommit,
    required this.plan,
    required this.spec,
    this.files = const [],
    this.rejected = const [],
  });

  /// The snapshot the bridge froze.
  final String id;

  /// The digest of the capture: the identity a handoff is held to.
  final String sha256;

  /// The commit the capture was taken against. A handoff names it, so the
  /// files it froze are the files of that revision rather than of whatever the
  /// repository holds when the draft is created.
  final String baseCommit;

  /// The plan text, verbatim.
  final String plan;

  /// The specification text, verbatim.
  final String spec;

  /// Every captured file, as the review reports it.
  final List<PlannerSnapshotFile> files;

  /// Every path the capture refused and why. A selected path is either
  /// captured or reported here, never silently dropped.
  final List<PlannerRejection> rejected;

  /// The plan one line, for a title: how many tasks it holds.
  String get summary => planSummary(plan);

  /// Reads a capture's answer as the plan the inspector shows. The plan and
  /// the spec are the artifacts the capture froze, by their kinds; a capture
  /// that froze neither shows no text rather than an invented one.
  factory PlannerSnapshot.of(PlannerSnapshotReply reply) => PlannerSnapshot(
    id: reply.id,
    sha256: reply.sha256,
    baseCommit: reply.baseCommit,
    plan: _artifact(reply.artifacts, 'plan'),
    spec: _artifact(reply.artifacts, 'spec'),
    files: reply.files,
    rejected: reply.rejected,
  );

  static String _artifact(
    List<PlannerSnapshotArtifact> artifacts,
    String kind,
  ) {
    for (final artifact in artifacts) {
      if (artifact.kind == kind) return artifact.content;
    }
    return '';
  }
}

/// How many tasks a plan text holds, and its first line of prose: what a plan
/// is summarised by before it is read.
String planSummary(String plan) {
  final text = plan.trim();
  if (text.isEmpty) return 'No plan text';
  final tasks = RegExp(
    r'^#{2,3} Task \d+',
    multiLine: true,
  ).allMatches(text).length;
  final first = [
    for (final line in text.split('\n'))
      if (line.trim().isNotEmpty && !line.trim().startsWith('#')) line.trim(),
  ].firstOrNull;
  final count = tasks == 0 ? 'plan' : '$tasks ${tasks == 1 ? 'task' : 'tasks'}';
  return first == null ? count : '$count · $first';
}

/// Where a planning session stands, as the bridge last reported it: live,
/// ended, or unknown because no record has been read or the record does not
/// say it is live.
///
/// It is never inferred from the journal: an event is a thing that happened,
/// and a journal that stops moving is not evidence that anything is running
/// (design-system.md §7).
enum PlannerConnection {
  live(
    'Planner live',
    'The bridge reports this session connected, so a turn can run here.',
  ),
  ended(
    'Planner ended',
    'The session has ended. Its conversation, its journal and its plan are '
        'still here to read.',
  ),
  unknown(
    'Planner state unknown',
    'The bridge has not said whether this session is connected, so nothing '
        'here is shown as running.',
  );

  const PlannerConnection(this.label, this.note);

  /// The state in words, for a chip or a line.
  final String label;

  /// What it means for the person, in one sentence.
  final String note;
}

/// One planning thread (F6 decision 8): the planner's journal as a
/// conversation, the requests it is parked on, and the plan the last snapshot
/// froze.
///
/// It reads the journal by sequence, so a screen that comes back to a planner
/// picks up where it stopped rather than replaying the whole record. The
/// routes are functions, so a test drives every one of them with no socket
/// (model/README.md).
class PlanningModel extends ChangeNotifier {
  PlanningModel({
    required this.plannerID,
    required Future<List<PlannerEvent>> Function(int after) events,
    required Future<PlannerTurnReply> Function(PlannerPromptRequest request)
    promptPlanner,
    required Future<void> Function() cancelPlanner,
    required Future<void> Function(String requestID, Object? response)
    answerPlanner,
    required Future<PlannerSnapshotReply> Function(
      PlannerSnapshotRequest request,
    )
    capture,
    Future<PlannerView> Function()? read,
  }) : _events = events,
       _prompt = promptPlanner,
       _cancel = cancelPlanner,
       _answer = answerPlanner,
       _capture = capture,
       _read = read;

  /// The planner this thread is, as the bridge named it.
  final String plannerID;

  final Future<List<PlannerEvent>> Function(int after) _events;
  final Future<PlannerTurnReply> Function(PlannerPromptRequest request) _prompt;
  final Future<void> Function() _cancel;
  final Future<void> Function(String requestID, Object? response) _answer;
  final Future<PlannerSnapshotReply> Function(PlannerSnapshotRequest request)
  _capture;
  final Future<PlannerView> Function()? _read;

  final _messages = <PlannerMessage>[];
  final _requests = <PlannerRequest>[];
  final _settled = <String>{};

  /// The last sequence the journal was read to, so the next read resumes.
  int _sequence = 0;

  /// The plan the last capture froze, or null until one is taken.
  PlannerSnapshot? _latest;
  PlannerView? _view;
  String? _failure;
  bool _running = false;
  bool _loading = false;
  int _requests_ = 0;

  /// The conversation, oldest first.
  List<PlannerMessage> get messages => List.unmodifiable(_messages);

  /// The requests still waiting on a person, in the order they were asked.
  List<PlannerRequest> get requests => List.unmodifiable(_requests);

  /// The plan the last snapshot froze, or null.
  PlannerSnapshot? get latest => _latest;

  /// The planner as the bridge last reported it, for what its session can do.
  PlannerView? get view => _view;

  /// Whether the planner's record has been read. False until a read lands, so
  /// nothing is stated as a fact the bridge has not reported.
  bool get reported => _view != null;

  /// The session's connection, from the record alone: a record that says the
  /// planner ended is ended, one that says it is live is live, and anything
  /// else — no record read yet, or a record that does not claim to be live —
  /// is unknown rather than assumed connected.
  PlannerConnection get connection {
    final view = _view;
    if (view == null) return PlannerConnection.unknown;
    if (view.state == 'ended' || view.endedAt != null) {
      return PlannerConnection.ended;
    }
    return view.live ? PlannerConnection.live : PlannerConnection.unknown;
  }

  /// The mode the person asked for, in the design system's words: the
  /// request, which is not the same thing as the mode in force.
  String? get requestedMode => _modeWordsOrNull(_view?.requestedMode);

  /// The mode in force, in the design system's words, and only one the
  /// bridge confirmed. Null while a request is unanswered, so no screen can
  /// draw a requested mode as the one running.
  String? get confirmedMode {
    final view = _view;
    if (view == null || !view.modeConfirmed) return null;
    // A confirmation that names its mode is that mode; one that confirms only
    // the request confirms the request.
    return _modeWordsOrNull(view.confirmedMode) ??
        _modeWordsOrNull(view.requestedMode);
  }

  /// Whether the bridge confirmed the session's mode.
  bool get modeConfirmed => confirmedMode != null;

  /// The mode in one phrase: the mode in force, else the one requested and
  /// not yet confirmed, else that this build has no mode to state.
  String get modeWords {
    final confirmed = confirmedMode;
    if (confirmed != null) return confirmed;
    final requested = requestedMode;
    if (requested != null) return '$requested requested · not confirmed';
    return 'Mode unknown';
  }

  /// The workspace the planner runs in, as the record named it; null when the
  /// record names none, or when no record has been read.
  String? get workspace {
    final id = _view?.workspaceID ?? '';
    return id.isEmpty ? null : id;
  }

  /// Where the planner runs, in one phrase: its workspace, that the record
  /// names none, or that no record has been read.
  String get workspaceWords {
    final workspace = this.workspace;
    if (workspace != null) return 'Workspace $workspace';
    return _view == null ? 'Workspace unknown' : 'No workspace';
  }

  /// How many requests are waiting on the person: the cards the planner is
  /// parked on, counted.
  int get waiting => _requests.length;

  /// Whether a turn is in flight: the composer's button steers while it is.
  /// A planner the record says has ended is never in flight, whatever its
  /// last turn says, because nothing is running in it any more.
  bool get running => _running && connection != PlannerConnection.ended;

  /// Whether a read is in flight.
  bool get loading => _loading;

  /// Why the last read, prompt or capture failed, in the bridge's words.
  String? get failure => _failure;

  /// Reads the journal from where the last read stopped, and the planner's
  /// record, then rebuilds the conversation and the open requests.
  Future<void> refresh() async {
    if (_loading) return;
    _loading = true;
    notifyListeners();
    try {
      final events = await _events(_sequence);
      final read = _read;
      if (read != null) _view = await read();
      _apply(events);
      _failure = null;
    } catch (error) {
      _failure = describeFailure(error);
    }
    _loading = false;
    notifyListeners();
  }

  /// Sends a prompt. The person's words are shown at once, because the person
  /// said them; nothing else is shown as done until the journal says so
  /// (design-system.md §7).
  Future<void> prompt(String text) async {
    final words = text.trim();
    if (words.isEmpty) return;
    _failure = null;
    _messages.add(PlannerMessage(label: 'You', text: words, fromUser: true));
    _running = true;
    notifyListeners();
    try {
      await _prompt(PlannerPromptRequest(requestID: _requestID(), text: words));
    } catch (error) {
      _running = false;
      _failure = describeFailure(error);
      notifyListeners();
      return;
    }
    await refresh();
  }

  /// Stops the turn in flight.
  Future<void> cancel() async {
    try {
      await _cancel();
      _failure = null;
    } catch (error) {
      _failure = describeFailure(error);
    }
    _running = false;
    notifyListeners();
    await refresh();
  }

  /// Answers one request the planner is parked on. The response is the answer
  /// itself, never a wire envelope the screen assembled.
  Future<void> answer(String requestID, String text) async {
    final request = _requestOf(requestID);
    if (request == null) return;
    final trimmed = text.trim();
    final response = request.permission
        ? PlannerRequest.permissionAnswer(
            allow: trimmed.toLowerCase() == 'allow',
          )
        : PlannerRequest.questionAnswer(trimmed);
    _settled.add(requestID);
    _requests.removeWhere((each) => each.requestID == requestID);
    notifyListeners();
    try {
      await _answer(requestID, response);
    } catch (error) {
      // A refused answer leaves the request where it was: the person can
      // decide again rather than believing it was settled. Nothing is read
      // back here, so the refusal is what the screen says.
      _settled.remove(requestID);
      _requests.insert(0, request);
      _failure = describeFailure(error);
      notifyListeners();
      return;
    }
    _failure = null;
    notifyListeners();
    await refresh();
  }

  /// Captures the plan and the spec for review, and keeps what the bridge
  /// froze. [paths] are the repository-relative files the person selected; an
  /// empty list is a documents-only snapshot.
  Future<PlannerSnapshot?> snapshot({
    String plan = '',
    String spec = '',
    List<String> paths = const [],
  }) async {
    try {
      final reply = await _capture(
        PlannerSnapshotRequest(plan: plan, spec: spec, paths: paths),
      );
      _latest = PlannerSnapshot.of(reply);
      _failure = null;
      notifyListeners();
      return _latest;
    } catch (error) {
      _failure = describeFailure(error);
      notifyListeners();
      return null;
    }
  }

  PlannerRequest? _requestOf(String requestID) {
    for (final request in _requests) {
      if (request.requestID == requestID) return request;
    }
    return null;
  }

  /// Folds one journal read into the conversation and the open requests.
  ///
  /// A sequence already seen is skipped, so a read that overlaps the last one
  /// adds nothing twice; a request the record shows as settled never becomes a
  /// card again.
  void _apply(List<PlannerEvent> events) {
    for (final event in events) {
      if (event.sequence <= _sequence && event.sequence != 0) continue;
      _sequence = event.sequence > _sequence ? event.sequence : _sequence;
      final data = _data(event);
      switch (event.kind) {
        case 'turn.text':
          final text = _str(data['text']);
          if (text.isNotEmpty) {
            _messages.add(PlannerMessage(label: 'The planner', text: text));
          }
        case 'turn.tool':
          _messages.add(PlannerMessage(label: 'Tool', text: _toolLine(data)));
        case 'turn.prompt':
          // The person's own prompt is already in the conversation; the
          // journal's echo of it is not a second line.
          break;
        case 'turn.permission':
        case 'turn.question':
          _addRequest(event.kind, data);
        case 'turn.cancelled':
          _running = false;
          _messages.add(
            PlannerMessage(label: 'Turn cancelled', text: _str(data['reason'])),
          );
        case 'turn.uncertain':
          _running = false;
          _messages.add(
            PlannerMessage(
              label: 'Turn outcome unknown',
              text: _str(data['reason']).isEmpty
                  ? 'The backend could not observe the turn; it was not resent.'
                  : _str(data['reason']),
            ),
          );
        case 'planner.started':
          _messages.add(
            PlannerMessage(
              label: 'Started',
              text:
                  '${_modeWords(_str(data['mode']))} in workspace '
                  '${_str(data['workspaceId'])}',
            ),
          );
        case 'planner.ended':
          _running = false;
          _messages.add(
            const PlannerMessage(
              label: 'Ended',
              text: 'The planner has ended.',
            ),
          );
        default:
          // An event this build does not model is shown under its own name
          // rather than dropped: the journal is the record of what happened.
          _messages.add(
            PlannerMessage(
              label: event.kind,
              text: event.truncated ?? false
                  ? 'This event exceeded the journal bound.'
                  : '',
            ),
          );
      }
    }
    _running = _view == null ? _running : _turnInFlight;
  }

  /// Whether the record shows a turn that has not settled.
  bool get _turnInFlight => (_view?.turns ?? const <PlannerTurn>[]).any(
    (turn) => turn.state == 'dispatched' || turn.state == 'running',
  );

  /// Adds the card one request event raises, unless it is settled or already
  /// shown.
  void _addRequest(String kind, Map<String, Object?> data) {
    final requestID = _str(data['requestId']);
    if (requestID.isEmpty || _settled.contains(requestID)) return;
    for (final request in _requests) {
      if (request.requestID == requestID) return;
    }
    final permission = kind == 'turn.permission';
    final payload = _payload(data['params'] ?? data['update']);
    // The question is the card's title, so a question's body is empty; a
    // permission keeps its wording as the title and its command as the body.
    final asked = permission ? '' : _questionLine(payload);
    final titled = asked.isNotEmpty;
    _requests.add(
      PlannerRequest(
        requestID: requestID,
        method: _str(data['method']),
        permission: permission,
        title: permission
            ? 'Permission requested'
            : titled
            ? asked
            : 'Question from the planner',
        detail: permission
            ? _permissionLine(payload)
            : titled
            ? ''
            : asked,
        questions: permission ? const [] : _questionTexts(payload),
      ),
    );
  }

  /// The journal event's own payload, decoded. A payload this build cannot
  /// read is empty rather than a thrown exception: one odd event must not
  /// blank the conversation.
  static Map<String, Object?> _data(PlannerEvent event) {
    final raw = event.data;
    if (raw is Map) return _asMap(raw);
    if (raw is String && raw.isNotEmpty) {
      try {
        final decoded = jsonDecode(raw);
        if (decoded is Map) return _asMap(decoded);
      } on FormatException {
        // An unreadable payload is no payload.
      }
    }
    return const {};
  }

  /// A request payload, which an event may nest one level down.
  static Map<String, Object?> _payload(Object? raw) {
    final outer = raw is Map ? _asMap(raw) : const <String, Object?>{};
    final inner = outer['payload'];
    if (inner is Map && inner.isNotEmpty) return _asMap(inner);
    return outer;
  }

  static Map<String, Object?> _asMap(Map<dynamic, dynamic> raw) => {
    for (final entry in raw.entries)
      if (entry.key is String) entry.key as String: entry.value,
  };

  static String _str(Object? value) => value is String ? value : '';

  /// A tool update in its own words: its title, else its kind.
  static String _toolLine(Map<String, Object?> data) {
    final update = data['update'] is Map
        ? _asMap(data['update'] as Map)
        : const <String, Object?>{};
    final payload = update['payload'] is Map
        ? _asMap(update['payload'] as Map)
        : const <String, Object?>{};
    final title = _str(payload['title']);
    final name = title.isNotEmpty
        ? title
        : _str(payload['toolName']).isNotEmpty
        ? _str(payload['toolName'])
        : _str(payload['name']);
    final status = _str(payload['status']);
    final kind = _str(data['kind']).isNotEmpty
        ? _str(data['kind'])
        : _str(update['kind']);
    final head = name.isNotEmpty
        ? name
        : kind.isNotEmpty
        ? kind
        : 'tool activity';
    return status.isEmpty ? head : '$head ($status)';
  }

  /// A permission request in one line: the tool, the command, the risk and the
  /// reason, whichever the payload carries.
  static String _permissionLine(Map<String, Object?> payload) {
    final parts = [
      if (_str(payload['toolName']).isNotEmpty)
        'tool: ${_str(payload['toolName'])}',
      if (_str(payload['command']).isNotEmpty)
        'command: ${_str(payload['command'])}',
      if (_str(payload['risk']).isNotEmpty) 'risk: ${_str(payload['risk'])}',
      _str(payload['reason']),
    ].where((part) => part.isNotEmpty);
    return parts.isEmpty
        ? 'The planner is waiting for permission.'
        : parts.join(' · ');
  }

  /// A question request in one line: the first question's text, else the
  /// message the payload carried, else empty. Empty is a real answer here: a
  /// payload that asks nothing gets the card's own wording rather than a
  /// question nobody asked.
  static String _questionLine(Map<String, Object?> payload) {
    final asked = _questionTexts(payload);
    if (asked.isNotEmpty) return asked.first;
    for (final key in const ['message', 'prompt']) {
      final text = _str(payload[key]);
      if (text.isNotEmpty) return text;
    }
    return '';
  }

  /// Every question text a payload asks, in its own order.
  static List<String> _questionTexts(Map<String, Object?> payload) {
    final questions = payload['questions'];
    if (questions is! List) return const [];
    return List.unmodifiable([
      for (final question in questions)
        if (question is Map && _str(question['question']).isNotEmpty)
          _str(question['question']),
    ]);
  }

  /// The planner's access mode in the design system's words.
  static String _modeWords(String mode) => switch (mode) {
    'bypassPermissions' => 'Permissive',
    'default' => 'Restricted',
    _ => mode,
  };

  /// The same words, for a record that may name no mode at all: an absent or
  /// empty mode is null rather than a made-up one.
  static String? _modeWordsOrNull(String? mode) =>
      mode == null || mode.isEmpty ? null : _modeWords(mode);

  String _requestID() =>
      'app-planner-$plannerID-${DateTime.now().microsecondsSinceEpoch}-'
      '${_requests_++}';

  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  @override
  void notifyListeners() {
    if (_disposed) return;
    super.notifyListeners();
  }
}
