import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_header.dart';

/// The header names the project as the sidebar does, with the root in the
/// tooltip where a long path stays readable (design-system.md §8, §11); its
/// headline is the live work, so it is not drawn while the thread waits on a
/// person — the ask in the conversation is where that question is read.
void main() {
  model.ThreadRef thread({
    model.ThreadStatus status = model.ThreadStatus.running,
    String headline = 'Removing the fixtures nobody reads',
  }) => model.ThreadRef(
    host: 'local',
    id: 'a-1',
    project: '/home/u/marshal',
    title: 'Clean up the fixtures',
    kind: model.ThreadKind.agent,
    status: status,
    headline: headline,
    lastActive: DateTime.utc(2026, 11, 17, 4, 10),
  );

  Future<void> pumpHeader(
    WidgetTester tester,
    model.ThreadRef threadRef,
  ) async {
    tester.view.physicalSize = const Size(1200, 200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: ThreadHeader(thread: threadRef),
      ),
    );
  }

  testWidgets('the identity line names the project, the tooltip its root', (
    tester,
  ) async {
    await pumpHeader(tester, thread());

    expect(find.text('marshal  ›  Agent  ·  local'), findsOneWidget);
    final tip = tester.widget<Tip>(
      find.ancestor(
        of: find.text('marshal  ›  Agent  ·  local'),
        matching: find.byType(Tip),
      ),
    );
    expect(tip.message, '/home/u/marshal  ›  Agent  ·  local');
  });

  testWidgets('a thread that needs you has no headline', (tester) async {
    await pumpHeader(
      tester,
      thread(
        status: model.ThreadStatus.needsYou,
        headline: 'Run the fixture cleanup?',
      ),
    );

    expect(find.text('Clean up the fixtures'), findsOneWidget);
    expect(find.text('Run the fixture cleanup?'), findsNothing);
  });

  testWidgets('a running thread still shows its headline', (tester) async {
    await pumpHeader(tester, thread());

    expect(find.text('Removing the fixtures nobody reads'), findsOneWidget);
  });
}
