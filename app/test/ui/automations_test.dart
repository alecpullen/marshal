import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/recipes_model.dart';
import 'package:marshal_app/src/model/schedules_model.dart';
import 'package:marshal_app/src/ui/automations/recipes_tab.dart';
import 'package:marshal_app/src/ui/automations/schedules_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// A transport that answers canned routes and records what was asked, so a
/// widget test exercises the client and its routes without a socket: the
/// widget binding answers every real HTTP request with 400.
class _Scripted implements Transport {
  _Scripted(this._replies);

  /// `METHOD /route` to the answer it gets.
  final Map<String, Response> _replies;

  /// Every request, oldest first.
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    final reply = _replies['${request.method} ${request.path}'];
    return reply ??
        Response(
          status: 404,
          body: jsonEncode({'error': 'no canned route for ${request.path}'}),
        );
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// The Schedules and Recipes sections of Automations (F7 decision 4): what a
/// create posts, what run now posts, that a delete confirms first, and that a
/// built-in recipe is copy only.
void main() {
  Map<String, Object?> scheduleJson({
    String id = 's-1',
    String name = 'nightly gate',
  }) => {
    'id': id,
    'name': name,
    'recipe': 'gate',
    'cron': '0 3 * * *',
    'enabled': true,
    'ownerId': 'owner-1',
  };

  Map<String, Object?> recipeJson({
    required String name,
    String? title,
    bool builtin = false,
  }) => {
    'name': name,
    'title': ?title,
    'kind': 'prompt',
    'prompt': 'Run the gate',
    if (builtin) 'builtin': true,
  };

  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1000, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('create posts a schedule', (tester) async {
    final transport = _Scripted({
      'GET /api/schedules': const Response(status: 200, body: '[]'),
      'POST /api/schedules': Response(
        status: 201,
        body: jsonEncode(scheduleJson(id: 's-new', name: 'nightly gate')),
      ),
    });
    final model = SchedulesModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, SchedulesTab(model: model));
    await tester.tap(find.text('New schedule'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText).at(0), 'nightly gate');
    await tester.enterText(find.byType(EditableText).at(1), 'gate');
    await tester.enterText(find.byType(EditableText).at(2), '0 3 * * *');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    final posts = [
      for (final request in transport.requests)
        if (request.method == 'POST' && request.path == '/api/schedules')
          request,
    ];
    expect(posts, hasLength(1), reason: 'a create posts once');
    final body = posts.single.body! as Schedule;
    expect(body.name, 'nightly gate');
    expect(body.cron, '0 3 * * *');
    expect(model.items.single.id, 's-new');
  });

  testWidgets('run now posts', (tester) async {
    final transport = _Scripted({
      'GET /api/schedules': Response(
        status: 200,
        body: jsonEncode([scheduleJson()]),
      ),
      'POST /api/schedules/s-1/run': const Response(
        status: 202,
        body: '{"agentId":"a-9"}',
      ),
    });
    final model = SchedulesModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, SchedulesTab(model: model));
    await tester.tap(find.text('nightly gate'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Run it now'));
    await tester.pumpAndSettle();

    final runs = [
      for (final request in transport.requests)
        if (request.method == 'POST' &&
            request.path == '/api/schedules/s-1/run')
          request,
    ];
    expect(runs, hasLength(1));
    expect(find.textContaining('Started'), findsOneWidget);
  });

  testWidgets('delete confirms', (tester) async {
    final transport = _Scripted({
      'GET /api/schedules': Response(
        status: 200,
        body: jsonEncode([scheduleJson()]),
      ),
      'DELETE /api/schedules/s-1': const Response(
        status: 200,
        body: '{"status":"deleted"}',
      ),
    });
    final model = SchedulesModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, SchedulesTab(model: model));
    await tester.tap(find.text('nightly gate'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('Delete this schedule'));
    await tester.tap(find.text('Delete this schedule'));
    await tester.pumpAndSettle();

    // Nothing is deleted until the confirmation is answered, and it names
    // what is lost.
    expect(
      transport.requests.where((request) => request.method == 'DELETE'),
      isEmpty,
    );
    expect(find.text('Delete nightly gate?'), findsOneWidget);

    await tester.tap(find.text('Delete it'));
    await tester.pumpAndSettle();
    expect(
      transport.requests.where(
        (request) =>
            request.method == 'DELETE' && request.path == '/api/schedules/s-1',
      ),
      hasLength(1),
    );
    expect(model.items, isEmpty);
  });

  testWidgets('a built-in recipe is copy only', (tester) async {
    final transport = _Scripted({
      'GET /api/recipes': Response(
        status: 200,
        body: jsonEncode([
          recipeJson(name: 'gate', title: 'The gate', builtin: true),
          recipeJson(name: 'nightly-gate', title: 'Nightly gate'),
        ]),
      ),
      'POST /api/recipes/gate/copy': Response(
        status: 201,
        body: jsonEncode(recipeJson(name: 'my-gate')),
      ),
    });
    final model = RecipesModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, RecipesTab(model: model));
    await tester.tap(find.text('The gate'));
    await tester.pumpAndSettle();

    // A built-in cannot be edited or deleted: Save has nothing to do, its
    // fields are read only, and no delete is offered.
    expect(tester.widget<Btn>(find.widgetWithText(Btn, 'Save')).onTap, isNull);
    expect(find.text('Delete this recipe'), findsNothing);
    final title = tester.widget<Field>(
      find.ancestor(of: find.text('Title'), matching: find.byType(Field)),
    );
    expect(title.enabled, isFalse);

    await tester.ensureVisible(find.text('Copy it'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(EditableText).at(3), 'my-gate');
    await tester.tap(find.text('Copy it'));
    await tester.pumpAndSettle();

    final copies = [
      for (final request in transport.requests)
        if (request.method == 'POST' &&
            request.path == '/api/recipes/gate/copy')
          request,
    ];
    expect(copies, hasLength(1));
    expect((copies.single.body! as RecipeCopy).name, 'my-gate');
    expect(model.items.map((recipe) => recipe.name), contains('my-gate'));
  });
}
