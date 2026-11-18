import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/thread_inspector_models.dart';

/// A transport no route reaches: these models are only built here, never read.
class _Unused implements Transport {
  @override
  Future<Response> send(Request request) async =>
      const Response(status: 404, body: '{"error":"no such route"}');

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// The five models an agent thread's inspector tabs read, owned by one object
/// (F9 decision 4): built from the thread's own ids, released together, and
/// released only once.
void main() {
  test('the holder builds the five models from the thread it was given', () {
    final models = ThreadInspectorModels(
      BridgeClient(_Unused()),
      agentId: 'a-1',
      sessionId: 'a-1',
      root: '/home/u/marshal',
    );

    // Each tab reads the thread's own ids: the agent its changes are, the
    // project root its files are under, and the session its nodes are in.
    expect(models.changes.agentId, 'a-1');
    expect(models.changes.sessionId, 'a-1');
    expect(models.files.root, '/home/u/marshal');
    expect(models.terminal.agentId, 'a-1');
    expect(models.preview.agentId, 'a-1');
    expect(models.inspect.sessionId, 'a-1');

    // Nothing is released before the thread closes.
    expect(() => models.inspect.addListener(() {}), returnsNormally);

    models.dispose();
    // Closed, the model no longer answers a listener.
    expect(() => models.inspect.addListener(() {}), throwsA(anything));
    // And closing the place twice is not a fault.
    expect(models.dispose, returnsNormally);
  });
}
