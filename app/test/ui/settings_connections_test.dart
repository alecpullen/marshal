import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/connections_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/connections_section.dart';

/// A transport that answers canned routes and records what was asked.
class _Scripted implements Transport {
  _Scripted(this._replies);

  final Map<String, Response> _replies;
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return _replies['${request.method} ${request.path}'] ??
        Response(status: 404, body: jsonEncode({'error': 'no canned route'}));
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// Settings' Connections section (F7 decision 7): what a forge credential
/// permits, and what a provider answers when probed.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  /// A configuration with one provider and no presets.
  Map<String, dynamic> modelsBody() => {
    'providers': {
      'local': {
        'type': 'ollama',
        'baseUrl': 'http://127.0.0.1:11434',
        'toolCalling': true,
        'hasKey': false,
        'keySource': 'none',
      },
    },
    'presets': <String, dynamic>{},
    'profiles': <String, dynamic>{},
    'customAgents': <String>[],
    'defaultProfile': 'default',
    'activePreset': '',
    'roles': ['implementer'],
    'budgets': {
      'dailyUsd': 0,
      'perAgentUsd': 0,
      'onDailyCap': 'warn',
      'onAgentCap': 'warn',
    },
  };

  testWidgets('an ssh credential states its limits', (tester) async {
    final transport = _Scripted({
      'GET /api/repos': Response(
        status: 200,
        body: jsonEncode([
          {
            'id': 'acme-widgets',
            'url': 'github.com/acme/widgets',
            'branch': 'main',
            'credRef': 'deploy',
            'ownerId': 'owner-1',
            'forge': 'github',
          },
        ]),
      ),
      'GET /api/credentials': Response(
        status: 200,
        body: jsonEncode([
          {
            'id': 'deploy',
            'kind': 'ssh',
            'keyPath': '/home/u/.ssh/id_ed25519',
            'set': true,
          },
        ]),
      ),
      'GET /api/models': Response(status: 200, body: jsonEncode(modelsBody())),
    });
    final model = ConnectionsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, ConnectionsSection(model: model));

    // The row says what an ssh key cannot do, without opening anything.
    expect(
      find.text('Push only — cannot open pull requests or read checks'),
      findsOneWidget,
    );

    await tester.tap(find.text('acme-widgets'));
    await tester.pumpAndSettle();

    expect(find.text('Can push to the repository'), findsOneWidget);
    expect(find.text('Cannot open pull requests'), findsOneWidget);
    expect(find.text('Cannot read checks'), findsOneWidget);
  });

  testWidgets('probe reports health', (tester) async {
    final transport = _Scripted({
      'GET /api/repos': const Response(status: 200, body: '[]'),
      'GET /api/credentials': const Response(status: 200, body: '[]'),
      'GET /api/models': Response(status: 200, body: jsonEncode(modelsBody())),
      'POST /api/models/probe': Response(
        status: 200,
        body: jsonEncode({
          'models': [
            {'id': 'qwen3-coder', 'contextWindow': 131072},
            {'id': 'llama3.3'},
          ],
        }),
      ),
    });
    final model = ConnectionsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, ConnectionsSection(model: model));
    expect(find.text('No key · not probed'), findsOneWidget);

    await tester.tap(find.text('local'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Probe'));
    await tester.pumpAndSettle();

    // The probe asked the bridge, and its answer is in the detail and in the
    // row's health.
    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' && request.path == '/api/models/probe',
      ),
      hasLength(1),
    );
    expect(find.text('2 models'), findsOneWidget);
    expect(find.textContaining('qwen3-coder'), findsOneWidget);
    expect(model.items.single.value, 'No key · 2 models');
  });

  group('connecting', () {
    Future<(_Bridge, ConnectionsModel)> open(
      WidgetTester tester, {
      _Bridge? bridge,
    }) async {
      final fake = bridge ?? _Bridge();
      final model = ConnectionsModel(BridgeClient(fake));
      addTearDown(model.dispose);
      await model.load();
      await pump(tester, ConnectionsSection(model: model));
      return (fake, model);
    }

    testWidgets('a repository with no credential says it cannot push', (
      tester,
    ) async {
      final bridge = _Bridge()
        ..repos.add({
          'id': 'acme-widgets',
          'url': 'github.com/acme/widgets',
          'ownerId': 'o',
          'forge': 'github',
        });
      await open(tester, bridge: bridge);

      expect(find.text('No credential — cannot push'), findsOneWidget);
      expect(
        find.text('1 connection needs you. Open it to fix it.'),
        findsOneWidget,
      );
      expect(find.text('REPOSITORIES'), findsOneWidget);
    });

    testWidgets('connecting a repository registers it and lists it', (
      tester,
    ) async {
      final (bridge, _) = await open(tester);
      await tester.tap(find.text('Connect a repository').first);
      await tester.pumpAndSettle();

      await tester.enterText(
        find.byType(EditableText).first,
        'https://github.com/acme/widgets.git',
      );
      await tester.pump();
      await tester.tap(find.text('Connect the repository'));
      await tester.pumpAndSettle();

      final posted = bridge.sent('POST', '/api/repos').single;
      expect(posted['id'], 'acme-widgets');
      expect(posted['forge'], 'github');
      // The sheet closed and the row is there, saying what is missing.
      expect(find.text('Connect the repository'), findsNothing);
      expect(find.text('acme-widgets'), findsOneWidget);
      expect(find.text('No credential — cannot push'), findsOneWidget);
    });

    testWidgets('a pasted token is stored, then the repository uses it', (
      tester,
    ) async {
      final bridge = _Bridge()
        ..repos.add({
          'id': 'acme-widgets',
          'url': 'github.com/acme/widgets',
          'ownerId': 'o',
          'forge': 'github',
          'branch': 'main',
        });
      await open(tester, bridge: bridge);

      await tester.tap(find.text('acme-widgets'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Choose a credential'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('A new credential'));
      await tester.pumpAndSettle();

      await tester.enterText(find.widgetWithText(Field, 'Token'), 'ghp_secret');
      await tester.pump();
      await tester.tap(find.text('Save credential'));
      await tester.pumpAndSettle();

      // The secret first, so the credential never points at nothing.
      expect(bridge.order, [
        'PUT /api/secrets/git/acme-widgets-token',
        'POST /api/credentials',
        'POST /api/repos',
      ]);
      expect(bridge.sent('PUT', '/api/secrets/git/acme-widgets-token').single, {
        'value': 'ghp_secret',
      });
      final credential = bridge.sent('POST', '/api/credentials').single;
      expect(credential['kind'], 'vault');
      expect(credential['ref'], 'vault:git/acme-widgets-token');
      // The repo was registered again whole: its branch survived.
      final repo = bridge.sent('POST', '/api/repos').single;
      expect(repo['credRef'], 'acme-widgets-token');
      expect(repo['branch'], 'main');
      // The detail reads what is true now, and the row no longer needs you.
      expect(find.text('Change credential'), findsOneWidget);
      expect(find.textContaining('needs you'), findsNothing);
    });

    testWidgets('the bridge\'s refusal stays with what was typed', (
      tester,
    ) async {
      final bridge = _Bridge()..refuse = 'id must be 1-64 letters';
      await open(tester, bridge: bridge);
      await tester.tap(find.text('Connect a repository').first);
      await tester.pumpAndSettle();
      await tester.enterText(find.byType(EditableText).first, 'github.com/a/b');
      await tester.pump();
      await tester.tap(find.text('Connect the repository'));
      await tester.pumpAndSettle();

      expect(find.textContaining('id must be 1-64 letters'), findsOneWidget);
      // Still open, still holding the URL.
      expect(find.text('Connect the repository'), findsOneWidget);
      expect(find.text('github.com/a/b'), findsOneWidget);
    });

    testWidgets('removing a repository asks first, naming what is lost', (
      tester,
    ) async {
      final bridge = _Bridge()
        ..repos.add({
          'id': 'acme-widgets',
          'url': 'github.com/acme/widgets',
          'ownerId': 'o',
        });
      await open(tester, bridge: bridge);
      await tester.tap(find.text('acme-widgets'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Remove repository'));
      await tester.pumpAndSettle();

      expect(bridge.sent('DELETE', '/api/repos/acme-widgets'), isEmpty);
      expect(find.text('Remove acme-widgets?'), findsOneWidget);

      await tester.tap(find.text('Remove the repository'));
      await tester.pumpAndSettle();

      expect(bridge.sent('DELETE', '/api/repos/acme-widgets'), hasLength(1));
      expect(find.text('acme-widgets'), findsNothing);
    });

    testWidgets('the forms fit a phone at twice the text size', (tester) async {
      final bridge = _Bridge()
        ..repos.add({
          'id': 'acme-widgets',
          'url': 'github.com/acme/widgets',
          'ownerId': 'o',
        })
        ..providers['local'] = {
          'type': 'ollama',
          'baseUrl': 'http://127.0.0.1:11434',
          'toolCalling': true,
          'hasKey': false,
          'keySource': 'none',
        };
      final model = ConnectionsModel(BridgeClient(bridge));
      addTearDown(model.dispose);
      await model.load();
      tester.view.physicalSize = const Size(390, 800);
      tester.view.devicePixelRatio = 1;
      tester.platformDispatcher.textScaleFactorTestValue = 2;
      addTearDown(tester.view.reset);
      addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
      await tester.pumpWidget(
        EmberApp(
          brightness: Brightness.light,
          home: ConnectionsSection(model: model),
        ),
      );

      // The connect sheet, the inline credential form and the provider forms.
      Future<void> press(String text) async {
        await tester.ensureVisible(find.text(text).first);
        await tester.pump();
        await tester.tap(find.text(text).first);
        await tester.pumpAndSettle();
      }

      await press('Connect a repository');
      expect(find.text('Connect the repository'), findsOneWidget);
      expect(tester.takeException(), isNull);
      Navigator.of(tester.element(find.byType(EditableText).first)).pop();
      await tester.pumpAndSettle();

      await press('acme-widgets');
      await press('Choose a credential');
      await press('A new credential');
      expect(find.text('Save credential'), findsOneWidget);
      expect(tester.takeException(), isNull);
      Navigator.of(tester.element(find.text('Save credential'))).pop();
      await tester.pumpAndSettle();

      await press('local');
      await press('Set key');
      expect(find.text('Save key'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });

    testWidgets('adding a provider stores its key one way', (tester) async {
      final (bridge, _) = await open(tester);
      await tester.tap(find.text('Add a model provider'));
      await tester.pumpAndSettle();

      await tester.enterText(find.widgetWithText(Field, 'Name'), 'lmstudio');
      await tester.enterText(find.widgetWithText(Field, 'API key'), 'sk-1');
      await tester.pump();
      await tester.tap(find.text('Add the provider'));
      await tester.pumpAndSettle();

      final put = bridge.sent('PUT', '/api/models/providers').single;
      final provider = (put['providers'] as Map)['lmstudio'] as Map;
      expect(provider['type'], 'openai_compatible');
      expect(provider['baseUrl'], 'http://127.0.0.1:1234/v1');
      expect(bridge.sent('PUT', '/api/models/providers/lmstudio/key').single, {
        'key': 'sk-1',
      });
      expect(find.text('MODEL PROVIDERS'), findsOneWidget);
      expect(find.text('lmstudio'), findsOneWidget);
    });
  });
}

/// A bridge that keeps what it is told, so a write shows up in the next read.
class _Bridge implements Transport {
  final repos = <Map<String, dynamic>>[];
  final credentials = <Map<String, dynamic>>[];
  final providers = <String, Map<String, dynamic>>{};
  final order = <String>[];
  final _bodies = <String, List<Map<String, dynamic>>>{};

  /// When set, every write is refused with this message.
  String? refuse;

  List<Map<String, dynamic>> sent(String method, String path) =>
      _bodies['$method $path'] ?? const [];

  Response _json(Object body, [int status = 200]) =>
      Response(status: status, body: jsonEncode(body));

  @override
  Future<Response> send(Request request) async {
    final key = '${request.method} ${request.path}';
    final raw = request.body;
    final body = raw == null
        ? null
        : jsonDecode(jsonEncode(raw)) as Map<String, dynamic>;
    if (request.method != 'GET') {
      order.add(key);
      (_bodies[key] ??= []).add(body ?? {});
      final message = refuse;
      if (message != null) return _json({'error': message}, 400);
    }
    switch (key) {
      case 'GET /api/repos':
        return _json(repos);
      case 'GET /api/credentials':
        return _json(credentials);
      case 'GET /api/models':
        return _json({
          'providers': providers,
          'presets': <String, dynamic>{},
          'profiles': <String, dynamic>{},
          'customAgents': <String>[],
          'defaultProfile': 'default',
          'activePreset': '',
          'roles': <String>[],
          'budgets': {
            'dailyUsd': 0,
            'perAgentUsd': 0,
            'onDailyCap': 'warn',
            'onAgentCap': 'warn',
          },
        });
      case 'POST /api/repos':
        repos
          ..removeWhere((one) => one['id'] == body!['id'])
          ..add({...body!, 'ownerId': 'o'});
        return _json(repos.last);
      case 'DELETE /api/repos/acme-widgets':
        repos.removeWhere((one) => one['id'] == 'acme-widgets');
        return const Response(status: 204, body: '');
      case 'POST /api/credentials':
        credentials
          ..removeWhere((one) => one['id'] == body!['id'])
          ..add({...body!, 'set': true});
        return _json(credentials.last);
      case 'PUT /api/models/providers':
        (body!['providers'] as Map).forEach((name, value) {
          if (value == null) {
            providers.remove(name);
          } else {
            providers[name as String] = {
              ...(value as Map<String, dynamic>),
              'hasKey': false,
              'keySource': 'none',
            };
          }
        });
        return const Response(status: 204, body: '');
    }
    if (request.method == 'PUT') return const Response(status: 204, body: '');
    return _json({'error': 'no route $key'}, 404);
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}
