import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/model/new_project_model.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

FakeReply _json(Object body, [int status = 200]) => FakeReply(
  status,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);

Map<String, Object?> _creation(
  String state, {
  String kind = 'clone',
  String root = '/home/u/projects/marshal',
  String? progress,
  String? error,
}) => {
  'id': 'np-1',
  'kind': kind,
  'root': root,
  'state': state,
  'startedAt': 1764000000000,
  'progress': ?progress,
  'error': ?error,
};

void main() {
  late FakeBridge bridge;
  late BridgeClient client;

  setUp(() async {
    HttpOverrides.global = null;
    final box = await socketBridge();
    bridge = box.bridge;
    client = BridgeClient(box.transport);
    bridge.reply(
      'GET',
      '/api/projects/new',
      _json({'projectsDir': '/home/u/projects', 'creations': []}),
    );
  });

  tearDown(() => bridge.close());

  NewProjectModel modelFor({Map<String, BridgeClient>? clients}) =>
      NewProjectModel(
        clients: clients ?? {'local': client},
        host: 'local',
        poll: Duration.zero,
      );

  Map<String, Object?> posted() => jsonDecode(
    bridge.requests
        .lastWhere((r) => r.method == 'POST' && r.uri == '/api/projects/new')
        .body,
  ) as Map<String, Object?>;

  test('a URL names the folder', () {
    for (final (url, name) in [
      ('https://github.com/acme/marshal.git', 'marshal'),
      ('https://github.com/acme/marshal/', 'marshal'),
      ('git@github.com:acme/marshal.git', 'marshal'),
      ('ssh://git@host:2222/srv/tool.git?x=1', 'tool'),
      ('', ''),
    ]) {
      expect(NewProjectModel.nameFor(url), name, reason: url);
    }
  });

  test('the folder follows the projects folder and the name until it is '
      'edited', () async {
    final model = modelFor();
    await model.load();
    expect(model.projectsDir, '/home/u/projects');
    expect(model.problem, 'Name the project');

    model.selectWay(NewProjectWay.clone);
    model.setUrl('https://github.com/acme/marshal.git');
    expect(model.name, 'marshal');
    expect(model.path, '/home/u/projects/marshal');

    model.setName('marshal-fork');
    model.setUrl('https://github.com/acme/other.git');
    expect(model.name, 'marshal-fork', reason: 'an edited name stays');
    expect(model.path, '/home/u/projects/marshal-fork');

    model.setPath('/srv/code/m');
    model.setName('again');
    expect(model.path, '/srv/code/m', reason: 'an edited folder stays');
    expect(model.problem, isNull);
  });

  test('an existing folder is the person to finish', () async {
    final model = modelFor();
    await model.load();
    model.selectWay(NewProjectWay.existing);
    expect(model.path, '/home/u/projects/');
    expect(
      model.canCreate,
      isFalse,
      reason: 'the projects folder is not itself the project',
    );
    model.setPath('relative/dir');
    expect(model.problem, contains('full path'));
    model.setPath('/home/u/work/tool');
    expect(model.canCreate, isTrue);
  });

  test('a blank project is made at once', () async {
    bridge.reply(
      'POST',
      '/api/projects/new',
      _json(_creation('ready', kind: 'blank', root: '/home/u/projects/fresh')),
    );
    final model = modelFor();
    await model.load();
    model.setName('fresh');

    await model.create();

    expect(posted(), {'kind': 'blank', 'root': '/home/u/projects/fresh'});
    expect(model.created, '/home/u/projects/fresh');
    expect(model.busy, isFalse);
    expect(model.failure, isNull);
  });

  test('a clone is followed until it is ready, with git’s progress', () async {
    bridge
      ..reply('POST', '/api/projects/new', _json(_creation('cloning'), 202))
      ..replyOnce(
        'GET',
        '/api/projects/new/np-1',
        _json(_creation('cloning', progress: 'Receiving objects:  42%')),
      )
      ..reply('GET', '/api/projects/new/np-1', _json(_creation('ready')));
    final model = modelFor();
    await model.load();
    model
      ..selectWay(NewProjectWay.clone)
      ..setUrl('https://github.com/acme/marshal.git')
      ..setBranch('main');
    final progress = <String>[];
    model.addListener(() {
      final line = model.progress;
      if (line != null && !progress.contains(line)) progress.add(line);
    });

    await model.create();

    expect(posted(), {
      'kind': 'clone',
      'root': '/home/u/projects/marshal',
      'url': 'https://github.com/acme/marshal.git',
      'branch': 'main',
    });
    expect(progress, ['Receiving objects:  42%']);
    expect(model.created, '/home/u/projects/marshal');
    expect(model.busy, isFalse);
  });

  test('a failed clone says what git said', () async {
    bridge
      ..reply('POST', '/api/projects/new', _json(_creation('cloning'), 202))
      ..reply(
        'GET',
        '/api/projects/new/np-1',
        _json(
          _creation('failed', error: 'git clone: fatal: repository not found'),
        ),
      );
    final model = modelFor();
    await model.load();
    model
      ..selectWay(NewProjectWay.clone)
      ..setUrl('https://github.com/acme/gone.git');

    await model.create();

    expect(model.failure, 'git clone: fatal: repository not found');
    expect(model.created, isNull);
    expect(model.canCreate, isTrue, reason: 'it can be tried again');
  });

  test('a refusal is the bridge’s words, and what was typed stays', () async {
    bridge.reply(
      'POST',
      '/api/projects/new',
      _json({'error': '/home/u/projects/fresh already has files in it'}, 400),
    );
    final model = modelFor();
    await model.load();
    model.setName('fresh');

    await model.create();

    expect(model.failure, '/home/u/projects/fresh already has files in it');
    expect(model.name, 'fresh');
    expect(model.busy, isFalse);
  });

  test('another host is asked for its own projects folder', () async {
    final other = await socketBridge('other.sock');
    other.bridge.reply(
      'GET',
      '/api/projects/new',
      _json({'projectsDir': '/srv/code', 'creations': []}),
    );
    final model = modelFor(
      clients: {'local': client, 'strix': BridgeClient(other.transport)},
    );
    await model.load();
    model.setName('tool');
    expect(model.path, '/home/u/projects/tool');

    await model.selectHost('strix');

    expect(model.host, 'strix');
    expect(model.path, '/srv/code/tool');
  });
}
