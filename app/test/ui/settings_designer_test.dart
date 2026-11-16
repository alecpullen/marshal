import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/environment_model.dart';
import 'package:marshal_app/src/model/environments_model.dart'
    show EnvironmentBuilds, EnvironmentVersion;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/settings/environment_designer.dart';

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

/// What `GET /api/workspaces/X/builds` answers: one version, in [state].
String buildsJson(String state) => jsonEncode({
  'versions': [
    {
      'n': 3,
      'at': '2026-11-17T03:00:00Z',
      'by': 'owner-1',
      'sha256': '9f1ad475',
      'buildStatus': state,
    },
  ],
  'pool': {'size': 2, 'idle': 1, 'starting': 0},
  'starts': {'coldMs': 24000, 'warmMs': 1800},
});

/// What a draft save answers: the source it wrote, and the parse this test
/// does not read.
EnvLoaded savedDraft(String text, String name) => EnvLoaded(
  source: text,
  version: 0,
  doc: const EnvDoc(
    workspace: EnvWorkspace(name: 'go-service', base: 'ubuntu:24.04'),
    packages: EnvPackages(),
    network: EnvNetwork(mode: 'open'),
    resources: EnvResources(),
    policy: EnvPolicy(),
    setup: EnvSetup(),
  ),
  sections: const [],
  diagnostics: const [],
  meta: EnvMeta(
    name: name,
    ownerID: 'owner-1',
    createdAt: '2026-11-17T03:00:00Z',
    published: 0,
    pool: 0,
    versions: const [],
  ),
);

/// The environment designer (F7 decision 6): two views of one document that
/// stay in step, one layer at a time, and a publish that waits for a build.
void main() {
  const source = '''[workspace]
name = "go-service"
base = "ubuntu:24.04"
toolchains = ["go@1.24"]

[packages]
go = ["golang.org/x/tools/gopls@latest"]

[network]
mode = "allowlist"
egress = ["proxy.golang.org"]
''';

  EnvironmentBuilds builds(String state) => EnvironmentBuilds(
    versions: [
      EnvironmentVersion(n: 3, detail: 'marshal-ws/go-service:3', state: state),
    ],
    poolSize: 2,
    poolIdle: 1,
    poolStarting: 0,
    coldMs: 24000,
    warmMs: 1800,
    sizeBytes: 734003200,
  );

  Future<void> pump(WidgetTester tester, Widget screen) async {
    tester.view.physicalSize = const Size(1400, 1200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(brightness: Brightness.light, home: screen),
    );
  }

  testWidgets('selecting a layer selects its source range', (tester) async {
    final model = EnvironmentModel.fixture(name: 'go-service', source: source);
    addTearDown(model.dispose);
    await pump(tester, EnvironmentDesigner(model: model));

    await tester.tap(find.text('Packages'));
    await tester.pumpAndSettle();

    final (start, end) = model.charRangeOf(LayerKind.packages);
    expect(model.source.substring(start, end), contains('[packages]'));
    final field = tester.widget<EditableText>(find.byType(EditableText));
    expect(field.controller.selection.baseOffset, start);
    expect(field.controller.selection.extentOffset, end);
  });

  testWidgets('editing source updates the layers', (tester) async {
    final model = EnvironmentModel.fixture(name: 'go-service', source: source);
    addTearDown(model.dispose);
    await pump(tester, EnvironmentDesigner(model: model));

    expect(find.text('Packages'), findsOneWidget);

    await tester.enterText(
      find.byType(EditableText),
      '[workspace]\nname = "go-service"\nbase = "ubuntu:24.04"\n\n'
      '[network]\nmode = "open"\n',
    );
    await tester.pumpAndSettle();

    expect(find.text('Network'), findsOneWidget);
    expect(find.text('Packages'), findsNothing);
    expect(model.source, contains('mode = "open"'));
  });

  testWidgets('editing a layer rewrites only its range', (tester) async {
    final model = EnvironmentModel.fixture(name: 'go-service', source: source);
    addTearDown(model.dispose);
    await pump(tester, EnvironmentDesigner(model: model));

    await tester.tap(find.text('Packages'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Edit this layer'));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byType(EditableText).last,
      '[packages]\nnpm = ["pnpm@9"]',
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Apply'));
    await tester.pumpAndSettle();

    // The layer's own lines changed, and nothing else did.
    expect(model.source, contains('npm = ["pnpm@9"]'));
    expect(model.source, isNot(contains('gopls')));
    expect(model.source, contains('name = "go-service"'));
    expect(model.source, contains('toolchains = ["go@1.24"]'));
    expect(model.source, contains('[network]'));
    expect(model.source, contains('egress = ["proxy.golang.org"]'));
  });

  testWidgets('publish is disabled until a build passes', (tester) async {
    final transport = _Scripted({
      'GET /api/workspaces/go-service/builds': Response(
        status: 200,
        body: buildsJson('failed'),
      ),
    });
    final model = EnvironmentModel(
      name: 'go-service',
      source: source,
      client: BridgeClient(transport),
    );
    addTearDown(model.dispose);
    await model.refreshBuilds();
    expect(builds('failed').versions.single.state, 'failed');

    await pump(tester, EnvironmentDesigner(model: model));
    expect(
      tester.widget<Btn>(find.widgetWithText(Btn, 'Publish')).onTap,
      isNull,
      reason: 'a build that failed published nothing',
    );

    transport.replies['GET /api/workspaces/go-service/builds'] = Response(
      status: 200,
      body: buildsJson('ok'),
    );
    await model.refreshBuilds();
    await tester.pumpAndSettle();

    expect(
      tester.widget<Btn>(find.widgetWithText(Btn, 'Publish')).onTap,
      isNotNull,
      reason: 'a build that passed may be published',
    );
  });

  testWidgets('an edit past a passing build disables publish', (tester) async {
    final transport = _Scripted({
      'GET /api/workspaces/go-service/builds': Response(
        status: 200,
        body: buildsJson('ok'),
      ),
    });
    final model = EnvironmentModel(
      name: 'go-service',
      source: source,
      client: BridgeClient(transport),
    );
    addTearDown(model.dispose);
    await model.refreshBuilds();
    await pump(tester, EnvironmentDesigner(model: model));

    Btn publish() => tester.widget<Btn>(find.widgetWithText(Btn, 'Publish'));
    expect(publish().onTap, isNotNull, reason: 'the newest build passed');

    // The source moves past the build: nothing has built what is now there.
    await tester.enterText(
      find.byType(EditableText),
      '$source\n[setup]\nrun = "make test"\n',
    );
    await tester.pumpAndSettle();
    expect(publish().onTap, isNull, reason: 'the draft has never been built');

    // Taking the edit back leaves the draft the build read.
    model.discardEdits();
    await tester.pumpAndSettle();
    expect(publish().onTap, isNotNull, reason: 'the edit was discarded');
  });

  testWidgets('a draft saved after the build is built before it publishes', (
    tester,
  ) async {
    const next = '''[workspace]
name = "go-service"
base = "ubuntu:24.04"

[network]
mode = "open"
''';
    final transport = _Scripted({
      'GET /api/workspaces/go-service/builds': Response(
        status: 200,
        body: buildsJson('ok'),
      ),
      'PUT /api/workspaces/go-service/draft': Response(
        status: 200,
        body: jsonEncode(savedDraft(next, 'go-service').toJson()),
      ),
      'POST /api/workspaces/go-service/builds': Response(
        status: 200,
        body: jsonEncode({'version': 4}),
      ),
    });
    final model = EnvironmentModel(
      name: 'go-service',
      source: source,
      client: BridgeClient(transport),
    );
    addTearDown(model.dispose);
    await model.refreshBuilds();
    await pump(tester, EnvironmentDesigner(model: model));

    Btn publish() => tester.widget<Btn>(find.widgetWithText(Btn, 'Publish'));
    expect(publish().onTap, isNotNull, reason: 'the newest build passed');

    // Saved, and so no longer an unsaved edit: the build still read the old
    // draft, and the bridge publishes whatever draft it is handed.
    model.editSource(next);
    await model.saveDraft();
    await tester.pumpAndSettle();
    expect(publish().onTap, isNull, reason: 'the saved draft is unbuilt');

    await model.build();
    await tester.pumpAndSettle();
    expect(publish().onTap, isNotNull, reason: 'the rebuild read that draft');
  });
}
