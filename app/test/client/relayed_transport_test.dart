import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/relayed_transport.dart';
import 'package:marshal_app/src/client/transport.dart';

/// [relayedTransport] against a recording transport: every route a host serves
/// is written as though the host were reached directly, and the relay's prefix
/// is added at this one seam (one-daemon D10).
void main() {
  late _RecordingTransport relay;

  setUp(() => relay = _RecordingTransport());

  /// The host as a client above this seam sees it.
  Transport host({String name = 'alpha'}) =>
      relayedTransport(relay, host: name);

  test('routes go under the host\'s prefix', () async {
    relay.reply(const Response(status: 200, body: '[]'));

    await host().send(
      const Request(
        method: 'POST',
        path: '/api/agents/a-1/prompt',
        query: {'sessionId': 's-1'},
        body: {'text': 'fix it'},
      ),
    );

    // The route is the original's — method, query and body included — and only
    // its path is the host's.
    final forwarded = relay.requests.single;
    expect(forwarded.path, '/api/hosts/alpha/api/agents/a-1/prompt');
    expect(forwarded.method, 'POST');
    expect(forwarded.query, {'sessionId': 's-1'});
    expect(forwarded.body, {'text': 'fix it'});
  });

  test('a name with a slash is one segment', () async {
    relay.reply(const Response(status: 200));

    await host(name: 'team/alpha').send(const Request(path: '/api/agents'));

    expect(
      relay.requests.single.path,
      '/api/hosts/team%2Falpha/api/agents',
      reason: 'the name is encoded once, as one path segment',
    );
  });

  test('the route\'s own headers are kept, and nothing is added', () async {
    relay.reply(const Response(status: 200));

    await host().send(
      const Request(
        path: '/api/projects/settings',
        headers: {'If-Match': '"abc123"'},
      ),
    );

    // The page holds no credential (one-daemon D13): a forwarded request
    // carries the route's own headers and no token of its own or the host's.
    expect(relay.requests.single.headers, {'If-Match': '"abc123"'});
  });

  test('a 503 from the relay is the relay\'s words', () async {
    // The daemon answers for itself when it cannot reach the host, and that
    // answer is the caller's: nothing here rewrites a failure it did not
    // cause.
    relay.reply(const Response(status: 503, body: 'no relayed host alpha'));

    await expectLater(
      BridgeClient(host()).daemon(),
      throwsA(
        isA<BridgeFailure>()
            .having((failure) => failure.status, 'status', 503)
            .having(
              (failure) => failure.message,
              'message',
              'no relayed host alpha',
            ),
      ),
    );
    expect(relay.requests.single.path, '/api/hosts/alpha/api/daemon');
  });
}

/// A [Transport] that records every request and answers with what the test
/// scripted, so a test can say exactly what the relay was asked for.
class _RecordingTransport implements Transport {
  /// One request per call, in order.
  final requests = <Request>[];

  Response _response = const Response(status: 200);

  /// Sets the answer every request gets.
  void reply(Response response) => _response = response;

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return _response;
  }

  @override
  Stream<String> lines(Request request) {
    requests.add(request);
    return const Stream<String>.empty();
  }
}
