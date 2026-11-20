import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/model/terminal_session.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

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
    final box = await socketBridge();
    bridge = box.bridge;
    client = BridgeClient(box.transport);
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

  test('the bridge’s reason for a shell that never started is kept', () async {
    final session = TerminalSession(client, agentId: 'a-1');
    await session.open();

    // The bridge attaches the failure to the chunk that ends the stream
    // (contract.TerminalChunk.Error): without reading it a shell that never
    // started ends silently.
    events.write(
      'data: ${jsonEncode({'error': 'the shell could not be started: no pty'})}\n\n',
    );
    await settle();

    expect(session.error, 'the shell could not be started: no pty');
    expect(session.running, isFalse);
    await session.close();
  });

  test('a shell that exits cleanly has no error', () async {
    final session = TerminalSession(client, agentId: 'a-1');
    await session.open();

    events.write('data: ${jsonEncode({'exit': 0})}\n\n');
    await settle();

    expect(session.error, isNull);
    expect(session.exit, '0');
    expect(session.running, isFalse);
    await session.close();
  });

  test('reopen asks for a fresh shell after one failed', () async {
    final session = TerminalSession(client, agentId: 'a-1');
    await session.open();
    events.write(
      'data: ${jsonEncode({'error': 'the shell could not be started'})}\n\n',
    );
    await settle();
    expect(session.error, isNotNull);

    await session.reopen();

    expect(session.error, isNull);
    expect(session.isOpen, isTrue);
    expect(
      bridge.requests.where(
        (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/terminal',
      ),
      hasLength(2),
    );
    await session.close();
  });

  test('keys typed before the shell has an id are sent after it', () async {
    // The terminal route answers slowly, so the id arrives after the typing.
    bridge.reply(
      'POST',
      '/api/agents/a-1/terminal',
      FakeReply(
        200,
        headers: const {'content-type': 'application/json'},
        body: jsonEncode({'terminalId': 't-1'}),
        delay: const Duration(milliseconds: 40),
      ),
    );
    final idle = Completer<void>();
    final session = TerminalSession(
      client,
      agentId: 'a-1',
      wait: (_) => idle.future,
    );

    final opening = session.open();
    session.input('l');
    session.input('s');
    // Nothing is posted yet: the shell has no id to post to.
    expect(bridge.requests.where((r) => r.uri.endsWith('/t-1/input')), isEmpty);

    await opening;
    await settle();

    // Both keystrokes arrive, in the order they were typed, once the id does.
    final bodies = [
      for (final request in bridge.requests)
        if (request.uri.endsWith('/t-1/input'))
          (jsonDecode(request.body) as Map<String, dynamic>)['data'] as String,
    ];
    expect(bodies, [
      base64Encode(utf8.encode('l')),
      base64Encode(utf8.encode('s')),
    ]);

    // The hold began on the first keystroke, before the shell was open.
    expect(session.agentPaused, isTrue);
    idle.complete();
    await session.close();
  });

  test('one keystroke body is in flight at a time, in order', () async {
    // A slow answer to the first input means the second can only be posted
    // after it: two bodies must never race to the pty.
    bridge.reply(
      'POST',
      '/api/agents/a-1/terminal/t-1/input',
      const FakeReply(204, delay: Duration(milliseconds: 60)),
    );
    final idle = Completer<void>();
    final session = TerminalSession(
      client,
      agentId: 'a-1',
      wait: (_) => idle.future,
    );
    await session.open();

    session.input('a');
    session.input('b');
    // While the first is still unanswered, the second has not been posted.
    await Future<void>.delayed(const Duration(milliseconds: 20));
    expect(
      bridge.requests.where(
        (r) => r.uri == '/api/agents/a-1/terminal/t-1/input',
      ),
      hasLength(1),
    );

    await settle();
    final bodies = [
      for (final request in bridge.requests)
        if (request.uri == '/api/agents/a-1/terminal/t-1/input')
          (jsonDecode(request.body) as Map<String, dynamic>)['data'] as String,
    ];
    expect(bodies, [
      base64Encode(utf8.encode('a')),
      base64Encode(utf8.encode('b')),
    ]);
    idle.complete();
    await session.close();
  });

  test('a large paste is sent in pieces of at most 16 KiB', () async {
    final idle = Completer<void>();
    final session = TerminalSession(
      client,
      agentId: 'a-1',
      wait: (_) => idle.future,
    );
    await session.open();

    final paste = 'a' * (TerminalSession.inputChunk * 2 + 100);
    session.input(paste);
    await settle();

    final bodies = [
      for (final request in bridge.requests)
        if (request.uri == '/api/agents/a-1/terminal/t-1/input')
          utf8.decode(
            base64Decode(
              (jsonDecode(request.body) as Map<String, dynamic>)['data']
                  as String,
            ),
          ),
    ];
    expect(bodies, hasLength(3));
    expect(bodies.every((b) => b.length <= TerminalSession.inputChunk), isTrue);
    expect(bodies.join(), paste);
    idle.complete();
    await session.close();
  });

  test('chunking never splits a character', () {
    // A two-byte character straddling the 16 KiB boundary would be corrupted
    // by a byte-wise cut; the pieces are cut on character boundaries.
    final text = '${'a' * (TerminalSession.inputChunk - 1)}é';
    final pieces = TerminalSession.chunks(text);
    expect(pieces, hasLength(2));
    expect(pieces.first, 'a' * (TerminalSession.inputChunk - 1));
    expect(pieces.last, 'é');
    expect(pieces.join(), text);
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
