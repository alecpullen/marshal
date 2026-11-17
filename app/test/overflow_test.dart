import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import 'support/screens.dart';

/// design-system.md §4 and §11: no screen overflows or scrolls sideways at
/// 390, 800 or 1440 wide, or with the text at twice its size. Each
/// registered screen is pumped in every case at each width and scale; an
/// overflow is a layout error the test binding catches, and a sideways
/// scroll is any horizontal scrollable with somewhere to scroll to.
void main() {
  const widths = [390.0, 800.0, 1440.0];
  const scales = [1.0, 2.0];

  for (final screen in screens) {
    for (final MapEntry(key: name, value: build) in screen.cases.entries) {
      for (final width in widths) {
        for (final scale in scales) {
          testWidgets('${screen.name} ($name) at $width, text ×$scale, fits', (
            tester,
          ) async {
            tester.view.physicalSize = Size(width, 900);
            tester.view.devicePixelRatio = 1;
            tester.platformDispatcher.textScaleFactorTestValue = scale;
            addTearDown(tester.view.reset);
            addTearDown(
              tester.platformDispatcher.clearTextScaleFactorTestValue,
            );

            await tester.pumpWidget(
              EmberApp(brightness: Brightness.light, home: build()),
            );

            final error = tester.takeException();
            expect(error, isNull, reason: '$error');

            final sideways = [
              for (final s in tester.stateList<ScrollableState>(
                find.byType(Scrollable),
              ))
                if (s.position.axis == Axis.horizontal &&
                    s.position.maxScrollExtent > 0)
                  s.widget,
            ];
            expect(sideways, isEmpty, reason: 'scrolls sideways: $sideways');
          });
        }
      }
    }
  }
}
