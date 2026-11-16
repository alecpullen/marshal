import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/terminal_session.dart';

import '../support/fake_bridge.dart';

FakeReply _json(Object body, [int status = 200]) => FakeReply(
  status,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);

/// Lets the event loop and the fake bridge run, so a frame written to the
/// stream has been delivered.
Future<void> settle() async {
  for (var i = 0; i < 10; i++) {
    await pumpEventQueue();
    await Future<void>.delayed(const Duration(milliseconds: 5));
  }
}

void main() {
  late FakeBridge bridge;
  late BridgeClient client;
  late FakeStream events;

  setUp(() async {
    HttpOverrides.global = null;
    bridge = await FakeBridge.onLoopback();
    client = BridgeClient(
      serverTransport(bridge.base, token: () async => 'unused'),
    );
    events = bridge.stream('GET', '/api/agents/a-1/terminal/t-1/events');
    bridge
      ..reply('POST', '/api/agents/a-1/terminal', _json({'terminalId': 't-1'}))
      ..reply(
        'POST',
        '/api/agents/a-1/terminal/t-1/input',
        const FakeReply(204),
      )
      ..reply(
        'POST',
        '/api/agents/a-1/terminal/t-1/resize',
        const FakeReply(204),
      )
      ..reply(
        'POST',
        '/api/agents/a-1/terminal/t-1/release',
        const FakeReply(204),
      )
      ..reply('DELETE', '/api/agents/a-1/terminal/t-1', const FakeReply(204));
  });

  tearDown(() => bridge.close());

  test('a character split across chunks is intact', () async {
    final session = TerminalSession(client, agentId: 'a-1');
    final received = <String>[];
    session.output.listen(received.add);
    await session.open();
    expect(session.isOpen, isTrue);

    // "é" is two UTF-8 bytes, split across two chunks: a decoder per chunk
    // would corrupt it.
    final bytes = utf8.encode('é');
    expect(bytes, hasLength(2));
    String frame(List<int> chunk) =>
        'data: ${jsonEncode({"data": base64Encode(chunk)})}\n\n';
    events.write(frame(bytes.sublist(0, 1)));
    await settle();
    events.write(frame(bytes.sublist(1)));
    await settle();

    expect(received.join(), 'é');
    await session.close();
  });

  test('typing pauses the agent, and hand back resumes it', () async {
    // The idle wait is held open here, so only the explicit hand back can
    // resume the agent.
    final idle = Completer<void>();
    final session = TerminalSession(
      client,
      agentId: 'a-1',
      wait: (_) => idle.future,
    );
    await session.open();
    expect(session.agentPaused, isFalse);

    session.input('ls\n');
    expect(session.agentPaused, isTrue);
    await settle();

    final input = bridge.requests.singleWhere(
      (r) => r.uri == '/api/agents/a-1/terminal/t-1/input',
    );
    expect(
      (jsonDecode(input.body) as Map<String, dynamic>)['data'],
      base64Encode(utf8.encode('ls\n')),
    );

    await session.handBack();
    expect(session.agentPaused, isFalse);
    expect(
      bridge.requests.where(
        (r) => r.uri == '/api/agents/a-1/terminal/t-1/release',
      ),
      hasLength(1),
    );
    idle.complete();
    await session.close();
  });

  test('two minutes idle hands back', () async {
    final session = TerminalSession(client, agentId: 'a-1', wait: (_) async {});
    await session.open();

    session.input('x');
    expect(session.agentPaused, isTrue);

    // The injected wait *is* the two idle minutes: when it returns with no
    // newer keystroke, the agent is handed back.
    await settle();
    expect(session.agentPaused, isFalse);
    expect(
      bridge.requests.where(
        (r) => r.uri == '/api/agents/a-1/terminal/t-1/release',
      ),
      hasLength(1),
    );
    await session.close();
  });

  test('resize posts the size', () async {
    final session = TerminalSession(client, agentId: 'a-1');
    await session.open(cols: 100, rows: 30);

    final opened = bridge.requests.singleWhere(
      (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/terminal',
    );
    final body = jsonDecode(opened.body) as Map<String, dynamic>;
    expect(body['cols'], 100);
    expect(body['rows'], 30);

    session.resize(120, 32);
    await settle();
    final resize = bridge.requests.singleWhere(
      (r) => r.uri == '/api/agents/a-1/terminal/t-1/resize',
    );
    final size = jsonDecode(resize.body) as Map<String, dynamic>;
    expect(size['cols'], 120);
    expect(size['rows'], 32);
    await session.close();
  });
}
