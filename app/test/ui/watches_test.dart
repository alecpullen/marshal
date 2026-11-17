import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/watches_model.dart';
import 'package:marshal_app/src/ui/automations/watches_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';

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

/// The Watches section of Automations (F7 decision 4) and its Overview card:
/// a tripped watch says what it did, an agent's watch names the agent, and
/// the reroute undo is one post.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  Map<String, Object?> watchJson({
    String agentID = 'a-1',
    String id = 'w-1',
    String name = 'the gate',
    String state = 'fired',
    int fireCount = 2,
    Map<String, Object?>? onTrip,
    Map<String, Object?>? lastReroute,
  }) => {
    'agentId': agentID,
    'id': id,
    'name': name,
    'kind': 'command',
    'state': state,
    'condition': 'exit != 0',
    'mode': 'interval',
    'intervalMs': 5000,
    'owner': agentID,
    'fireCount': fireCount,
    'lastSample': 'exit 1',
    'createdAt': 1763352000000,
    'lastFiredAt': now.millisecondsSinceEpoch - 4 * 60 * 1000,
    'samples': [
      {'at': 1763352300000, 'value': 0, 'tripped': false},
      {'at': 1763352600000, 'value': 1, 'tripped': true},
    ],
    'onTrip': ?onTrip,
    'lastReroute': ?lastReroute,
  };

  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1000, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('a tripped watch says what it did', (tester) async {
    final transport = _Scripted({
      'GET /api/watches': Response(
        status: 200,
        body: jsonEncode([
          watchJson(
            onTrip: {
              'notify': true,
              'resume': false,
              'reroute': {'role': 'implementer', 'preset': 'local'},
            },
          ),
        ]),
      ),
    });
    final model = WatchesModel(BridgeClient(transport), now: now);
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, WatchesTab(model: model));
    await tester.tap(find.text('the gate'));
    await tester.pumpAndSettle();

    expect(find.text('It has tripped 2 times.'), findsOneWidget);
    expect(
      find.text('When it trips it reroutes implementer to local.'),
      findsOneWidget,
    );
    expect(find.text('4m ago'), findsOneWidget);
  });

  testWidgets('an agent’s watch names the agent', (tester) async {
    final transport = _Scripted({
      'GET /api/watches': Response(
        status: 200,
        body: jsonEncode([
          watchJson(agentID: 'a-7', id: 'w-7', state: 'watching', fireCount: 0),
          watchJson(agentID: 'studio', id: 'w-2', name: 'the queue'),
        ]),
      ),
    });
    final model = WatchesModel(BridgeClient(transport), now: now);
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, WatchesTab(model: model));
    expect(find.textContaining('Agent a-7'), findsOneWidget);
    expect(find.textContaining('The bridge · command'), findsOneWidget);
  });

  testWidgets('the detail undoes the reroute the watch carries', (
    tester,
  ) async {
    final transport = _Scripted({
      'GET /api/watches': Response(
        status: 200,
        body: jsonEncode([
          watchJson(
            onTrip: {
              'notify': true,
              'resume': false,
              'reroute': {'role': 'implementer', 'preset': 'local'},
            },
            lastReroute: {
              'id': 'rr-1',
              'at': now.millisecondsSinceEpoch - 4 * 60 * 1000,
            },
          ),
        ]),
      ),
      'POST /api/reroutes/rr-1/undo': const Response(status: 200, body: '{}'),
    });
    final model = WatchesModel(BridgeClient(transport), now: now);
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, WatchesTab(model: model));
    await tester.tap(find.text('the gate'));
    await tester.pumpAndSettle();

    final undo = find.text('Undo the reroute');
    expect(undo, findsOneWidget);
    expect(
      find.text('Puts back the binding the trip changed 4m ago.'),
      findsOneWidget,
    );
    await tester.ensureVisible(undo);
    await tester.tap(undo);
    await tester.pumpAndSettle();

    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' &&
            request.path == '/api/reroutes/rr-1/undo',
      ),
      hasLength(1),
    );
    expect(
      find.text(
        'Undone: the role the trip rebound is back on its earlier binding.',
      ),
      findsOneWidget,
    );
  });

  testWidgets('the undo stays disabled for a watch with no reroute record', (
    tester,
  ) async {
    final transport = _Scripted({
      'GET /api/watches': Response(
        status: 200,
        body: jsonEncode([
          watchJson(
            onTrip: {
              'notify': true,
              'resume': false,
              'reroute': {'role': 'implementer', 'preset': 'local'},
            },
          ),
        ]),
      ),
    });
    final model = WatchesModel(BridgeClient(transport), now: now);
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, WatchesTab(model: model));
    await tester.tap(find.text('the gate'));
    await tester.pumpAndSettle();

    final undo = find.text('Undo the reroute');
    expect(undo, findsOneWidget);
    expect(
      find.text(
        'There is nothing to undo: the bridge records a reroute only when a '
        'trip makes one.',
      ),
      findsOneWidget,
    );
    await tester.ensureVisible(undo);
    await tester.tap(undo);
    await tester.pumpAndSettle();

    expect(
      transport.requests.where((request) => request.method == 'POST'),
      isEmpty,
    );
  });

  test('undo posts the reroute undo', () async {
    final transport = _Scripted({
      'GET /api/watches': const Response(status: 200, body: '[]'),
      'POST /api/reroutes/rr-1/undo': const Response(status: 200, body: '{}'),
    });
    final model = WatchesModel(BridgeClient(transport), now: now);
    addTearDown(model.dispose);
    await model.undoReroute('rr-1');
    expect(
      transport.requests.where(
        (request) =>
            request.method == 'POST' &&
            request.path == '/api/reroutes/rr-1/undo',
      ),
      hasLength(1),
    );
  });

  testWidgets('a tripped watch appears in the Overview card', (tester) async {
    final model = WatchesModel(
      BridgeClient(
        _Scripted({
          'GET /api/watches': const Response(status: 200, body: '[]'),
        }),
      ),
      now: now,
    );
    addTearDown(model.dispose);

    await pump(
      tester,
      Overview(
        overview: OverviewModel(
          needsYou: const [],
          running: const [],
          projects: const [],
          capacity: const [],
          spend: null,
        ),
        now: now,
        automations: model.tripped.isEmpty
            ? const [
                TrippedWatch(
                  name: 'the gate',
                  agent: 'Agent a-1',
                  action: 'Reroutes implementer to local',
                  age: '4m',
                ),
              ]
            : model.tripped,
      ),
    );
    expect(find.text('AUTOMATIONS'), findsOneWidget);
    expect(find.text('the gate'), findsOneWidget);
    expect(
      find.text('Agent a-1 · Reroutes implementer to local'),
      findsOneWidget,
    );
  });
}
