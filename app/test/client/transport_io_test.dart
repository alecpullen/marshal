import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/transport.dart';

import '../support/fake_bridge.dart';

/// The native transports against an in-process fake bridge: one over a unix
/// socket, the way the local daemon is reached, and one over a loopback origin
/// behind a bearer token, the way a server profile is.
void main() {
  late Directory dir;
  late FakeBridge fake;

  setUp(() async {
    // flutter_test's binding answers every HttpClient with 400 (see
    // flutter_test_config.dart, which initializes it). These tests talk to a
    // fake bridge on this machine, so they want the real client back.
    HttpOverrides.global = null;
    dir = await Directory.systemTemp.createTemp('marshal-transport-');
    fake = await FakeBridge.onLoopback();
    addTearDown(() async {
      await fake.close();
      await dir.delete(recursive: true);
    });
  });

  test('unix socket request round trips', () async {
    final socketPath = '${dir.path}/bridge.sock';
    final unix = await FakeBridge.onUnixSocket(socketPath);
    addTearDown(unix.close);
    unix.reply(
      'GET',
      '/api/daemon',
      const FakeReply(
        200,
        headers: {'content-type': 'application/json'},
        body: '{"version":"0.9.1"}',
      ),
    );

    final response = await unixTransport(socketPath)
        .send(const Request(path: '/api/daemon'));

    expect(response.status, 200);
    expect(response.body, '{"version":"0.9.1"}');
    expect(response.ok, isTrue);
    expect(unix.requests, hasLength(1));
    expect(unix.requests.single.method, 'GET');
    expect(unix.requests.single.uri, '/api/daemon');
  });

  test('bearer token is sent', () async {
    final bridge = await FakeBridge.onLoopback(token: 'right-token');
    addTearDown(bridge.close);
    bridge.reply('GET', '/api/agents', const FakeReply(200, body: '[]'));

    final response = await serverTransport(
      bridge.base,
      token: () async => 'right-token',
    ).send(const Request(path: '/api/agents'));

    expect(response.status, 200);
    expect(
      bridge.requests.single.headers[HttpHeaders.authorizationHeader],
      'Bearer right-token',
    );
  });

  test('401 is Unauthorized', () async {
    final bridge = await FakeBridge.onLoopback(token: 'right-token');
    addTearDown(bridge.close);
    bridge.reply('GET', '/api/agents', const FakeReply(200, body: '[]'));

    await expectLater(
      serverTransport(
        bridge.base,
        token: () async => 'wrong-token',
      ).send(const Request(path: '/api/agents')),
      throwsA(isA<Unauthorized>()),
    );
  });

  test('error body becomes BridgeFailure message', () async {
    fake.reply(
      'POST',
      '/api/agents',
      const FakeReply(500, body: 'the bridge is out of room'),
    );

    final failure =
        await serverTransport(fake.base, token: () async => 'unused')
            .send(
              const Request(
                method: 'POST',
                path: '/api/agents',
                body: {'project': '/home/u/src/marshal'},
              ),
            )
            .then<Object?>((_) => null, onError: (Object error) => error);

    expect(failure, isA<BridgeFailure>());
    final bridgeFailure = failure! as BridgeFailure;
    expect(bridgeFailure.status, 500);
    expect(bridgeFailure.message, contains('out of room'));
    expect(bridgeFailure, isNot(isA<Unauthorized>()));
    // The body the request carried is JSON on the wire.
    expect(fake.requests.single.body, '{"project":"/home/u/src/marshal"}');
  });

  test('a route header reaches the daemon over the unix socket', () async {
    // A conditional write names If-Match on the request, not on the transport,
    // so a transport that dropped it would turn the write unconditional and
    // defeat the stale-save protection the project page advertises
    // (web/bridge/projects.go).
    final socketPath = '${dir.path}/settings.sock';
    final unix = await FakeBridge.onUnixSocket(socketPath);
    addTearDown(unix.close);
    unix.reply(
      'PUT',
      '/api/projects/settings',
      const FakeReply(200, body: '{}'),
    );

    final response = await unixTransport(socketPath).send(
      const Request(
        method: 'PUT',
        path: '/api/projects/settings',
        headers: {'If-Match': '"0123"'},
      ),
    );

    expect(response.status, 200);
    expect(unix.requests.single.headers['if-match'], '"0123"');
  });

  test('a route header survives a request that carries a body', () async {
    // The body path sets its own Content-Type after the route's headers; it
    // must not replace them.
    fake.reply(
      'PUT',
      '/api/projects/settings',
      const FakeReply(200, body: '{}'),
    );

    final response =
        await serverTransport(fake.base, token: () async => 'unused').send(
          const Request(
            method: 'PUT',
            path: '/api/projects/settings',
            body: {'workspace': 'alpha'},
            headers: {'If-Match': '"4567"'},
          ),
        );

    expect(response.status, 200);
    final put = fake.requests.single;
    expect(put.headers['if-match'], '"4567"');
    expect(put.headers['content-type'], contains('application/json'));
    expect(put.headers[HttpHeaders.authorizationHeader], 'Bearer unused');
  });

  test('lines yields one element per line as they arrive', () async {
    fake.reply('GET', '/api/agents', const FakeReply(200, body: '[]'));
    final frames = fake.stream('GET', '/api/events');
    final lines = StreamIterator(
      serverTransport(
        fake.base,
        token: () async => 'unused',
      ).lines(const Request(path: '/api/events')),
    );
    addTearDown(lines.cancel);

    // Nothing has ended the body yet, so a transport that buffered the whole
    // answer would never answer these.
    frames.write('id: 1\n');
    frames.write('data: one\n');
    frames.write('\n');

    const wait = Duration(seconds: 5);
    expect(await lines.moveNext().timeout(wait), isTrue);
    expect(lines.current, 'id: 1');
    expect(await lines.moveNext().timeout(wait), isTrue);
    expect(lines.current, 'data: one');
    expect(await lines.moveNext().timeout(wait), isTrue);
    expect(lines.current, '');

    // A clean end finishes the stream rather than failing it.
    await frames.end();
    expect(await lines.moveNext().timeout(wait), isFalse);
  });
}
