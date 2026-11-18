import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';

/// What the Inspect tab draws for one node (f4-inspector.md decision 11):
/// the facts, then the tabs' bodies.
///
/// The model's own vocabulary, so the tab reads it without reaching for the
/// bridge's contract (ui/ never touches client/).
@immutable
class InspectFacts {
  const InspectFacts({
    this.title = '',
    this.kind = '',
    this.actor,
    this.why,
    this.took,
    this.sandbox,
    this.approval,
    this.callId,
    this.output = '',
    this.arguments = '',
    this.thinking = const [],
    this.modelRequest,
  });

  final String title;
  final String kind;
  final String? actor;
  final String? why;
  final String? took;
  final String? sandbox;
  final String? approval;
  final String? callId;
  final String output;
  final String arguments;
  final List<String> thinking;

  /// The newest model request, in words, once it has been asked for.
  final String? modelRequest;
}

/// One node's detail, and the newest model request behind it.
class InspectModel extends ChangeNotifier {
  InspectModel(this._client, {required this.sessionId, this.subagentId = 0});

  /// A model holding what a caller gives it, for a test or the gallery.
  InspectModel.seeded({InspectFacts? facts, this.modelRequest})
    : _client = null,
      sessionId = '',
      subagentId = 0,
      _facts = facts ?? const InspectFacts();

  final BridgeClient? _client;
  final String sessionId;
  final int subagentId;

  InspectFacts _facts = const InspectFacts();

  /// The newest model request, in words, once it has been asked for.
  String? modelRequest;
  String? _nodeId;
  bool _loading = false;
  String? _error;

  /// Whether the thread that owned this model closed it: a read still in
  /// flight when the place closes answers into nothing rather than into a
  /// disposed notifier.
  bool _disposed = false;

  /// What the tab draws.
  InspectFacts get facts => _facts;

  String? get nodeId => _nodeId;
  bool get loading => _loading;
  String? get error => _error;
  bool get requestLoading => _loadingRequest;
  bool _loadingRequest = false;

  /// Inspects [nodeId]: reads its detail. Null clears the tab.
  Future<void> select(String? nodeId) async {
    _nodeId = nodeId;
    modelRequest = null;
    if (nodeId == null) {
      _facts = const InspectFacts();
      _notify();
      return;
    }
    final client = _client;
    if (client == null) {
      _notify();
      return;
    }
    _loading = true;
    _error = null;
    _notify();
    try {
      final answer = await client.node(sessionId, nodeId, subagentId);
      _facts = _factsOf(answer);
    } catch (error) {
      _error = '$error';
    } finally {
      _loading = false;
      _notify();
    }
  }

  /// Reads the newest model request, on demand (decision 11).
  Future<void> loadRequest() async {
    final client = _client;
    if (client == null || modelRequest != null) return;
    _loadingRequest = true;
    _notify();
    try {
      final request = await client.lastRequest(sessionId);
      final model = request?.request;
      modelRequest = model == null
          ? 'This session has made no request.'
          : _requestSummary(model);
      _notify();
    } catch (error) {
      modelRequest = 'The request could not be read: $error';
      _notify();
    } finally {
      _loadingRequest = false;
      _notify();
    }
  }

  /// Notifies the tab, unless the thread that owned this model has closed
  /// it: a read already in flight still lands, and its answer must not reach
  /// a disposed notifier.
  void _notify() {
    if (!_disposed) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  static InspectFacts _factsOf(NodeDetailResponse answer) {
    final node = answer.node;
    final detail = answer.detail;
    final calls = detail.calls ?? const <CallDetail>[];
    final call = calls.isEmpty ? null : calls.first;
    final wireCall = (node.tool?.calls ?? const <WireCall>[]).isEmpty
        ? null
        : node.tool!.calls!.first;
    final step = detail.step;
    final title =
        node.step?.headline ??
        node.tool?.display ??
        node.message?.content ??
        node.id;
    return InspectFacts(
      title: title,
      kind: node.kind,
      actor: step?.owner,
      why: step?.why,
      took: wireCall?.durationMs == null ? null : '${wireCall!.durationMs} ms',
      sandbox: _sandbox(call?.sandbox),
      approval: [
        if ((wireCall?.approval ?? '').isNotEmpty) wireCall!.approval!,
        if ((wireCall?.risk ?? '').isNotEmpty) wireCall!.risk!,
      ].join(' · '),
      callId: call?.callID ?? wireCall?.callID,
      output: call == null
          ? (node.message?.content ?? node.completion?.report ?? '')
          : (call.output ?? call.diff ?? ''),
      arguments: call?.args ?? '',
      thinking: [
        for (final thought in detail.thinking ?? const <ThoughtDetail>[])
          thought.text,
        for (final text in detail.narration ?? const <String>[]) text,
      ],
    );
  }

  /// The sandbox record as the registry writes it: its own field names, not
  /// the wire's, so both spellings are read.
  static String? _sandbox(Object? raw) {
    if (raw is! Map) return null;
    final backend = raw['Backend'] ?? raw['backend'];
    final network =
        raw['NetworkIsolated'] == true || raw['networkIsolated'] == true;
    final parts = [
      if (backend is String && backend.isNotEmpty) backend,
      if (network) 'network isolated',
    ];
    return parts.isEmpty ? null : parts.join(' · ');
  }

  static String _requestSummary(ModelRequest request) {
    final model = request.model ?? request.provider ?? 'a model';
    final lines = [
      '$model · ${request.messages.length} messages · '
          '${request.tools.length} tools',
      for (final message in request.messages.take(4))
        '${message.role}: ${message.content.split('\n').first}',
    ];
    return lines.join('\n');
  }
}
