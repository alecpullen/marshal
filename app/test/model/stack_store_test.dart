import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

/// A transport the store never uses: every test here injects the fetcher, so
/// no request is sent and no socket is opened.
class _UnusedTransport implements Transport {
  @override
  Future<Response> send(Request request) async =>
      throw StateError('the stack store test does not send requests');

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the stack store test does not open streams');
}

/// The session stream a store follows, recorded rather than connected: it
/// writes to [log] when it starts, so a test can say when the store followed
/// against when the opener answered.
class _RecordedStream extends EventStream {
  _RecordedStream(this.log) : super(_UnusedTransport(), '/api/events');

  final List<String> log;

  final _events = StreamController<StreamEvent>.broadcast();
  final _gaps = StreamController<void>.broadcast();
  final _state = ValueNotifier<StreamState>(StreamState.connecting);

  @override
  Stream<StreamEvent> get events => _events.stream;

  @override
  Stream<void> get gaps => _gaps.stream;

  @override
  ValueListenable<StreamState> get state => _state;

  @override
  void start() => log.add('stream');

  @override
  void stop() {}
}

/// One recorded snapshot-and-patch sequence, as test/fixtures/stack holds it.
class _Sequence {
  _Sequence(this.snapshot, this.patches, this.finalSnapshot);

  /// The snapshot the sequence starts from.
  final StackSnapshot snapshot;

  /// The patches, in the order the agent sent them.
  final List<StackPatch> patches;

  /// The snapshot of the same tree after the last patch: what a client that
  /// applied them all must hold.
  final StackSnapshot finalSnapshot;

  static _Sequence load(String name) {
    final file = File('test/fixtures/stack/$name');
    expect(file.existsSync(), isTrue, reason: '${file.path} is missing');
    final json = jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
    return _Sequence(
      StackSnapshot.fromJson(json['snapshot'] as Map<String, dynamic>),
      [
        for (final patch in json['patches'] as List<dynamic>)
          StackPatch.fromJson(patch as Map<String, dynamic>),
      ],
      StackSnapshot.fromJson(json['final'] as Map<String, dynamic>),
    );
  }

  /// One patch as the session stream carries it: an update map.
  Map<String, dynamic> update(int index) => patches[index].toJson();
}

/// The snapshot route, counted so a test can prove a refetch happened.
class _Fetcher {
  _Fetcher(this.snapshot);

  StackSnapshot snapshot;
  int calls = 0;

  Future<StackSnapshot> call(String sessionId, int subagentId) async {
    calls++;
    return snapshot;
  }
}

StackStore _store(_Fetcher fetcher) =>
    StackStore(BridgeClient(_UnusedTransport()), 's-1', fetcher: fetcher.call);

/// Lets the store's queued refetch run.
Future<void> _settle() => Future<void>.delayed(Duration.zero);

/// Lets an opener, an open stream and the fetch that follows both all run: one
/// `_settle` covers one turn of the event loop, and opening takes several.
Future<void> _settleAll() async {
  for (var i = 0; i < 5; i++) {
    await _settle();
  }
}

/// A node map as the store holds it, for comparing with a snapshot.
Map<String, Map<String, dynamic>> _held(StackStore store) => {
  for (final node in store.nodes) node.id: node.toJson(),
};

Map<String, Map<String, dynamic>> _snapshotNodes(StackSnapshot snapshot) => {
  for (final node in snapshot.nodes) node.id: node.toJson(),
};

void main() {
  test('snapshot loads', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);

    expect(store.status, StackStatus.loading);
    await store.refresh();

    expect(store.status, StackStatus.ready);
    expect(store.rev, sequence.snapshot.rev);
    expect(store.roots, sequence.snapshot.roots);
    expect(store.node('turn:1')?.kind, 'turn');
    expect(store.node('step:1')?.step?.headline, 'Reading the parser.');
    expect(fetcher.calls, 1);
    store.dispose();
  });

  test('in-order patch applies', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    store.onUpdate(sequence.update(0));

    expect(store.rev, 2);
    expect(store.node('tool:1:call_0')?.tool?.name, 'file.read');
    expect(store.node('turn:1')?.children, ['step:1', 'tool:1:call_0']);
    // An applied patch is not a reason to ask again.
    expect(fetcher.calls, 1);
    store.dispose();
  });

  test('out-of-order patch refetches', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    // The second patch builds on a revision this store does not hold.
    store.onUpdate(sequence.update(1));
    await _settle();

    expect(fetcher.calls, 2, reason: 'a revision gap refetches the snapshot');
    // The patch itself was discarded: the store is exactly its snapshot.
    expect(_held(store), _snapshotNodes(sequence.snapshot));
    store.dispose();
  });

  test('removal removes', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();
    for (var i = 0; i < 2; i++) {
      store.onUpdate(sequence.update(i));
    }
    expect(store.node('completion:job:3'), isNotNull);

    store.onUpdate(sequence.update(2));

    expect(store.node('completion:job:3'), isNull);
    expect(store.rev, 4);
    store.dispose();
  });

  test('a patch for another transcript is ignored', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    final forChild = Map<String, dynamic>.from(sequence.update(0))
      ..['subagentId'] = 3;
    store.onUpdate(forChild);
    await _settle();

    expect(store.rev, sequence.snapshot.rev);
    expect(fetcher.calls, 1);
    store.dispose();
  });

  test('reconnect refetches', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    store.onStreamLive();
    await _settle();

    expect(fetcher.calls, 2, reason: 'a reconnect may have skipped patches');
    store.dispose();
  });

  test('a stream replay gap refetches', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    store.onStreamGap();
    await _settle();

    expect(fetcher.calls, 2);
    store.dispose();
  });

  test('a snapshot that lands after a patch does not rewind it', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final gate = Completer<void>();
    var calls = 0;
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      // The second fetch is held in flight, as a slow HTTP answer is while the
      // stream's patch for the same tree arrives.
      fetcher: (sessionId, subagentId) async {
        calls++;
        if (calls == 2) await gate.future;
        return sequence.snapshot;
      },
    );
    await store.refresh();
    expect(store.status, StackStatus.ready);
    expect(store.rev, sequence.snapshot.rev);

    // A refresh starts — a reconnect, say — and does not answer yet.
    final pending = store.refresh();
    await _settle();

    // A patch for this transcript lands while the snapshot is on its way.
    store.onUpdate(sequence.update(0));
    expect(store.rev, 2);
    expect(store.node('tool:1:call_0'), isNotNull);

    // The held fetch answers the tree from before that patch.
    gate.complete();
    await pending;

    expect(store.rev, 2, reason: 'the older snapshot must not win');
    expect(
      store.node('tool:1:call_0')?.tool?.name,
      'file.read',
      reason: 'the patch that landed mid-flight is not undone',
    );
    store.dispose();
  });

  test('recorded sequence ends equal to its final snapshot', () async {
    for (final name in const ['turn-arrives.json', 'turn-settles.json']) {
      final sequence = _Sequence.load(name);
      final fetcher = _Fetcher(sequence.snapshot);
      final store = _store(fetcher);
      await store.refresh();

      for (var i = 0; i < sequence.patches.length; i++) {
        store.onUpdate(sequence.update(i));
      }

      expect(store.rev, sequence.finalSnapshot.rev, reason: name);
      expect(store.roots, sequence.finalSnapshot.roots, reason: name);
      expect(
        _held(store),
        _snapshotNodes(sequence.finalSnapshot),
        reason: '$name: applying every patch must equal the final snapshot',
      );
      // Nothing about replaying a recorded sequence needs a refetch.
      expect(fetcher.calls, 1, reason: name);
      store.dispose();
    }
  });

  test('a node the roots do not reach is dropped', () async {
    final sequence = _Sequence.load('turn-settles.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    // A patch whose roots no longer name the turn reaches nothing.
    store.onUpdate({
      'kind': 'stack_patch',
      'rev': store.rev + 1,
      'baseRev': store.rev,
      'roots': <String>[],
    });

    expect(store.length, 0);
    expect(store.roots, isEmpty);
    store.dispose();
  });

  test('a node carrying fields this build does not know still loads', () async {
    // A newer bridge may send a payload this build has no field for, and a call
    // may carry no lifecycle evidence at all. Neither is an error: the store
    // keeps the node it understands and drops what it does not.
    final json = <String, dynamic>{
      'sessionId': 's-1',
      'rev': 3,
      'roots': ['turn:1'],
      'nodes': [
        {
          'id': 'turn:1',
          'kind': 'turn',
          'children': ['tool:1:call_1'],
          'futureField': {'anything': true},
        },
        {
          'id': 'tool:1:call_1',
          'kind': 'tool',
          'parent': 'turn:1',
          'tool': {
            'name': 'agent.integrate',
            'display': 'Integrate task-1',
            'calls': [
              {
                'callId': 'call_1',
                'output': 'task_key: task-1',
                'futureField': 7,
              },
            ],
          },
        },
      ],
    };
    final fetcher = _Fetcher(StackSnapshot.fromJson(json));
    final store = _store(fetcher);
    await store.refresh();

    expect(store.status, StackStatus.ready);
    expect(store.rev, 3);
    expect(store.node('turn:1')?.kind, 'turn');
    // The call carries no lifecycle evidence in this fixture, which is the
    // ordinary case for a receipt this build has no typed payload for.
    final call = store.node('tool:1:call_1')?.tool?.calls?.single;
    expect(call?.callID, 'call_1');
    expect(call?.output, 'task_key: task-1');
    store.dispose();
  });

  test('plainText projects the row a selection copies', () async {
    final sequence = _Sequence.load('turn-settles.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    await store.refresh();

    final text = store.plainText('tool:1:call_1');
    expect(text, contains('go test'));
    expect(text, contains('PASS'));
    expect(store.plainText('no-such-node'), isEmpty);
    store.dispose();
  });

  test('plainText leads a thinking row with the line it draws', () {
    final store = StackStore.seeded(
      const StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: ['turn:1'],
        nodes: [
          WireNode(id: 'turn:1', kind: 'turn', children: ['think:1']),
          WireNode(
            id: 'think:1',
            kind: 'thinking',
            parent: 'turn:1',
            thinking: WireThinking(
              text: 'The parser is the first stop.\nThe fixtures matter more.',
            ),
          ),
        ],
      ),
    );

    // The row is drawn as a notice whose first line names it, so the copied
    // text carries that line: drawn text stays findable in the projection the
    // selection highlights from (selection_layer.dart's `_HighlightPainter`).
    final text = store.plainText('think:1');
    expect(text, startsWith('Thinking'));
    expect(text, contains('The parser is the first stop.'));
    expect(text, contains('The fixtures matter more.'));
    expect(text.split('\n').first, 'Thinking');
    store.dispose();
  });

  test('fold override survives a patch', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final fetcher = _Fetcher(sequence.snapshot);
    final store = _store(fetcher);
    final view = ThreadViewState();
    await store.refresh();

    view.toggleFold('turn:1');
    expect(view.folded('turn:1'), isTrue);

    // The patch replaces the turn node the fold was made on.
    store.onUpdate(sequence.update(0));

    expect(store.node('turn:1')?.children?.length, 2);
    expect(
      view.folded('turn:1'),
      isTrue,
      reason: 'a fold is the user\'s choice, not the node\'s',
    );
    view.dispose();
    store.dispose();
  });

  test('a store with an opener opens before it follows', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final log = <String>[];
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async {
        log.add('fetch');
        return sequence.snapshot;
      },
      events: () => _RecordedStream(log),
      opener: () async {
        log.add('open');
        await Future<void>.delayed(Duration.zero);
        log.add('opened');
      },
    );

    expect(store.status, StackStatus.loading);
    store.start();
    // The opener is in flight: nothing is followed and nothing is read.
    expect(log, [
      'open',
    ], reason: 'the opener runs before anything is followed');

    await _settleAll();

    expect(log, ['open', 'opened', 'stream', 'fetch']);
    expect(store.status, StackStatus.ready);
    expect(store.rev, sequence.snapshot.rev);
    store.dispose();
  });

  test('a failed open is an error with the bridge\'s words', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final log = <String>[];
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async {
        log.add('fetch');
        return sequence.snapshot;
      },
      events: () => _RecordedStream(log),
      opener: () async => throw BridgeFailure(
        409,
        '{"error":"sess_1 is open in a terminal on studio"}',
      ),
    );

    store.start();
    await _settleAll();

    expect(store.status, StackStatus.error);
    expect(store.failure, 'sess_1 is open in a terminal on studio');
    expect(log, isEmpty, reason: 'a failed open opens nothing');
    store.dispose();
  });

  test('a 404 for an opened session is gone', () async {
    var opens = 0;
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async =>
          throw BridgeFailure(404, '{"error":"no such session"}'),
      events: () => _RecordedStream(<String>[]),
      opener: () async {
        opens++;
      },
    );

    store.start();
    await _settleAll();

    expect(opens, 1, reason: 'the session was opened before it was followed');
    expect(store.status, StackStatus.gone);
    store.dispose();
  });

  test('a 404 for an agent is still an error', () async {
    // No opener: an agent's session is not something this screen reopens, so
    // its 404 is the failed read it always was (P7 decision 6).
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      'a-1',
      fetcher: (sessionId, subagentId) async =>
          throw BridgeFailure(404, '{"error":"no such session"}'),
    );

    await store.refresh();

    expect(store.status, StackStatus.error);
    expect(
      store.failure,
      isNull,
      reason: 'the bridge’s words are a session’s, so an agent keeps its own',
    );
    store.dispose();
  });

  test('a transport outage keeps the plain sentence', () async {
    // A session's store, opened and followed, whose read throws something
    // that is not the bridge answering at all.
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async =>
          throw Exception('connection refused'),
      events: () => _RecordedStream(<String>[]),
      opener: () async {},
    );

    store.start();
    await _settleAll();

    expect(store.status, StackStatus.error);
    expect(
      store.failure,
      isNull,
      reason:
          'a raw exception is not the bridge speaking: the screen keeps '
          'the design system’s plain words',
    );
    store.dispose();
  });

  test('a second 404 on a gone session says nothing new', () async {
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async =>
          throw BridgeFailure(404, '{"error":"no such session"}'),
      events: () => _RecordedStream(<String>[]),
      opener: () async {},
    );
    var notifications = 0;
    store.addListener(() => notifications++);

    store.start();
    await _settleAll();
    expect(store.status, StackStatus.gone);
    final told = notifications;

    // The store keeps following its stream, so a gap refetches again and the
    // 404 arrives a second time.
    await store.refresh();
    await _settleAll();

    expect(store.status, StackStatus.gone);
    expect(
      notifications,
      told,
      reason: 'nothing changed, so there is nothing to say',
    );
    store.dispose();
  });

  test('reopen opens again and refetches', () async {
    final sequence = _Sequence.load('turn-arrives.json');
    final log = <String>[];
    var opens = 0;
    var calls = 0;
    var closed = true;
    final store = StackStore(
      BridgeClient(_UnusedTransport()),
      's-1',
      fetcher: (sessionId, subagentId) async {
        calls++;
        if (closed) throw BridgeFailure(404, '{"error":"closed"}');
        return sequence.snapshot;
      },
      events: () => _RecordedStream(log),
      opener: () async {
        opens++;
      },
    );

    store.start();
    await _settleAll();
    expect(store.status, StackStatus.gone);
    expect(opens, 1);

    closed = false;
    final reopening = store.reopen();
    expect(store.status, StackStatus.loading);
    await reopening;

    expect(opens, 2, reason: 'the opener runs again');
    expect(calls, 2, reason: 'and the transcript is read again');
    expect(store.status, StackStatus.ready);
    expect(store.failure, isNull);
    expect(
      log.where((entry) => entry == 'stream'),
      hasLength(1),
      reason: 'the stream was open, so it is not opened a second time',
    );
    store.dispose();
  });
}
