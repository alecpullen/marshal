import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

/// `StageStrip` (design-system.md §5, F6 decision 2): the stages as a bar and
/// as words, so a stage is never colour alone.
///
/// The words are one per segment of the bar, with the same gaps the bar draws,
/// so each stage is named under the segment it is.
void main() {
  testWidgets('each stage word starts under its segment', (tester) async {
    const labels = ['Plan', 'Implement', 'Review', 'Ready'];
    await pumpKit(
      tester,
      const SizedBox(
        width: 480,
        child: StageStrip(labels: labels, current: 1, status: Status.running),
      ),
    );

    final segments = find.descendant(
      of: find.byType(Segments),
      matching: find.byType(Container),
    );
    expect(segments, findsNWidgets(labels.length));

    for (final (index, label) in labels.indexed) {
      expect(
        tester.getTopLeft(find.text(label)).dx,
        moreOrLessEquals(tester.getTopLeft(segments.at(index)).dx),
        reason: 'the $label word starts under its own segment',
      );
    }
  });
}
