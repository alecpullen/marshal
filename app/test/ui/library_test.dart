import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart'
    show MemoryPromote, PutGrantRequest, SkillConfirm;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/clients_model.dart';
import 'package:marshal_app/src/model/library_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/library/mcp_tab.dart';
import 'package:marshal_app/src/ui/library/memory_tab.dart';
import 'package:marshal_app/src/ui/library/skills_tab.dart';

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

/// The Library sections (F7 decision 5): a skill is previewed before it is
/// confirmed, a promotion is the person's approval, a client's token is shown
/// once, and a grant is saved per project.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1000, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('a skill is previewed before it is confirmed', (tester) async {
    final transport = _Scripted({
      'GET /api/library/skills': const Response(
        status: 200,
        body: '{"skills":[]}',
      ),
      'POST /api/library/skills/preview': Response(
        status: 200,
        body: jsonEncode({
          'stagingToken': 'sk-1',
          'name': 'recovery',
          'description': 'How this project recovers a lost session',
          'risk': 'low',
          'source': 'github.com/acme/skills/recovery',
        }),
      ),
      'POST /api/library/skills/confirm': const Response(
        status: 200,
        body: '{}',
      ),
    });
    final model = SkillsModel(BridgeClient(transport), scope: 'global');
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, SkillsTab(model: model));
    await tester.tap(find.text('Add a skill'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(EditableText).first,
      'github.com/acme/skills/recovery',
    );
    await tester.tap(find.text('Preview'));
    await tester.pumpAndSettle();

    // The preview shows what would be installed, and installs nothing.
    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' &&
            request.path == '/api/library/skills/preview',
      ),
      hasLength(1),
    );
    expect(
      transport.requests.where(
        (request) => request.path == '/api/library/skills/confirm',
      ),
      isEmpty,
    );
    expect(
      find.text('How this project recovers a lost session'),
      findsOneWidget,
    );

    await tester.tap(find.text('Confirm'));
    await tester.pumpAndSettle();
    final confirms = [
      for (final request in transport.requests)
        if (request.method == 'POST' &&
            request.path == '/api/library/skills/confirm')
          request,
    ];
    expect(confirms, hasLength(1));
    expect((confirms.single.body! as SkillConfirm).stagingToken, 'sk-1');
  });

  testWidgets('promotion needs approval', (tester) async {
    final transport = _Scripted({
      'GET /api/library/memory/suggestions': Response(
        status: 200,
        body: jsonEncode({
          'suggestions': [
            {
              'memoryId': 7,
              'matchProjectRoot': '/home/u/other',
              'suggestedScope': 'global',
            },
          ],
        }),
      ),
      'GET /api/library/memory': Response(
        status: 200,
        body: jsonEncode({
          'entries': [
            {
              'id': 7,
              'kind': 'fact',
              'content': 'The gate runs on Go 1.24',
              'confidence': 'confirmed',
              'createdAt': '2026-11-17T03:00:00Z',
              'updatedAt': '2026-11-17T03:05:00Z',
              'scope': 'project',
            },
          ],
        }),
      ),
      'POST /api/library/memory/7/promote': const Response(
        status: 200,
        body: '{}',
      ),
    });
    final model = MemoryModel(
      BridgeClient(transport),
      project: '/home/u/marshal',
    );
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, MemoryTab(model: model));
    expect(find.text('SUGGESTED PROMOTIONS'), findsOneWidget);
    await tester.tap(find.text('Promote'));
    await tester.pumpAndSettle();

    // The question names what widens, and nothing is promoted until it is
    // answered.
    expect(find.text('Promote this memory to global?'), findsOneWidget);
    expect(
      transport.requests.where((request) => request.method == 'POST'),
      isEmpty,
    );

    await tester.tap(find.text('Promote it'));
    await tester.pumpAndSettle();
    final promotions = [
      for (final request in transport.requests)
        if (request.method == 'POST') request,
    ];
    expect(promotions, hasLength(1));
    expect(promotions.single.path, '/api/library/memory/7/promote');
    expect((promotions.single.body! as MemoryPromote).scope, 'global');
  });

  testWidgets('a client token is shown once', (tester) async {
    final transport = _Scripted({
      'GET /api/clients': Response(
        status: 200,
        body: jsonEncode([
          {
            'id': 'c-1',
            'name': 'editor',
            'ownerId': 'owner-1',
            'createdAt': '2026-11-17T03:00:00Z',
          },
        ]),
      ),
      'POST /api/clients': const Response(
        status: 201,
        body: '{"id":"c-2","name":"ci","token":"mcp_2f9d","autonomous":false}',
      ),
    });
    final model = ClientsModel(
      BridgeClient(transport),
      projects: const ['/home/u/marshal'],
    );
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, McpTab(model: model));
    await tester.tap(find.text('New client'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText).first, 'ci');
    await tester.tap(find.text('Create'));
    await tester.pumpAndSettle();

    expect(
      transport.requests.where(
        (request) => request.method == 'POST' && request.path == '/api/clients',
      ),
      hasLength(1),
    );
    expect(find.textContaining('mcp_2f9d'), findsOneWidget);

    // Closing the detail loses the token for good: nothing reads it back.
    await tester.tap(find.text('Close'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('editor'));
    await tester.pumpAndSettle();
    expect(find.textContaining('mcp_2f9d'), findsNothing);
  });

  testWidgets('a grant is saved per project', (tester) async {
    Map<String, Object?> grantJson({bool read = false}) => {
      'clientId': 'c-1',
      'projectId': '/home/u/marshal',
      'grant': {
        'projectId': '/home/u/marshal',
        'read': read,
        'createDraft': false,
        'editDraft': false,
        'answerQuestion': false,
        'approveAndQueue': false,
        'approvalAuthority': 'none',
      },
      'version': 'v1',
    };
    final transport = _Scripted({
      'GET /api/clients': Response(
        status: 200,
        body: jsonEncode([
          {
            'id': 'c-1',
            'name': 'editor',
            'ownerId': 'owner-1',
            'createdAt': '2026-11-17T03:00:00Z',
          },
        ]),
      ),
      'GET /api/clients/c-1/factory-grants/%2Fhome%2Fu%2Fmarshal': Response(
        status: 200,
        body: jsonEncode(grantJson()),
      ),
      'PUT /api/clients/c-1/factory-grants/%2Fhome%2Fu%2Fmarshal': Response(
        status: 200,
        body: jsonEncode(grantJson(read: true)),
      ),
    });
    final model = ClientsModel(
      BridgeClient(transport),
      projects: const ['/home/u/marshal'],
    );
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, McpTab(model: model));
    await tester.tap(find.text('editor'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('/home/u/marshal'));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Read'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save the grant'));
    await tester.pumpAndSettle();

    final writes = [
      for (final request in transport.requests)
        if (request.method == 'PUT') request,
    ];
    expect(writes, hasLength(1));
    expect(
      writes.single.path,
      '/api/clients/c-1/factory-grants/%2Fhome%2Fu%2Fmarshal',
    );
    final body = writes.single.body! as PutGrantRequest;
    expect(body.version, 'v1');
    expect(body.grant.read, isTrue);
    expect(find.textContaining('Saved for /home/u/marshal'), findsOneWidget);
  });
}
