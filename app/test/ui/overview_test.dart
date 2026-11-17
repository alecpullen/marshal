import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/attention.dart' as model;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/source_state.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';

import '../support/pulls.dart';
import '../support/screens.dart';

/// The Overview (F2 decision 8): the three oldest asks, a tile that opens its
/// thread, a host row that says how long it has been away, and one action
/// when there is nothing at all.
void main() {
  model.Ask ask(
    String id,
    String title,
    int ageMinutes, {
    model.AskKind kind = model.AskKind.approval,
    model.ThreadRef? thread,
    List<model.AskAction> actions = const [
      model.AskAction('Approve once', primary: true),
    ],
    model.AskSource source = model.AskSource.fleet,
    String project = '',
    model.AskScope scope = model.AskScope.everyone,
  }) => model.Ask(
    host: 'local',
    id: id,
    kind: kind,
    thread: thread,
    title: title,
    body: '',
    actions: actions,
    since: shellNow.subtract(Duration(minutes: ageMinutes)),
    source: source,
    project: project,
    scope: scope,
  );

  model.ThreadRef thread(String id, String title) => model.ThreadRef(
    host: 'local',
    id: id,
    project: '/home/u/marshal',
    title: title,
    kind: model.ThreadKind.agent,
    status: model.ThreadStatus.running,
    headline: 'Reading internal/acp',
    lastActive: shellNow.subtract(const Duration(minutes: 3)),
  );

  Widget show(
    OverviewModel overview, {
    List<Place>? opened,
    StateKind? state,
  }) => EmberApp(
    brightness: Brightness.light,
    home: Overview(
      overview: overview,
      now: shellNow,
      hosts: shellHosts,
      state: state,
      onSelect: (place) => opened?.add(place),
      onRetry: () {},
    ),
  );

  testWidgets('the needs-you count is the total, not the drawn three', (
    tester,
  ) async {
    final overview = OverviewModel(
      needsYou: [
        for (final (index, age) in [50, 40, 30].indexed)
          ask('tc-$index', 'ask $index', age),
      ],
      running: const [],
      needsYouTotal: 5,
      sources: const [],
      projects: const [],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    // The model hands over the three the Overview draws; the label counts
    // every ask, and the line beside them says where the rest are.
    expect(find.byType(AskCard), findsNWidgets(3));
    expect(find.text('ask 0'), findsOneWidget);
    expect(find.text('ask 2'), findsOneWidget);
    expect(find.text('5'), findsOneWidget);
    expect(find.text('2 more in the inbox.'), findsOneWidget);
    // The rest are one link away.
    expect(find.text('Open inbox'), findsOneWidget);
  });

  testWidgets('tile opens its thread', (tester) async {
    final opened = <Place>[];
    final overview = OverviewModel(
      needsYou: const [],
      running: [thread('a-1', 'Reliable session recovery')],
      needsYouTotal: 0,
      sources: const [],
      projects: const [],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview, opened: opened));

    await tester.tap(find.text('Reliable session recovery'));
    await tester.pump();

    expect(opened, [const Place(PlaceKind.thread, host: 'local', id: 'a-1')]);
  });

  testWidgets('stale host rows carry their age', (tester) async {
    final overview = OverviewModel(
      needsYou: const [],
      running: const [],
      needsYouTotal: 0,
      sources: const [],
      projects: const [],
      capacity: [
        HostCapacity(
          host: 'studio',
          reach: shellHosts.last.reach,
          lastContact: shellNow.subtract(const Duration(minutes: 4)),
          threads: 1,
          needsYou: 0,
        ),
      ],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    expect(find.text('1 thread · unreachable 4m'), findsOneWidget);
  });

  testWidgets('empty fleet shows one action', (tester) async {
    const overview = OverviewModel(
      needsYou: [],
      running: [],
      needsYouTotal: 0,
      sources: [],
      projects: [],
      capacity: [],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    final empty = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .where((message) => message.kind == StateKind.empty);
    expect(empty, hasLength(1));
    expect(find.byType(Btn), findsOneWidget);
  });

  testWidgets('ready and failing pull requests sit under needs you', (
    tester,
  ) async {
    final opened = <Place>[];
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Overview(
          overview: const OverviewModel(
            needsYou: [],
            running: [],
            needsYouTotal: 0,
            sources: [],
            projects: [],
            capacity: [],
            spend: null,
          ),
          now: shellNow,
          hosts: shellHosts,
          pulls: [
            pullView(number: 131, title: 'Quiet capture mode'),
            pullView(
              number: 132,
              title: 'Fix flaky calendar test',
              group: PullGroup.failing,
              checks: const [PullCheckView('unit', PullCheckState.failed)],
            ),
            // Neither ready nor failing: the Overview does not list these.
            pullView(
              number: 133,
              title: 'Daemon upgrade',
              group: PullGroup.inProgress,
            ),
          ],
          onSelect: opened.add,
          onRetry: () {},
        ),
      ),
    );

    expect(find.text('PULL REQUESTS'), findsOneWidget);
    expect(find.byType(PrCard), findsNWidgets(2));
    expect(find.text('Quiet capture mode'), findsOneWidget);
    expect(find.text('Fix flaky calendar test'), findsOneWidget);
    expect(find.text('Daemon upgrade'), findsNothing);

    // The place itself is one link away.
    await tester.tap(find.text('Open pull requests'));
    expect(opened, [const Place(PlaceKind.pulls)]);
  });

  testWidgets('a factory ask opens its item and offers nothing that posts', (
    tester,
  ) async {
    final opened = <Place>[];
    final overview = OverviewModel(
      needsYou: [
        ask(
          'item-1:completion',
          'Merge and complete',
          5,
          kind: model.AskKind.completion,
          thread: thread('item-1', 'Reliable session recovery'),
          actions: const [model.AskAction('Merge and complete', primary: true)],
          source: model.AskSource.factory,
          project: '/home/u/marshal',
          scope: model.AskScope.projectWide,
        ),
      ],
      running: const [],
      needsYouTotal: 1,
      sources: const [],
      projects: const [],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview, opened: opened));

    // One way in, and it is the item's own thread: these asks carry the item's
    // version and reviewed identity, so nothing here would post anything.
    expect(find.text('Open in the factory'), findsOneWidget);
    expect(find.widgetWithText(Btn, 'Merge and complete'), findsNothing);

    await tester.tap(find.text('Open in the factory'));
    await tester.pump();
    expect(opened, [
      const Place(PlaceKind.thread, host: 'local', id: 'item-1'),
    ]);
  });

  testWidgets('every card names its source', (tester) async {
    final overview = OverviewModel(
      needsYou: [
        ask(
          'item-1:decision',
          'Which branch?',
          5,
          kind: model.AskKind.decision,
          thread: thread('item-1', 'Reliable session recovery'),
          source: model.AskSource.factory,
          project: '/home/u/marshal',
          scope: model.AskScope.projectWide,
        ),
        ask('tc-1', 'Approve once', 10),
      ],
      running: const [],
      needsYouTotal: 2,
      sources: const [],
      projects: const [],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    expect(find.text('Factory · marshal · Project-wide'), findsOneWidget);
    expect(find.text('Fleet · Everyone'), findsOneWidget);
  });

  testWidgets('a stale factory is named while the fleet’s rows stay', (
    tester,
  ) async {
    final overview = OverviewModel(
      needsYou: const [],
      running: const [],
      needsYouTotal: 0,
      sources: [
        SourceState(
          kind: SourceKind.factory,
          host: 'local',
          loaded: true,
          lastRead: shellNow.subtract(const Duration(minutes: 4)),
          failure: 'offline',
        ),
      ],
      projects: const [
        ProjectSummary(
          id: '/home/u/marshal',
          name: 'marshal',
          needsYou: 2,
          running: 1,
          ready: 0,
          failed: 0,
          idle: 0,
        ),
      ],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    // The failed read is one row with its own age and the bridge's words.
    expect(find.text('Last updated 4m ago'), findsOneWidget);
    expect(find.textContaining('offline'), findsOneWidget);
    // A dead feed does not replace the page: the projects stay.
    expect(find.text('marshal'), findsOneWidget);
    expect(find.text('2 needs you · 1 running'), findsOneWidget);
  });

  testWidgets('a loaded factory is not hidden by an unreachable fleet', (
    tester,
  ) async {
    final overview = OverviewModel(
      needsYou: [
        ask(
          'item-1:completion',
          'Merge and complete',
          5,
          kind: model.AskKind.completion,
          thread: thread('item-1', 'Reliable session recovery'),
          source: model.AskSource.factory,
          project: '/home/u/marshal',
          scope: model.AskScope.projectWide,
        ),
      ],
      running: const [],
      needsYouTotal: 1,
      sources: [
        SourceState(
          kind: SourceKind.factory,
          host: 'local',
          loaded: true,
          lastRead: shellNow,
        ),
      ],
      projects: const [],
      capacity: const [],
      spend: null,
    );
    await tester.pumpWidget(show(overview, state: StateKind.unavailable));

    // The factory answered, so its asks are drawn; the whole-page message is
    // not.
    expect(find.byType(AskCard), findsOneWidget);
    final whole = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .where((message) => message.kind == StateKind.unavailable);
    expect(whole, isEmpty);
  });

  testWidgets('the F3 placeholder is gone', (tester) async {
    const overview = OverviewModel(
      needsYou: [],
      running: [],
      needsYouTotal: 0,
      sources: [],
      projects: [],
      capacity: [],
      spend: null,
    );
    await tester.pumpWidget(show(overview));

    expect(
      find.text('Activity arrives with the thread screen in F3.'),
      findsNothing,
    );
  });
}
