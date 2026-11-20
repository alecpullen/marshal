import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/files_model.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

FakeReply _json(Object body) => FakeReply(
  200,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);

void main() {
  late FakeBridge bridge;
  late BridgeClient client;

  setUp(() async {
    // flutter_test's binding answers every HttpClient with 400; the fake
    // bridge is on this machine, so the real client is wanted back.
    HttpOverrides.global = null;
    final box = await socketBridge();
    bridge = box.bridge;
    client = BridgeClient(box.transport);
    bridge
      ..reply(
        'GET',
        '/api/agents/a-1/files',
        _json(
          const FileList(
            root: '/home/u/marshal',
            path: '',
            entries: [
              FileEntry(name: 'internal', dir: true, size: 0),
              FileEntry(name: 'go.mod', dir: false, size: 412),
            ],
          ).toJson(),
        ),
      )
      ..reply(
        'GET',
        '/api/agents/a-1/file',
        _json(
          const FileContent(
            path: 'go.mod',
            size: 5,
            binary: false,
            truncated: false,
            content: 'hello',
          ).toJson(),
        ),
      );
  });

  tearDown(() => bridge.close());

  test('expands lazily: nothing is read until a directory is opened', () async {
    final model = FilesModel(client, agentId: 'a-1');
    expect(model.children(''), isEmpty);
    expect(model.expanded(''), isFalse);
    expect(bridge.requests, isEmpty);

    await model.expand('');

    expect(model.expanded(''), isTrue);
    expect(model.children('').map((e) => e.name), ['internal', 'go.mod']);
    expect(model.root, '/home/u/marshal');
    expect(bridge.requests, hasLength(1));

    // A second expand is free, and a child directory is its own read.
    await model.expand('');
    expect(bridge.requests, hasLength(1));

    await model.toggle('internal');
    expect(model.expanded('internal'), isTrue);
    // The fake bridge answers the root list for any path; what matters is
    // that the child directory was asked for, exactly once.
    expect(
      bridge.requests.where((r) => r.uri.contains('path=internal')),
      hasLength(1),
    );
  });

  test('a collapsed directory is not read again when reopened', () async {
    final model = FilesModel(client, agentId: 'a-1');
    await model.expand('internal');
    await model.toggle('internal');
    expect(model.expanded('internal'), isFalse);
    await model.toggle('internal');
    expect(model.expanded('internal'), isTrue);
    expect(
      bridge.requests.where((r) => r.uri.contains('path=internal')),
      hasLength(1),
    );
  });

  test('marks the files this thread changed', () {
    final model = FilesModel.seeded(changed: const ['internal/acp/stack.go']);
    expect(model.changed('internal/acp/stack.go'), isTrue);
    expect(model.changed('internal/acp/stack_test.go'), isFalse);

    model.setChanged(const ['go.mod']);
    expect(model.changed('internal/acp/stack.go'), isFalse);
    expect(model.changed('go.mod'), isTrue);
  });

  test('a path outside the workspace is refused', () async {
    final model = FilesModel(client, agentId: 'a-1');

    await model.expand('../secrets');

    expect(model.children('../secrets'), isEmpty);
    expect(model.expanded('../secrets'), isFalse);
    expect(model.errorFor('../secrets'), 'That path is outside the workspace.');
    // It was refused here: the bridge was never asked.
    expect(bridge.requests, isEmpty);

    // An absolute path is refused too.
    await model.expand('/etc/passwd');
    expect(model.errorFor('/etc/passwd'), isNotNull);
    expect(bridge.requests, isEmpty);

    // And a read of one is refused, with the words rather than a file.
    final file = await model.open('../secrets');
    expect(file.text, 'That path is outside the workspace.');
    expect(bridge.requests, isEmpty);
  });

  test('opens a file', () async {
    final model = FilesModel(client, agentId: 'a-1');
    final file = await model.open('go.mod');
    expect(file.path, 'go.mod');
    expect(file.text, 'hello');
    expect(file.binary, isFalse);
    expect(file.truncated, isFalse);
  });
}
