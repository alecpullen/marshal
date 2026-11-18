import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart'
    show
        EnvCreate,
        EnvDoc,
        EnvLoaded,
        EnvListItem,
        EnvMeta,
        EnvNetwork,
        EnvPackages,
        EnvPolicy,
        EnvResources,
        EnvSetup,
        EnvWorkspace;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/environments_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/environments_section.dart';

/// A transport that answers canned routes, streams canned lines and records
/// what was asked.
class _Scripted implements Transport {
  _Scripted(this._replies, {this.streams = const {}});

  final Map<String, Response> _replies;
  final Map<String, List<String>> streams;
  final requests = <Request>[];

  @override
  Future<Response> send(Request request) async {
    requests.add(request);
    return _replies['${request.method} ${request.path}'] ??
        Response(status: 404, body: jsonEncode({'error': 'no canned route'}));
  }

  @override
  Stream<String> lines(Request request) async* {
    for (final line in streams[request.path] ?? const <String>[]) {
      yield line;
    }
  }
}

/// Settings' Environments section (F7 decision 6): rows with their version and
/// use, an import that starts a draft, and a build log that streams.
void main() {
  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1200, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  const item = EnvListItem(
    source: 'studio',
    name: 'go-service',
    published: 3,
    pool: 2,
    usage: 4,
    draftChanges: true,
  );

  const loaded = EnvLoaded(
    source: '[workspace]\nname = "go-service"\nbase = "ubuntu:24.04"\n',
    version: 0,
    doc: EnvDoc(
      workspace: EnvWorkspace(
        name: 'go-service',
        base: 'ubuntu:24.04',
        toolchains: ['go@1.24'],
      ),
      packages: EnvPackages(),
      network: EnvNetwork(mode: 'allowlist'),
      resources: EnvResources(),
      policy: EnvPolicy(),
      setup: EnvSetup(),
    ),
    sections: [],
    diagnostics: [],
    meta: EnvMeta(
      name: 'go-service',
      ownerID: 'owner-1',
      createdAt: '2026-11-16T09:00:00Z',
      published: 0,
      pool: 0,
      versions: [],
    ),
  );

  testWidgets('rows show version and use', (tester) async {
    final model = EnvironmentsModel.fixture(
      items: const [item],
      loaded: const {'go-service': loaded},
    );
    addTearDown(model.dispose);

    await pump(tester, EnvironmentsSection(model: model));

    expect(find.text('go-service'), findsOneWidget);
    expect(find.text('v3 · 4 agents'), findsOneWidget);
    expect(
      find.text('ubuntu:24.04 · go@1.24 · network allowlist'),
      findsOneWidget,
    );
  });

  testWidgets('import starts a draft', (tester) async {
    final transport = _Scripted({
      'GET /api/workspaces': const Response(status: 200, body: '[]'),
      'POST /api/workspaces': Response(
        status: 201,
        body: jsonEncode({
          'name': 'go-service',
          'ownerId': 'owner-1',
          'createdAt': '2026-11-17T04:00:00Z',
          'published': 0,
          'pool': 0,
          'versions': [],
        }),
      ),
    });
    final model = EnvironmentsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, EnvironmentsSection(model: model));
    // The header's import: the empty list offers the same one below it.
    await tester.tap(find.text('Import devcontainer.json').first);
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(EditableText).at(0), 'go-service');
    await tester.enterText(
      find.byType(EditableText).at(1),
      'acme/widgets/.devcontainer/devcontainer.json',
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Import'));
    await tester.pumpAndSettle();

    final posts = [
      for (final request in transport.requests)
        if (request.method == 'POST' && request.path == '/api/workspaces')
          request,
    ];
    expect(posts, hasLength(1));
    final body = posts.single.body! as EnvCreate;
    expect(body.name, 'go-service');
    expect(
      body.from,
      'devcontainer:acme/widgets/.devcontainer/devcontainer.json',
    );
  });

  testWidgets('a build log streams', (tester) async {
    final transport = _Scripted(
      {
        'GET /api/workspaces': Response(
          status: 200,
          body: jsonEncode([
            {
              'source': 'studio',
              'name': 'go-service',
              'published': 3,
              'usage': 1,
            },
          ]),
        ),
        'GET /api/workspaces/go-service': Response(
          status: 200,
          body: jsonEncode({
            'source': '[workspace]\n',
            'version': 0,
            'doc': {
              'workspace': {'name': 'go-service', 'base': 'ubuntu:24.04'},
              'packages': {},
              'network': {},
              'resources': {},
              'policy': {},
              'setup': {},
            },
            'sections': [],
            'diagnostics': [],
            'meta': {
              'name': 'go-service',
              'ownerId': 'owner-1',
              'createdAt': '2026-11-16T09:00:00Z',
              'published': 3,
              'pool': 2,
              'versions': [],
            },
          }),
        ),
        'GET /api/workspaces/go-service/builds': Response(
          status: 200,
          body: jsonEncode({
            'versions': [
              {
                'n': 3,
                'at': '2026-11-17T03:00:00Z',
                'by': 'owner-1',
                'sha256': '9f1ad475',
                'buildStatus': 'ok',
              },
            ],
            'pool': {'size': 2, 'idle': 1, 'starting': 0},
            'starts': {'coldMs': 24000, 'warmMs': 1800},
          }),
        ),
      },
      streams: {
        '/api/workspaces/go-service/builds/3/events': [
          'data: compiling the toolchains',
          '',
          'data: pushed marshal-ws/go-service:3',
          '',
        ],
      },
    );
    final model = EnvironmentsModel(BridgeClient(transport));
    addTearDown(model.dispose);
    await model.load();

    await pump(tester, EnvironmentsSection(model: model));
    await tester.tap(find.text('go-service'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Builds'));
    await tester.pumpAndSettle();

    expect(find.text('Warm pool 2 · 1 idle · 0 starting'), findsOneWidget);

    await tester.tap(
      find.descendant(of: find.byType(Sheet).last, matching: find.text('v3')),
    );
    await tester.pumpAndSettle();

    // The log came from the build's event route, a line at a time.
    expect(find.textContaining('compiling the toolchains'), findsOneWidget);
    expect(
      find.textContaining('pushed marshal-ws/go-service:3'),
      findsOneWidget,
    );

    // Closing the sheet stops the stream; draining its watchdog leaves the
    // test with no pending timer. The wait outlasts the stream's staleness
    // window (EventStream.defaultStaleAfter, 35s) so the watchdog has fired.
    await tester.pumpWidget(const SizedBox());
    await tester.pump(const Duration(seconds: 36));
  });
}
