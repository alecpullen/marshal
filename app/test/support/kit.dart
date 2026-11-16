import 'package:flutter/gestures.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// Pumps [child] under the kit's root at a window of [width] × [height],
/// pinned to the top left with a 16 margin.
Future<void> pumpKit(
  WidgetTester tester,
  Widget child, {
  Brightness brightness = Brightness.light,
  double width = 800,
  double height = 600,
  bool reduceMotion = false,
}) async {
  tester.view.physicalSize = Size(width, height);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  if (reduceMotion) {
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
  }
  await tester.pumpWidget(
    EmberApp(
      brightness: brightness,
      home: Align(
        alignment: Alignment.topLeft,
        child: Padding(padding: const EdgeInsets.all(16), child: child),
      ),
    ),
  );
}

/// The tokens a test pumped with [brightness].
Ember tokens([Brightness brightness = Brightness.light]) =>
    Ember.resolve(brightness);

/// The first box decoration painted under [of].
BoxDecoration decorationUnder(WidgetTester tester, Finder of) {
  final boxes = tester.widgetList<DecoratedBox>(
    find.descendant(of: of, matching: find.byType(DecoratedBox)),
  );
  return boxes.first.decoration as BoxDecoration;
}

/// Every box decoration painted under [of], outermost first.
List<BoxDecoration> decorationsUnder(WidgetTester tester, Finder of) => [
  for (final box in tester.widgetList<DecoratedBox>(
    find.descendant(of: of, matching: find.byType(DecoratedBox)),
  ))
    if (box.decoration is BoxDecoration) box.decoration as BoxDecoration,
];

/// The colour of a uniform border.
Color borderColour(BoxDecoration decoration) =>
    (decoration.border! as Border).top.color;

/// Moves a mouse over [finder] and leaves it there.
Future<TestGesture> hover(WidgetTester tester, Finder finder) async {
  final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
  await mouse.addPointer(location: Offset.zero);
  addTearDown(mouse.removePointer);
  await mouse.moveTo(tester.getCenter(finder));
  await tester.pump();
  return mouse;
}

/// The text style of the [Text] showing [text].
TextStyle styleOf(WidgetTester tester, String text) =>
    tester.widget<Text>(find.text(text)).style!;

/// Whether some [Tip] above [finder] says [message].
bool hasTip(WidgetTester tester, Finder finder, String message) => tester
    .widgetList<Tip>(find.ancestor(of: finder, matching: find.byType(Tip)))
    .any((tip) => tip.message == message);
