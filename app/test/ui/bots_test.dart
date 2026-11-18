import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/bots_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as inbox;
import 'package:marshal_app/src/ui/automations/bots_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../model/support.dart';

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

/// The Bots section of Automations (F7 decision 4): what Post, Send to the
/// author and Discard post, that Discard confirms first, that the CI fixer's
/// rule is text and not a setting, and that a held draft is an ask.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  Map<String, Object?> draftJson({String status = 'draft'}) => {
    'id': 'd-1',
    'repoId': 'acme/widgets',
    'number': 42,
    'title': 'Make the scan report a file it cannot read',
    'headSha': '9f1ad475',
    'findings': [
      {
        'id': 'f-1',
        'severity': 'blocking',
        'path': 'internal/repo/scan.go',
        'line': 42,
        'title': 'The scan drops a file it cannot read',
        'body': 'A permission error ends the walk.',
      },
    ],
    'summary': 'One blocking finding.',
    'status': status,
    'createdAt': '2026-11-17T03:00:00Z',
  };

  Map<String, Object?> settingsJson() => {
    'automations': {
      'reviewBot': {
        'enabled': true,
        'repoId': 'acme/widgets',
        'skipDrafts': true,
        'autoPost': false,
      },
      'ciFixer': {
        'enabled': true,
        'repoId': 'acme/widgets',
        'branches': ['main'],
        'push': false,
      },
    },
  };

  List<Object?> historyJson() => [
    {
      'id': 'c-1',
      'repoId': 'acme/widgets',
      'branch': 'main',
      'sha': '9f1ad475',
      'check': 'go',
      'status': 'fixed',
      'reason': 'a nil dereference in the parser',
      'costUsd': 0.42,
      'createdAt': '2026-11-17T03:10:00Z',
    },
  ];

  _Scripted transportFor({Response? post, Response? send, Response? discard}) =>
      _Scripted({
        'GET /api/projects/settings': Response(
          status: 200,
          body: jsonEncode(settingsJson()),
        ),
        'GET /api/automations/review/drafts': Response(
          status: 200,
          body: jsonEncode([draftJson()]),
        ),
        'GET /api/automations/ci/history': Response(
          status: 200,
          body: jsonEncode(historyJson()),
        ),
        'POST /api/automations/review/drafts/d-1/post': ?post,
        'POST /api/automations/review/drafts/d-1/send-to-author': ?send,
        'POST /api/automations/review/drafts/d-1/discard': ?discard,
      });

  Future<BotsModel> modelFor(
    _Scripted transport, {
    void Function()? onWrote,
  }) async {
    final model = BotsModel(
      BridgeClient(transport),
      project: '/home/u/marshal',
      now: now,
      onWrote: onWrote,
    );
    addTearDown(model.dispose);
    await model.load();
    return model;
  }

  Future<void> pump(WidgetTester tester, BotsModel model) async {
    tester.view.physicalSize = const Size(1000, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: BotsTab(model: model),
      ),
    );
  }

  /// Opens the review bot's detail and then its held draft.
  Future<void> openDraft(WidgetTester tester) async {
    await tester.tap(find.text('The review bot'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Make the scan report a file it cannot read'));
    await tester.pumpAndSettle();
  }

  testWidgets('post posts the draft', (tester) async {
    final transport = transportFor(
      post: Response(
        status: 200,
        body: jsonEncode(draftJson(status: 'posted')),
      ),
    );
    final model = await modelFor(transport);
    await pump(tester, model);
    await openDraft(tester);

    await tester.tap(find.text('Post'));
    await tester.pumpAndSettle();

    final posts = [
      for (final request in transport.requests)
        if (request.method == 'POST' &&
            request.path == '/api/automations/review/drafts/d-1/post')
          request,
    ];
    expect(posts, hasLength(1));
    expect(find.textContaining('Posted'), findsOneWidget);
  });

  testWidgets('send to author posts', (tester) async {
    final transport = transportFor(
      send: const Response(status: 200, body: '{}'),
    );
    final model = await modelFor(transport);
    await pump(tester, model);
    await openDraft(tester);

    await tester.tap(find.text('Send to the author’s agent'));
    await tester.pumpAndSettle();

    final sends = [
      for (final request in transport.requests)
        if (request.method == 'POST' &&
            request.path == '/api/automations/review/drafts/d-1/send-to-author')
          request,
    ];
    expect(sends, hasLength(1));
    expect((sends.single.body! as Map)['findingIds'], ['f-1']);
  });

  testWidgets('discard confirms', (tester) async {
    final transport = transportFor(
      discard: const Response(status: 200, body: '{}'),
    );
    final model = await modelFor(transport);
    await pump(tester, model);
    await openDraft(tester);

    await tester.tap(find.text('Discard'));
    await tester.pumpAndSettle();
    expect(find.text('Discard this review of #42?'), findsOneWidget);
    expect(
      transport.requests.where((request) => request.method == 'POST'),
      isEmpty,
    );

    await tester.tap(find.text('Discard it'));
    await tester.pumpAndSettle();
    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' &&
            request.path == '/api/automations/review/drafts/d-1/discard',
      ),
      hasLength(1),
    );
    expect(model.drafts, isEmpty);
  });

  testWidgets('the fixer’s rule is shown and not editable', (tester) async {
    final transport = transportFor();
    final model = await modelFor(transport);
    await pump(tester, model);

    await tester.tap(find.text('The CI fixer'));
    await tester.pumpAndSettle();
    expect(
      find.textContaining('never deletes, skips or disables a test'),
      findsOneWidget,
    );
    // The rule is text, not a setting: no field stands in for it, and the
    // automatic-fixing switch is a planned row.
    expect(find.byType(Field), findsNothing);
    expect(find.text('Automatic fixing'), findsOneWidget);
  });

  test('posting a draft reports the write', () async {
    final transport = transportFor(
      post: Response(
        status: 200,
        body: jsonEncode(draftJson(status: 'posted')),
      ),
    );
    var writes = 0;
    final model = await modelFor(transport, onWrote: () => writes++);

    await model.postDraft(model.drafts.single);

    expect(writes, 1);
  });

  test('a refused post reports nothing', () async {
    final transport = transportFor(
      post: Response(
        status: 409,
        body: jsonEncode({'error': 'this review is already posted'}),
      ),
    );
    var writes = 0;
    final model = await modelFor(transport, onWrote: () => writes++);

    await expectLater(
      model.postDraft(model.drafts.single),
      throwsA(isA<BridgeFailure>()),
    );

    expect(writes, 0);
  });

  test('a held draft is an ask', () async {
    final fixture = await ModelFixture.start([FixtureHost(name: 'local')]);
    await fixture.refresh();
    final model = inbox.InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(model.dispose);

    model.showHeldDrafts('local', [
      ReviewDraft(
        id: 'd-1',
        repoID: 'acme/widgets',
        number: 42,
        title: 'Make the scan report a file it cannot read',
        headSHA: '9f1ad475',
        findings: const [
          Finding(id: 'f-1', severity: 'blocking', title: 'A blocking one'),
        ],
        summary: 'One blocking finding.',
        status: 'draft',
        createdAt: '2026-11-17T03:00:00Z',
      ),
    ]);

    final ask = model.asks.single;
    expect(ask.kind, inbox.AskKind.reviewDraft);
    expect(ask.id, 'd-1');
    expect(ask.title, 'Make the scan report a file it cannot read');
    expect(ask.actions.first.label, 'Post');
    expect(ask.actions.first.primary, isTrue);
    expect(
      ask.actions.map((action) => action.label),
      containsAll(['Post', 'Send to the author’s agent', 'Discard']),
    );
  });
}
