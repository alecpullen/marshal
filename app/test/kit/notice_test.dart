import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  const oneLine =
      'Context compacted: the earlier turns were summarised to fit the window.';
  const warning =
      'Snapshot not recorded for acme-app (unrecorded): the repository is '
      'larger than the snapshot limit.\n'
      'No rollback snapshot was recorded, so the tool may proceed WITHOUT a '
      'rollback snapshot.\n'
      'Limit: 512 MiB of working tree.\n'
      'Recover: raise snapshot.max_bytes.';

  testWidgets('one line has no Details', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Notice(text: oneLine)),
    );
    expect(find.text(oneLine), findsOneWidget);
    expect(find.text('Details'), findsNothing);
    expect(find.text('Hide details'), findsNothing);
  });

  testWidgets('the rest is hidden until Details opens it', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Notice(text: warning)),
    );
    // Closed: the first line only, and the control that offers the rest.
    expect(find.textContaining('Snapshot not recorded'), findsOneWidget);
    expect(find.textContaining('Recover:'), findsNothing);
    expect(find.text('Details'), findsOneWidget);

    await tester.tap(find.text('Details'));
    await tester.pump();

    expect(find.textContaining('No rollback snapshot'), findsOneWidget);
    expect(find.textContaining('Recover:'), findsOneWidget);
    expect(find.text('Details'), findsNothing);
    expect(find.text('Hide details'), findsOneWidget);
  });

  testWidgets('Hide details folds it again', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Notice(text: warning)),
    );
    await tester.tap(find.text('Details'));
    await tester.pump();
    await tester.tap(find.text('Hide details'));
    await tester.pump();

    expect(find.textContaining('Recover:'), findsNothing);
    expect(find.text('Details'), findsOneWidget);
  });

  testWidgets('it is drawn in the notice style, not the reading one', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Notice(text: warning)),
    );
    final first = styleOf(tester, warning.split('\n').first);
    expect(first.fontSize, e.small.fontSize);
    expect(first.color, e.muted);
    expect(first.color, isNot(e.reading.color));
    expect(first.fontSize, isNot(e.reading.fontSize));
  });
}
