import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart'
    show ProviderKey, RoutingUpdate;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/models_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/models_section.dart';

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

/// Settings' Models and routing section (F7 decision 7): a routing save goes
/// to the user's global config, and a provider's key is never read back.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  /// A global config with one provider, one preset and two roles.
  Map<String, dynamic> configBody() => {
    'providers': {
      'anthropic': {
        'type': 'anthropic',
        'baseUrl': 'https://api.anthropic.com',
        'toolCalling': true,
        'hasKey': false,
        'keySource': 'none',
      },
    },
    'presets': {
      'local/qwen': {'provider': 'local', 'model': 'qwen3-coder'},
    },
    'profiles': {
      'default': {
        'reviewer': {'preset': 'local/qwen'},
      },
    },
    'customAgents': <String>[],
    'defaultProfile': 'default',
    'activePreset': 'local/qwen',
    'roles': ['implementer', 'reviewer'],
    'budgets': {
      'dailyUsd': 5,
      'perAgentUsd': 2,
      'onDailyCap': 'warn',
      'onAgentCap': 'block',
    },
  };

  testWidgets('routing saves to the global config', (tester) async {
    final transport = _Scripted({
      'GET /api/models': Response(status: 200, body: jsonEncode(configBody())),
      'PUT /api/models/routing': const Response(status: 200, body: '{}'),
    });
    final model = ModelsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, ModelsSection(model: model));
    expect(find.text('Saved to your global config'), findsOneWidget);

    await tester.tap(find.text('implementer'));
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(
        of: find.byType(Sheet),
        matching: find.text('local/qwen'),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    final puts = [
      for (final request in transport.requests)
        if (request.method == 'PUT' && request.path == '/api/models/routing')
          request,
    ];
    expect(puts, hasLength(1));
    final body = puts.single.body! as RoutingUpdate;
    expect(body.profiles!['default']!['implementer']!.preset, 'local/qwen');
    expect(model.items.first.value, 'local/qwen');
  });

  testWidgets('a key cannot be read back', (tester) async {
    const key = 'sk-live-never-shown';
    final transport = _Scripted({
      'GET /api/models': Response(status: 200, body: jsonEncode(configBody())),
      'PUT /api/models/providers/anthropic/key': const Response(
        status: 200,
        body: '{}',
      ),
    });
    final model = ModelsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, ModelsSection(model: model));
    await tester.tap(find.text('anthropic key'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText), key);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    final puts = [
      for (final request in transport.requests)
        if (request.path == '/api/models/providers/anthropic/key') request,
    ];
    expect(puts, hasLength(1));
    expect((puts.single.body! as ProviderKey).key, key);

    // Nothing on screen, and nothing the model holds, is the key again.
    expect(find.textContaining(key), findsNothing);
    expect(find.text('A key is stored'), findsOneWidget);
    for (final row in model.items) {
      expect(row.value.contains(key), isFalse);
      expect(row.description.contains(key), isFalse);
    }
  });
}
