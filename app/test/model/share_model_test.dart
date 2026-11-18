import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/share_model.dart';

/// A transport that answers scripted routes and records what was asked, so a
/// test pins the routes the model calls without a socket.
class _Scripted implements Transport {
  _Scripted(this.replies);

  final Map<String, Response> replies;
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return replies['${request.method} ${request.path}'] ??
        Response(status: 404, body: jsonEncode({'error': 'no canned route'}));
  }

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the share model opens no stream');
}

/// The thread header's Share (F7 decision 7): one URL at a time, a refusal in
/// the bridge's words, and a revoke that stops the URL working.
void main() {
  Response created() => Response(
    status: 200,
    body: jsonEncode({
      'id': 'link-1',
      'url': 'https://marshal.example/s/abc123',
      'expiresAt': '2026-11-19T12:00:00Z',
    }),
  );

  test('mint stores the one URL', () async {
    final transport = _Scripted({'POST /api/status-links': created()});
    final model = ShareModel(BridgeClient(transport), agentId: 'a-1');
    addTearDown(model.dispose);

    await model.mint();

    expect(model.made?.url, 'https://marshal.example/s/abc123');
    expect(model.busy, isFalse);
    expect(model.failure, isNull);
    // The route carries the thread's own agent and the hours asked for.
    final request = transport.requests.single;
    expect(request.method, 'POST');
    expect(request.path, '/api/status-links');
    final body = request.body! as StatusLinkCreate;
    expect(body.agentID, 'a-1');
    expect(body.ttlHours, 24);
  });

  test('a refusal is reported in the bridge’s words', () async {
    final transport = _Scripted({
      'POST /api/status-links': Response(
        status: 409,
        body: jsonEncode({'error': 'that agent is already shared'}),
      ),
    });
    final model = ShareModel(BridgeClient(transport), agentId: 'a-1');
    addTearDown(model.dispose);

    await model.mint();

    // No URL for a link that was not made, and the reason is the bridge's.
    expect(model.made, isNull);
    expect(model.busy, isFalse);
    expect(model.failure, contains('that agent is already shared'));
  });

  test('revoke clears the link', () async {
    final transport = _Scripted({
      'POST /api/status-links': created(),
      'DELETE /api/status-links/link-1': const Response(status: 204),
    });
    final model = ShareModel(BridgeClient(transport), agentId: 'a-1');
    addTearDown(model.dispose);
    await model.mint();

    await model.revoke();

    expect(model.made, isNull);
    expect(model.busy, isFalse);
    final revoke = transport.requests.last;
    expect(revoke.method, 'DELETE');
    expect(revoke.path, '/api/status-links/link-1');
  });

  test('a link in flight notifies nobody once the sheet has closed', () async {
    final held = _HeldTransport();
    final model = ShareModel(BridgeClient(held), agentId: 'a-1');

    final making = model.mint();
    // The sheet closes while the bridge has not answered: the answer still
    // lands, and it must not reach a disposed model.
    model.dispose();
    held.release(created());

    await making;
    expect(model.made?.url, 'https://marshal.example/s/abc123');
  });
}

/// A transport whose answer waits for the test: a link can be in flight while
/// the sheet that asked for it closes.
class _HeldTransport implements Transport {
  final _answer = Completer<Response>();

  @override
  Future<Response> send(Request request) => _answer.future;

  /// Answers the mint that is in flight, as the bridge does after the sheet
  /// closed.
  void release(Response response) => _answer.complete(response);

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}
