import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/event_stream.dart';
import '../client/transport.dart';

/// What a [StackStore] has: the snapshot it was seeded with, a patch stream it
/// is applying, or neither.
enum StackStatus {
  /// The first snapshot is in flight.
  loading,

  /// The store holds a snapshot and every patch since it.
  ready,

  /// The agent has no stack for this session: the route answered 501.
  unsupported,

  /// The first fetch failed and nothing is held yet.
  error,
}

/// One session transcript's tree, as the bridge relays it.
///
/// The tree is built by the agent (`internal/viewmodel`) and never regrouped
/// here: this store holds the node map and the root order, applies the patches
/// whose base revision is the one it holds, and refetches the snapshot for
/// anything else — a revision gap, a stream replay gap, a reconnect.
///
/// It is a [ChangeNotifier] and imports nothing from Flutter beyond
/// `foundation.dart`, so its rules are plain unit tests (model/README.md).
class StackStore extends ChangeNotifier {
  /// Holds one transcript: the parent's when [subagentId] is 0, a child's
  /// otherwise.
  ///
  /// [fetcher] and [events] are the two seams a test drives without a socket;
  /// production passes neither, and both come from [client].
  StackStore(
    BridgeClient client,
    this.sessionId, {
    this.subagentId = 0,
    Future<StackSnapshot> Function(String sessionId, int subagentId)? fetcher,
    EventStream Function()? events,
  }) : _fetcher =
           fetcher ??
           ((sessionId, subagentId) =>
               client.stack(sessionId, subagentId: subagentId)),
       _openEvents = events ?? (() => client.sessionEvents(sessionId));

  /// The session whose transcript this is.
  final String sessionId;

  /// The child transcript this store holds, 0 for the parent's.
  final int subagentId;

  final Future<StackSnapshot> Function(String, int) _fetcher;
  final EventStream Function() _openEvents;

  StackStatus _status = StackStatus.loading;
  int _rev = 0;
  List<String> _roots = const [];
  final _nodes = <String, WireNode>{};

  EventStream? _stream;
  StreamSubscription<StreamEvent>? _events;
  StreamSubscription<void>? _gaps;
  bool _wasLive = false;
  bool _disposed = false;

  Future<void>? _inflight;
  bool _again = false;
  int _generation = 0;

  /// The revision the store holds; 0 before the first snapshot.
  int get rev => _rev;

  /// The root node ids, in transcript order.
  List<String> get roots => List<String>.unmodifiable(_roots);

  /// What the store has.
  StackStatus get status => _status;

  /// The node with [id], or null when the tree does not hold it.
  WireNode? node(String id) => _nodes[id];

  /// Every node the tree holds, in no particular order.
  Iterable<WireNode> get nodes => _nodes.values;

  /// How many nodes the tree holds.
  int get length => _nodes.length;

  /// Starts following the session: the first snapshot, then its patches.
  ///
  /// A second call while running does nothing, so a screen that rebuilds does
  /// not open a second stream.
  void start() {
    if (_stream != null) return;
    final stream = _stream = _openEvents();
    _events = stream.events.listen(_onEvent);
    _gaps = stream.gaps.listen((_) => onStreamGap());
    stream.state.addListener(_onStreamState);
    stream.start();
    unawaited(refresh());
  }

  /// Stops following the session: the stream is closed and no patch is taken.
  /// What the store holds stays, so a screen that comes back redraws at once.
  void stop() {
    final stream = _stream;
    _stream = null;
    stream?.state.removeListener(_onStreamState);
    stream?.stop();
    unawaited(_events?.cancel());
    unawaited(_gaps?.cancel());
    _events = null;
    _gaps = null;
    _wasLive = false;
  }

  /// Refetches the snapshot: one at a time, with a request made while one is
  /// in flight folded into a single follow-up.
  Future<void> refresh() {
    if (_disposed) return Future<void>.value();
    final inflight = _inflight;
    if (inflight != null) {
      _again = true;
      return inflight;
    }
    final generation = ++_generation;
    return _inflight = _load(generation).whenComplete(() {
      _inflight = null;
    });
  }

  Future<void> _load(int generation) async {
    do {
      _again = false;
      await _fetchOnce(generation);
    } while (_again && !_disposed);
  }

  Future<void> _fetchOnce(int generation) async {
    final StackSnapshot snapshot;
    try {
      snapshot = await _fetcher(sessionId, subagentId);
    } on BridgeFailure catch (failure) {
      if (_disposed || generation != _generation) return;
      if (failure.status == 501) {
        // The agent has no stack for this transcript; asking again cannot
        // change that.
        _nodes.clear();
        _roots = const [];
        _rev = 0;
        _status = StackStatus.unsupported;
        notifyListeners();
        return;
      }
      _failed();
      return;
    } catch (_) {
      if (_disposed || generation != _generation) return;
      _failed();
      return;
    }
    if (_disposed || generation != _generation) return;
    _nodes
      ..clear()
      ..addEntries([
        for (final node in snapshot.nodes) MapEntry(node.id, node),
      ]);
    _collect(_nodes, snapshot.roots);
    _roots = snapshot.roots;
    _rev = snapshot.rev;
    _status = StackStatus.ready;
    notifyListeners();
  }

  /// A failed fetch keeps a snapshot already held; only a store that holds
  /// nothing reports it, and the next event tries again.
  void _failed() {
    if (_status == StackStatus.ready) return;
    _status = StackStatus.error;
    notifyListeners();
  }

  /// Applies one update from the session stream, as `session/update` carries
  /// it.
  ///
  /// An update that is not a patch for this transcript is ignored; a patch
  /// whose base revision is not the store's triggers a snapshot fetch and is
  /// otherwise discarded, which is the only way to be sure nothing was
  /// skipped.
  void onUpdate(Object? update) {
    if (_disposed || update is! Map) return;
    final kind = update['kind'];
    if (kind == 'session_telemetry') {
      unawaited(refresh());
      return;
    }
    if (kind != 'stack_patch') return;
    final StackPatch patch;
    try {
      patch = StackPatch.fromJson(Map<String, dynamic>.from(update));
    } catch (_) {
      // A malformed patch is not a patch: refetch rather than guess.
      unawaited(refresh());
      return;
    }
    // One stream carries every transcript's patches; each store takes its
    // own.
    if ((patch.subagentID ?? 0) != subagentId) return;
    _applyPatch(patch);
  }

  /// The stream could not replay from where this client was: what the store
  /// holds may have missed something, so it refetches.
  void onStreamGap() => unawaited(refresh());

  /// The stream is live again after a drop. A reconnect may have skipped
  /// patches, so the snapshot — not the patch — is the authority.
  void onStreamLive() => unawaited(refresh());

  void _applyPatch(StackPatch patch) {
    switch (_status) {
      case StackStatus.unsupported:
        return;
      case StackStatus.error:
        // A retry is already due while in error; patches do not each fetch.
        return;
      case StackStatus.loading:
        // The snapshot in flight may or may not include this patch. Whatever
        // it returns is the authority; ask for one more look afterwards.
        unawaited(refresh());
        return;
      case StackStatus.ready:
        break;
    }
    if (patch.rev <= _rev) return;
    if (patch.baseRev != _rev) {
      unawaited(refresh());
      return;
    }
    final nodes = Map<String, WireNode>.of(_nodes);
    for (final node in patch.upsert ?? const <WireNode>[]) {
      nodes[node.id] = node;
    }
    for (final id in patch.remove ?? const <String>[]) {
      nodes.remove(id);
    }
    _collect(nodes, patch.roots);
    _nodes
      ..clear()
      ..addAll(nodes);
    _roots = patch.roots;
    _rev = patch.rev;
    notifyListeners();
  }

  /// Drops every node the roots do not reach, so a removed subtree leaves
  /// nothing behind (the Svelte store's rule, stack.ts).
  static void _collect(Map<String, WireNode> nodes, List<String> roots) {
    final seen = <String>{};
    final stack = <String>[...roots];
    while (stack.isNotEmpty) {
      final id = stack.removeLast();
      if (!seen.add(id)) continue;
      for (final child in nodes[id]?.children ?? const <String>[]) {
        stack.add(child);
      }
    }
    nodes.removeWhere((id, _) => !seen.contains(id));
  }

  void _onEvent(StreamEvent event) {
    Object? decoded;
    try {
      decoded = jsonDecode(event.data);
    } catch (_) {
      return;
    }
    if (decoded is! Map) return;
    // The bridge's own envelope, before the JSON-RPC notification's.
    if (decoded['type'] == 'connected' ||
        decoded['type'] == 'replay_overflow') {
      onStreamLive();
      return;
    }
    if (decoded['method'] != 'session/update') return;
    final params = decoded['params'];
    if (params is! Map) return;
    final update = params['update'];
    if (update is Map) onUpdate(update);
  }

  /// The stream going live again after a drop may have missed patches.
  void _onStreamState() {
    final live = _stream?.state.value == StreamState.live;
    if (live && !_wasLive) onStreamLive();
    _wasLive = live;
  }

  /// The plain text of one node: what selecting it copies.
  ///
  /// It is the projection the rows draw, never a re-derivation of the tree:
  /// a step row is its headline and narration, a tool row is what each call
  /// said, a message row is its content.
  String plainText(String nodeId) {
    final node = _nodes[nodeId];
    if (node == null) return '';
    final parts = <String>[];
    void add(String? text) {
      final value = text?.trim() ?? '';
      if (value.isEmpty) return;
      parts.add(value);
    }

    final message = node.message;
    if (message != null) add(message.content);
    final step = node.step;
    if (step != null) {
      add(step.headline);
      add(step.rest);
      for (final narration in step.narration ?? const <String>[]) {
        add(narration);
      }
      for (final thought in step.thoughts ?? const <WireThought>[]) {
        add(thought.text);
      }
    }
    final tool = node.tool;
    if (tool != null) {
      for (final call in tool.calls ?? const <WireCall>[]) {
        add(call.summaryLine);
        add(call.summary);
        add(call.target);
        add(call.output);
        add(call.error);
        for (final line in call.tail ?? const <String>[]) {
          add(line);
        }
      }
      final running = tool.running;
      if (running != null) {
        add(running.subject);
        add(running.target);
        add(running.output);
      }
    }
    final completion = node.completion;
    if (completion != null) {
      add(completion.label);
      add(completion.outcome);
      add(completion.summary);
      add(completion.output);
      add(completion.report);
      add(completion.error);
    }
    final thinking = node.thinking;
    if (thinking != null) add(thinking.text);
    final runEvent = node.runEvent;
    if (runEvent != null) {
      add(runEvent.title);
      add(runEvent.detail);
      add(runEvent.body);
    }
    final plan = node.plan;
    if (plan != null) add(plan.activeText);
    final band = node.band;
    if (band != null) add(band.active);
    final receipt = node.receipt;
    if (receipt != null) add(receipt.usage);
    return parts.join('\n');
  }

  @override
  void dispose() {
    _disposed = true;
    _generation++;
    stop();
    super.dispose();
  }
}
