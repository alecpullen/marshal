import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart'
    show AuditEvent, DiskStatus;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/usage_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/usage_section.dart';

/// A transport that answers canned routes (query included) and records what
/// was asked.
class _Scripted implements Transport {
  _Scripted(this.replies);

  final Map<String, Response> replies;
  final requests = <Request>[];

  static String keyOf(Request request) {
    final query = [
      for (final entry in request.query.entries) '${entry.key}=${entry.value}',
    ]..sort();
    return '${request.method} ${request.path}'
        '${query.isEmpty ? '' : '?${query.join('&')}'}';
  }

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return replies[keyOf(request)] ??
        Response(status: 404, body: jsonEncode({'error': 'no canned route'}));
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}

/// Settings' Usage and audit section (F7 decision 7): the grouping changes the
/// series, the audit log filters by kind, and a prune says what it removes
/// before it runs.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1400, 1100);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  Map<String, Object?> report(String key, double cost) => {
    'range': '7d',
    'totals': {
      'costUsd': cost,
      'promptTokens': 1000,
      'completionTokens': 200,
      'agentHours': 1.5,
      'prsShipped': 1,
    },
    'series': [
      {'key': key, 'costUsd': cost, 'tokens': 1200},
    ],
  };

  Map<String, Object?> diskBody() => {
    'repos': 734003200,
    'work': 209715200,
    'total': 943718400,
    'measuredAt': '2026-11-17T04:00:00Z',
    'budgetMB': 4096,
  };

  testWidgets('grouping changes the series', (tester) async {
    final transport = _Scripted({
      'GET /api/usage?by=project&range=7d': Response(
        status: 200,
        body: jsonEncode(report('go-service', 7.25)),
      ),
      'GET /api/usage?by=role&range=7d': Response(
        status: 200,
        body: jsonEncode(report('reviewer', 4.10)),
      ),
      'GET /api/audit?limit=50': const Response(status: 200, body: '[]'),
      'GET /api/disk': Response(status: 200, body: jsonEncode(diskBody())),
    });
    final model = UsageModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, UsageSection(model: model));
    // The row and the chart's own bar both label the series.
    expect(find.text('go-service'), findsWidgets);
    expect(find.text('Cost by project'), findsOneWidget);

    await tester.tap(find.text('Filter'));
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(of: find.byType(Sheet), matching: find.text('By role')),
    );
    await tester.pumpAndSettle();

    expect(find.text('reviewer'), findsWidgets);
    expect(find.text('go-service'), findsNothing);
    expect(find.text('Cost by role'), findsOneWidget);
    expect(
      transport.requests.any(
        (request) =>
            request.path == '/api/usage' && request.query['by'] == 'role',
      ),
      isTrue,
    );
  });

  testWidgets('audit filters', (tester) async {
    final model = UsageModel.fixture(
      report: null,
      audit: const [
        AuditEvent(ts: '2026-11-17T03:00:00Z', event: 'prune', bytes: 1048576),
        AuditEvent(ts: '2026-11-17T02:00:00Z', event: 'spawn', agentID: 'a-1'),
      ],
      disk: const DiskStatus(
        repos: 734003200,
        work: 209715200,
        total: 943718400,
        measuredAt: '2026-11-17T04:00:00Z',
        budgetMB: 4096,
      ),
    );
    addTearDown(model.dispose);

    await pump(tester, UsageSection(model: model));
    expect(find.text('spawn'), findsOneWidget);
    expect(find.text('prune'), findsOneWidget);

    await tester.tap(find.text('Filter'));
    await tester.pumpAndSettle();
    await tester.tap(
      find.descendant(of: find.byType(Sheet), matching: find.text('prune')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Close'));
    await tester.pumpAndSettle();

    expect(find.text('spawn'), findsNothing);
    expect(find.text('prune'), findsOneWidget);
  });

  testWidgets('prune confirms and names what goes', (tester) async {
    final transport = _Scripted({
      'GET /api/usage?by=project&range=7d': Response(
        status: 200,
        body: jsonEncode(report('go-service', 7.25)),
      ),
      'GET /api/audit?limit=50': const Response(status: 200, body: '[]'),
      'GET /api/disk': Response(status: 200, body: jsonEncode(diskBody())),
      'POST /api/prune': const Response(
        status: 200,
        body: '{"reclaimed":1048576,"total":942669824}',
      ),
    });
    final model = UsageModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, UsageSection(model: model));
    await tester.tap(find.text('Disk'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Prune…'));
    await tester.pumpAndSettle();

    // The confirmation names what goes, and what stays.
    expect(find.textContaining('mirror checkouts'), findsOneWidget);
    expect(find.textContaining('work directories'), findsOneWidget);
    expect(find.textContaining('is kept'), findsOneWidget);
    expect(
      transport.requests.where((request) => request.path == '/api/prune'),
      isEmpty,
      reason: 'nothing is removed until the prune is confirmed',
    );

    await tester.tap(
      find.descendant(
        of: find.byType(Sheet).last,
        matching: find.text('Prune'),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      transport.requests.where(
        (request) => request.method == 'POST' && request.path == '/api/prune',
      ),
      hasLength(1),
    );
    expect(model.pruned?.reclaimed, 1048576);
  });
}
