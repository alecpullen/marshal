import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
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
}
