import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import 'support.dart';

void main() {
  // The screen with an inspector beside its transcript. The inspector is a
  // column only at or above the medium breakpoint, so the window is wide
  // enough for one; below it the panel is a sheet the header opens.
  Future<void> pumpInspector(WidgetTester tester, model.ThreadRef thread) async {
    tester.view.physicalSize = const Size(1280, 800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    // A live step's dot pulses forever, which pumpAndSettle never settles.
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
    final store = StackStore.seeded(transcriptFixture());
    addTearDown(store.dispose);
    final view = ThreadViewState();
    addTearDown(view.dispose);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: ThreadScreen(
          thread: thread,
          store: store,
          view: view,
          inspector: InspectorState(),
          inspectorBody: (_) => const SizedBox(),
        ),
      ),
    );
    await tester.pumpAndSettle();
  }

  testWidgets('an agent thread shows the five inspector tabs', (tester) async {
    await pumpInspector(tester, threadRef());

    for (final label in const [
      'Changes',
      'Files',
      'Terminal',
      'Preview',
      'Inspect',
    ]) {
      expect(find.text(label), findsOneWidget, reason: '$label is a tab');
    }
  });

  testWidgets('a work item thread shows its own five tabs', (tester) async {
    await pumpInspector(
      tester,
      model.ThreadRef(
        host: 'local',
        id: 'a-1',
        project: '/home/u/marshal',
        title: 'Clean up the fixtures',
        kind: model.ThreadKind.workItem,
        status: model.ThreadStatus.running,
        headline: 'Removing the fixtures nobody reads',
        lastActive: DateTime.utc(2026, 11, 17, 4, 10),
      ),
    );

    for (final label in const [
      'Plan',
      'Agents',
      'Checks',
      'Changes',
      'Pull requests',
    ]) {
      expect(find.text(label), findsOneWidget, reason: '$label is a tab');
    }
    expect(find.text('Files'), findsNothing);
  });
}
