import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/inspector/inspector.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import '../thread/support.dart';

/// The icon button that carries [tooltip]: how a test finds a control that
/// has no words of its own.
Finder iconButton(String tooltip) => find.byWidgetPredicate(
  (widget) => widget is IconBtn && widget.tooltip == tooltip,
);

/// The thread screen with an inspector beside it (f4-inspector.md decision
/// 3), at [width] by 900.
Future<void> pumpInspector(
  WidgetTester tester, {
  required InspectorState state,
  double width = 1440,
}) async {
  tester.view.physicalSize = Size(width, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  // A live step's dot pulses forever, which pumpAndSettle never settles; the
  // app honours the reduced-motion preference (§9).
  tester.platformDispatcher.accessibilityFeaturesTestValue =
      const FakeAccessibilityFeatures(disableAnimations: true);
  addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: ThreadScreen(
        thread: threadRef(),
        store: StackStore.seeded(transcriptFixture()),
        view: ThreadViewState(),
        inspector: state,
        inspectorBody: (tab) => Text('body ${tab.name}'),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('cycles three widths', (tester) async {
    final state = InspectorState();
    await pumpInspector(tester, state: state);

    expect(state.width, InspectorWidth.docked);
    expect(find.byType(Inspector), findsOneWidget);

    await tester.tap(iconButton('Expand for review'));
    await tester.pumpAndSettle();
    expect(state.width, InspectorWidth.expanded);
    // Expanded narrows the conversation to its step headlines.
    expect(find.text('Step number 1 mapped the fixtures'), findsWidgets);
    expect(
      find.text('Clean up the test fixtures nobody uses any more.'),
      findsNothing,
    );

    await tester.tap(iconButton('Dock the inspector'));
    await tester.pumpAndSettle();
    expect(state.width, InspectorWidth.collapsed);
    // Collapsed leaves a rail with one control, and the transcript back.
    expect(find.byType(Tabs), findsNothing);
    expect(
      find.text('Clean up the test fixtures nobody uses any more.'),
      findsOneWidget,
    );

    await tester.tap(iconButton('Show the inspector'));
    await tester.pumpAndSettle();
    expect(state.width, InspectorWidth.docked);
    expect(find.byType(Tabs), findsOneWidget);
  });

  testWidgets('sheet below 1080', (tester) async {
    final state = InspectorState();
    await pumpInspector(tester, state: state, width: 900);

    // No column: the header opens the inspector over the conversation.
    expect(find.byType(Inspector), findsNothing);
    await tester.tap(iconButton('Inspector'));
    await tester.pumpAndSettle();
    expect(find.byType(Sheet), findsOneWidget);
    expect(find.byType(Inspector), findsOneWidget);
  });

  testWidgets('selection switches to the node', (tester) async {
    final state = InspectorState();
    await pumpInspector(tester, state: state);
    expect(find.text('body changes'), findsOneWidget);

    state.select('step:2');
    await tester.pumpAndSettle();

    expect(state.tab, InspectorTab.inspect);
    expect(
      find.text('Following Step number 2 mapped the fixtures'),
      findsOneWidget,
    );
    expect(find.text('body inspect'), findsOneWidget);

    await tester.tap(find.widgetWithText(Btn, 'Back to live'));
    await tester.pumpAndSettle();
    expect(state.selectedNode, isNull);
    expect(find.textContaining('Following'), findsNothing);
  });

  testWidgets('pinned tab stays', (tester) async {
    final state = InspectorState(tab: InspectorTab.files);
    await pumpInspector(tester, state: state);

    await tester.tap(iconButton('Pin this tab, so selecting a step leaves it'));
    await tester.pumpAndSettle();
    expect(state.pinned, isTrue);

    state.select('step:2');
    await tester.pumpAndSettle();

    expect(state.tab, InspectorTab.files);
    expect(state.selectedNode, isNull);
    expect(find.text('body files'), findsOneWidget);
  });

  testWidgets('place restores tab and width', (tester) async {
    const place = Place(PlaceKind.thread, host: 'local', id: 'a-1');
    final state = InspectorState(
      width: InspectorWidth.expanded,
      tab: InspectorTab.preview,
    );

    final linked = state.applyTo(place);
    final path = formatPlace(linked);
    expect(path, '/thread/local/a-1?t=preview&w=expanded');

    final restored = InspectorState.fromPlace(parsePlace(path));
    expect(restored.width, InspectorWidth.expanded);
    expect(restored.tab, InspectorTab.preview);

    // A query this build does not honour is not part of the place.
    expect(
      parsePlace('/thread/local/a-1?filter=blocking'),
      const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
    );

    // The restored state draws that tab and that width.
    await pumpInspector(tester, state: restored);
    expect(find.text('body preview'), findsOneWidget);
    expect(find.text('Step number 1 mapped the fixtures'), findsWidgets);
  });
}
