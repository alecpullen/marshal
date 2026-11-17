import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart' show SecretWrite;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/secrets_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/secrets_section.dart';

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

/// Settings' Secrets section (F7 decision 7): a value is written and never
/// rendered, because no route answers one back.
void main() {
  const ref = 'vault:forge/github-token';
  const value = 'ghp-never-rendered';

  testWidgets('a secret value is never rendered', (tester) async {
    final transport = _Scripted({
      'GET /api/secrets/status': const Response(
        status: 200,
        body: '{"backend":"local","healthy":true}',
      ),
      'GET /api/secrets': Response(
        status: 200,
        body: jsonEncode({
          'refs': [ref],
        }),
      ),
      'GET /api/credentials': const Response(status: 200, body: '[]'),
      'PUT /api/secrets/forge/github-token': const Response(
        status: 204,
        body: '',
      ),
    });
    final model = SecretsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: SecretsSection(model: model),
      ),
    );

    // The reference is listed with where it is used, and no value anywhere.
    expect(find.text(ref), findsOneWidget);
    expect(find.text('No credential names it'), findsOneWidget);
    expect(find.text('Value not readable'), findsOneWidget);

    await tester.tap(find.text(ref));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText), value);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    final puts = [
      for (final request in transport.requests)
        if (request.method == 'PUT') request,
    ];
    expect(puts, hasLength(1));
    expect(puts.single.path, '/api/secrets/forge/github-token');
    expect((puts.single.body! as SecretWrite).value, value);

    // Written, and never shown again: not on the row, not in a detail, not in
    // the model.
    expect(find.textContaining(value), findsNothing);
    expect(find.text('Written'), findsOneWidget);
    for (final row in model.items) {
      expect(row.value.contains(value), isFalse);
      expect(row.description.contains(value), isFalse);
    }
  });
}
