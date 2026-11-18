import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/bots_model.dart';
import 'package:marshal_app/src/model/recipes_model.dart';
import 'package:marshal_app/src/model/schedules_model.dart';
import 'package:marshal_app/src/model/section_model.dart';
import 'package:marshal_app/src/model/watches_model.dart';
import 'package:marshal_app/src/ui/automations/automations_place.dart';
import 'package:marshal_app/src/ui/automations/bots_tab.dart';
import 'package:marshal_app/src/ui/automations/recipes_tab.dart';
import 'package:marshal_app/src/ui/automations/schedules_tab.dart';
import 'package:marshal_app/src/ui/automations/watch_form.dart';
import 'package:marshal_app/src/ui/automations/watches_tab.dart';
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

  /// The place with every tab's real widget, wired the way `main.dart` wires
  /// its one New automation (F9 B4).
  Widget place({
    required BotsModel bots,
    required SchedulesModel schedules,
    required WatchesModel watches,
    required RecipesModel recipes,
    int tab = 0,
  }) => AutomationsPlace(
    tab: tab,
    bots: (tabs, state) => BotsTab(model: bots, tabs: tabs, state: state),
    schedules: (tabs, state) =>
        SchedulesTab(model: schedules, tabs: tabs, state: state),
    watches: (tabs, state) =>
        WatchesTab(model: watches, tabs: tabs, state: state),
    recipes: (tabs, state) =>
        RecipesTab(model: recipes, tabs: tabs, state: state),
    onNew: (tab, context) async {
      switch (tab) {
        case 1:
          SchedulesTab.create(context, schedules);
        case 2:
          await showWatchForm(context, watches);
      }
    },
  );

  testWidgets('New automation starts a schedule', (tester) async {
    final now = DateTime.utc(2026, 11, 17, 4);
    final bots = BotsModel.fixture(
      const [],
      project: '/home/u/marshal',
      now: now,
    );
    final schedules = SchedulesModel.fixture(const <Schedule>[]);
    final watches = WatchesModel.fixture(const [], now: now, host: 'local');
    final recipes = RecipesModel.fixture(const []);
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      addTearDown(model.dispose);
      await model.load();
    }

    await pump(
      tester,
      place(
        bots: bots,
        schedules: schedules,
        watches: watches,
        recipes: recipes,
      ),
    );
    await tester.tap(find.text('New automation'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Schedule'));
    await tester.pumpAndSettle();

    // The form the section page's own New opens, for a blank schedule: the
    // shared detail with nothing typed and nothing to save yet.
    expect(find.text('New schedule'), findsOneWidget);
    expect(schedules.editing?.id, isEmpty);
    expect(schedules.isCreating, isTrue);
    expect(tester.widget<Btn>(find.widgetWithText(Btn, 'Save')).onTap, isNull);
    final fields = tester.widgetList<EditableText>(find.byType(EditableText));
    expect(fields, hasLength(3));
    expect(
      fields.every((field) => field.controller.text.isEmpty),
      isTrue,
      reason: 'a new schedule starts blank',
    );
  });

  testWidgets('Schedules has one New', (tester) async {
    final now = DateTime.utc(2026, 11, 17, 4);
    final bots = BotsModel.fixture(
      const [],
      project: '/home/u/marshal',
      now: now,
    );
    final schedules = SchedulesModel.fixture(const <Schedule>[]);
    final watches = WatchesModel.fixture(const [], now: now, host: 'local');
    final recipes = RecipesModel.fixture(const []);
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      addTearDown(model.dispose);
      await model.load();
    }

    await pump(
      tester,
      place(
        bots: bots,
        schedules: schedules,
        watches: watches,
        recipes: recipes,
        tab: 1,
      ),
    );

    // Schedules draws its own New in the page header, so the place's one New
    // is not drawn beside it: the tab offers exactly one way to create.
    expect(find.text('New automation'), findsNothing);
    expect(find.text('New schedule'), findsOneWidget);
  });

  testWidgets('the menu offers only the kinds that can be made', (
    tester,
  ) async {
    final now = DateTime.utc(2026, 11, 17, 4);
    final bots = BotsModel.fixture(
      const [],
      project: '/home/u/marshal',
      now: now,
    );
    final schedules = SchedulesModel.fixture(const <Schedule>[]);
    final watches = WatchesModel.fixture(const [], now: now, host: 'local');
    final recipes = RecipesModel.fixture(const [
      Recipe(
        name: 'gate',
        title: 'The gate',
        kind: 'prompt',
        prompt: 'Run the gate',
        builtin: true,
      ),
    ]);
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      addTearDown(model.dispose);
      await model.load();
    }

    await pump(
      tester,
      place(
        bots: bots,
        schedules: schedules,
        watches: watches,
        recipes: recipes,
      ),
    );
    await tester.tap(find.text('New automation'));
    await tester.pumpAndSettle();

    // Bots is set per project and has no create of its own, so it is not
    // offered; the three that can be started are.
    final menu = tester.widget<Menu>(find.byType(Menu));
    expect(
      [for (final choice in menu.choices) choice.name],
      ['Schedule', 'Watch', 'Recipe'],
    );

    // A recipe is made by copying a built-in, which is what the Recipes tab
    // offers, so its choice opens that tab rather than a blank form.
    await tester.tap(find.text('Recipe'));
    await tester.pumpAndSettle();
    expect(tester.widget<Tabs>(find.byType(Tabs)).index, 3);
    expect(find.text('The gate'), findsOneWidget);
  });

  testWidgets('every tab carries its count', (tester) async {
    final now = DateTime.utc(2026, 11, 17, 4);
    final bots = BotsModel.fixture(
      const [
        Bot(
          kind: BotKind.review,
          enabled: true,
          repoID: 'acme/widgets',
          summary: 'On',
          facts: [],
        ),
        Bot(
          kind: BotKind.fixer,
          enabled: true,
          repoID: 'acme/widgets',
          summary: 'On',
          facts: [],
        ),
      ],
      project: '/home/u/marshal',
      now: now,
    );
    final schedules = SchedulesModel.fixture([
      for (var i = 0; i < 3; i++)
        Schedule(
          id: 's-$i',
          name: 'schedule $i',
          recipe: 'gate',
          cron: '0 3 * * *',
          enabled: true,
          ownerID: 'owner-1',
        ),
    ]);
    final watches = WatchesModel.fixture(
      const [
        WatchInfo(
          agentID: 'studio',
          id: 'w-1',
          name: 'the gate',
          kind: 'command',
          state: 'watching',
          mode: 'interval',
          intervalMs: 60000,
          fireCount: 0,
          createdAt: 1763352000000,
          samples: [],
        ),
      ],
      now: now,
      host: 'local',
    );
    final recipes = RecipesModel.fixture([
      for (var i = 0; i < 4; i++)
        Recipe(
          name: 'recipe-$i',
          title: 'Recipe $i',
          kind: 'prompt',
          prompt: 'Run it',
        ),
    ]);
    for (final model in <SectionModel<Object>>[
      bots,
      schedules,
      watches,
      recipes,
    ]) {
      addTearDown(model.dispose);
      await model.load();
    }

    await pump(
      tester,
      AutomationsPlace(
        bots: (tabs, state) => BotsTab(model: bots, tabs: tabs, state: state),
        schedules: (tabs, state) =>
            SchedulesTab(model: schedules, tabs: tabs, state: state),
        watches: (tabs, state) =>
            WatchesTab(model: watches, tabs: tabs, state: state),
        recipes: (tabs, state) =>
            RecipesTab(model: recipes, tabs: tabs, state: state),
        counts: {
          0: bots.items.length,
          1: schedules.items.length,
          2: watches.items.length,
          3: recipes.items.length,
        },
      ),
    );

    // Bots, Schedules, Watches and Recipes: the four tabs say how many entries
    // each one holds, from the model that holds them.
    final bar = tester.widget<Tabs>(find.byType(Tabs));
    expect([for (final item in bar.tabs) item.count], [2, 3, 1, 4]);
  });
}
