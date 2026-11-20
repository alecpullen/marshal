import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/fleet_profiles.dart';
import 'package:marshal_app/src/client/transport.dart';

/// The hosts commands as the daemon runs them (one-daemon D15, plan 7): the
/// app asks a daemon and runs nothing itself, so what these tests assert is the
/// route each command takes and the words a refusal carries back.
void main() {
  late _ScriptedTransport transport;
  late FleetProfiles profiles;

  setUp(() {
    transport = _ScriptedTransport();
    profiles = fleetProfiles(BridgeClient(transport));
  });

  test('add posts the name and destination', () async {
    await profiles.add('box', 'me@box');

    final request = transport.requests.single;
    expect(request.method, 'POST');
    expect(request.path, '/api/hosts');
    expect(jsonDecode(jsonEncode(request.body)), {
      'name': 'box',
      'ssh': 'me@box',
    });
    // Both builds answer that they can: the daemon does the work for a
    // browser as it does for the tray.
    expect(profiles.supported, isTrue);
  });

  test('remove deletes the name', () async {
    await profiles.remove('box');

    final request = transport.requests.single;
    expect(request.method, 'DELETE');
    expect(request.path, '/api/hosts/box');
  });

  test("a refusal carries the daemon's words", () async {
    // Every refusal these routes answer is JSON (web/bridge/hostedit.go's
    // 400s and 501, and the 403 guard's answer): what a form shows is the
    // sentence inside it, not the body.
    transport.answer = const Response(
      status: 400,
      body: '{"error":"the name \\"box\\" is taken"}',
    );

    await expectLater(
      profiles.add('box', 'me@box'),
      throwsA(
        isA<FleetProfileFailure>().having(
          (failure) => failure.message,
          'message',
          'the name "box" is taken',
        ),
      ),
    );
  });
}

/// A [Transport] that records what it was sent and answers [answer] every
/// time. Nothing listens on a socket, and no daemon is needed to know which
/// route a command takes.
class _ScriptedTransport implements Transport {
  /// Every request sent, in order.
  final requests = <Request>[];

  /// The answer the next request gets.
  Response answer = const Response(status: 200, body: '{"hosts":[]}');

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return answer;
  }

  @override
  Stream<String> lines(Request request) =>
      throw UnimplementedError('the hosts commands read no body as lines');
}
