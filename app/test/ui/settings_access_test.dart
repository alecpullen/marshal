import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart'
    show StatusLink, StatusLinkCreate;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/access_model.dart';
import 'package:marshal_app/src/model/notifications_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/access_section.dart';
import 'package:marshal_app/src/ui/settings/notifications_section.dart';

/// A transport that answers canned routes and records what was asked.
class _Scripted implements Transport {
  _Scripted(this.replies);

  final Map<String, Response> replies;
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return replies['${request.method} ${request.path}'] ??
        Response(status: 404, body: jsonEncode({'error': 'no canned route'}));
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// Settings' Notifications and Access sections (F7 decision 7): Send a test
/// posts, a revoke asks first, and Share makes a thread's status link.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1300, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('send a test posts', (tester) async {
    final transport = _Scripted({
      'GET /api/notifications': Response(
        status: 200,
        body: jsonEncode({
          'webhooks': [
            {
              'id': 'wh-1',
              'url': 'https://hooks.example.com/marshal',
              'events': ['needs_you'],
            },
          ],
        }),
      ),
      'POST /api/notifications/test': const Response(
        status: 202,
        body: '{"sent":1}',
      ),
    });
    final model = NotificationsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, NotificationsSection(model: model));
    expect(find.text('https://hooks.example.com/marshal'), findsOneWidget);

    await tester.tap(find.text('Send a test'));
    await tester.pumpAndSettle();

    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' &&
            request.path == '/api/notifications/test',
      ),
      hasLength(1),
    );
    expect(find.text('Sent to 1 webhook'), findsOneWidget);
  });

  testWidgets('revoke confirms', (tester) async {
    final transport = _Scripted({
      'GET /api/status-links': Response(
        status: 200,
        body: jsonEncode([
          {
            'id': 'sl-1',
            'agentId': 'a-1',
            'createdAt': '2026-11-17T03:00:00Z',
            'expiresAt': '2026-11-18T03:00:00Z',
          },
        ]),
      ),
      'DELETE /api/status-links/sl-1': const Response(
        status: 200,
        body: '{"status":"revoked"}',
      ),
    });
    final model = AccessModel(BridgeClient(transport), tokenHeld: true);
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, AccessSection(model: model));
    await tester.tap(find.text('a-1'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Revoke…'));
    await tester.pumpAndSettle();

    // Nothing happens until the revocation is confirmed.
    expect(
      transport.requests.where((request) => request.method == 'DELETE'),
      isEmpty,
    );
    expect(find.textContaining('stops working at once'), findsOneWidget);

    await tester.tap(
      find.descendant(
        of: find.byType(Sheet).last,
        matching: find.text('Revoke'),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      transport.requests.where(
        (request) =>
            request.method == 'DELETE' &&
            request.path == '/api/status-links/sl-1',
      ),
      hasLength(1),
    );
  });

  testWidgets('share creates a link for the thread', (tester) async {
    final transport = _Scripted({
      'GET /api/status-links': const Response(status: 200, body: '[]'),
      'POST /api/status-links': Response(
        status: 201,
        body: jsonEncode({
          'id': 'sl-2',
          'url': '/status/sl-2.token',
          'expiresAt': '2026-11-18T03:00:00Z',
        }),
      ),
    });
    final model = AccessModel(BridgeClient(transport));
    addTearDown(model.dispose);

    final made = await model.share('a-1', hours: 24);

    final posts = [
      for (final request in transport.requests)
        if (request.method == 'POST' && request.path == '/api/status-links')
          request,
    ];
    expect(posts, hasLength(1));
    final body = posts.single.body! as StatusLinkCreate;
    expect(body.agentID, 'a-1');
    expect(body.ttlHours, 24);
    expect(made.url, '/status/sl-2.token');
    expect(model.made?.id, 'sl-2');
  });

  test('a listed link never carries its token', () {
    const link = StatusLink(
      id: 'sl-1',
      agentID: 'a-1',
      createdAt: '2026-11-17T03:00:00Z',
      expiresAt: '2026-11-18T03:00:00Z',
    );
    expect(link.toJson().keys, isNot(contains('url')));
    expect(link.toJson().keys, isNot(contains('tokenHash')));
  });
}
