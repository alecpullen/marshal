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
}
