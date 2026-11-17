import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';

import '../support/pulls.dart';
import '../support/screens.dart';

/// The Overview (F2 decision 8): the three oldest asks, a tile that opens its
/// thread, a host row that says how long it has been away, and one action
/// when there is nothing at all.
void main() {
  model.Ask ask(String id, String title, int ageMinutes) => model.Ask(
    host: 'local',
    id: id,
    kind: model.AskKind.approval,
    thread: null,
    title: title,
    body: '',
    actions: const [model.AskAction('Approve once', primary: true)],
    since: shellNow.subtract(Duration(minutes: ageMinutes)),
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

  Widget show(OverviewModel overview, {List<Place>? opened}) => EmberApp(
    brightness: Brightness.light,
    home: Overview(
      overview: overview,
      now: shellNow,
      hosts: shellHosts,
      onSelect: (place) => opened?.add(place),
      onRetry: () {},
    ),
  );

  testWidgets('shows the three oldest asks', (tester) async {
    final overview = OverviewModel(
      needsYou: [
        for (final (index, age) in [50, 40, 30, 20, 10].indexed)
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

    expect(find.byType(AskCard), findsNWidgets(3));
    expect(find.text('ask 0'), findsOneWidget);
    expect(find.text('ask 2'), findsOneWidget);
    expect(find.text('ask 3'), findsNothing);
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
}
