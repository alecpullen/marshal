import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

/// `Lanes` (design-system.md §5, §10): a run's tasks by role — one row per
/// task, one column per lane, every cell a status dot beside its word.
///
/// The grid is a set of facts, not a control: a cell that opens a role's
/// conversation takes a tap and one without is read only, a cell is never
/// colour alone, and its tooltip says what its small line has no room for.
void main() {
  final e = tokens();

  const lanes = ['Implement', 'Verify', 'Review', 'Commit'];

  /// Two tasks: the first did every role and carries notes, the second is
  /// running in its first lane and pending in the rest.
  const rows = [
    LaneRow(
      title: 'Task 1 · Read the item',
      detail: 'Shipped',
      cells: [
        LaneCell(status: Status.ready, words: 'Passed'),
        LaneCell(
          status: Status.ready,
          words: 'Verified',
          note: 'go test ./... passed',
        ),
        LaneCell(status: Status.ready, words: 'Approved'),
        LaneCell(status: Status.ready, words: 'Committed', note: 'a1b2c3d'),
      ],
    ),
    LaneRow(
      title: 'Task 2 · Approve it',
      detail: 'Repairing',
      cells: [
        LaneCell(status: Status.running, words: 'Running'),
        LaneCell(status: Status.idle, words: 'Pending'),
        LaneCell(status: Status.failed, words: 'Failed', note: '1 round'),
        LaneCell(status: Status.idle, words: 'Waiting'),
      ],
    ),
  ];

  /// The same grid, with the first cell selectable and openable.
  Lanes view({VoidCallback? onPassed, bool selected = false}) => Lanes(
    lanes: lanes,
    rows: [
      LaneRow(
        title: rows.first.title,
        detail: rows.first.detail,
        cells: [
          LaneCell(
            status: Status.ready,
            words: 'Passed',
            selected: selected,
            onTap: onPassed,
          ),
          ...rows.first.cells.skip(1),
        ],
      ),
      rows.last,
    ],
  );

  testWidgets('a header per lane, and one row per task, in order', (
    tester,
  ) async {
    await pumpKit(tester, view());

    for (final lane in lanes) {
      expect(find.text(lane), findsOneWidget, reason: 'the $lane column');
    }
    final titles = [
      for (final row in rows) tester.getTopLeft(find.text(row.title)).dy,
    ];
    expect(titles.first, lessThan(titles.last), reason: 'tasks are in order');

    // One cell per task per lane, and the cells follow the lanes' own order.
    expect(find.byType(Dot), findsNWidgets(rows.length * lanes.length));
    final cells = [
      for (final word in ['Passed', 'Verified', 'Approved', 'Committed'])
        tester.getTopLeft(find.text(word)).dx,
    ];
    expect(cells, orderedEquals([...cells]..sort()));
  });

  testWidgets('a cell says its status in words and in its tooltip', (
    tester,
  ) async {
    await pumpKit(tester, view());

    expect(hasTip(tester, find.text('Passed'), 'Passed · Ready'), isTrue);
    expect(
      hasTip(
        tester,
        find.text('Verified'),
        'Verified · Ready · go test ./... passed',
      ),
      isTrue,
    );
    // The status is spelled out twice over: the mark's own tooltip and the
    // cell's, so no cell is colour alone (§6).
    expect(tester.widget<Dot>(find.byType(Dot).first).tooltip, 'Ready');
    expect(
      hasTip(tester, find.text('Failed'), 'Failed · Failed · 1 round'),
      isTrue,
    );
    expect(styleOf(tester, 'Passed'), e.small, reason: 'an unselected cell');
  });

  testWidgets('a cell that opens a role takes a tap; one without is a fact', (
    tester,
  ) async {
    var opened = 0;
    await pumpKit(tester, view(onPassed: () => opened++));

    await tester.tap(find.text('Passed'));
    await tester.pump();
    expect(opened, 1);

    await tester.tap(find.text('Approved'), warnIfMissed: false);
    await tester.pump();
    expect(opened, 1, reason: 'a cell with no role behind it is read only');
    expect(tester.takeException(), isNull);
  });

  testWidgets('the selected cell reads as the one the screen is showing', (
    tester,
  ) async {
    await pumpKit(tester, view(selected: true));
    expect(styleOf(tester, 'Passed'), e.bodyStrong);
    expect(styleOf(tester, 'Verified'), e.small);
  });

  testWidgets('a row names its task, with its detail beneath', (tester) async {
    await pumpKit(tester, view());
    expect(styleOf(tester, rows.first.title), e.bodyStrong);
    expect(styleOf(tester, 'Shipped'), e.small.tint(e.muted));
    expect(
      tester.getTopLeft(find.text('Shipped')).dy,
      greaterThan(tester.getTopLeft(find.text(rows.first.title)).dy),
    );
  });

  testWidgets('a row’s name is inset from the box', (tester) async {
    await pumpKit(
      tester,
      SizedBox(
        width: 600,
        child: lanesBox(const Lanes(lanes: lanes, rows: rows)),
      ),
    );

    // The box draws its own border; the rows inside it sit `s16` in from it,
    // which is the inset `Rows` uses in a `Box.flush`.
    final box = tester.getTopLeft(find.byType(Box)).dx;
    final inset = box + Sizes.border + Space.s16;
    expect(
      tester.getTopLeft(find.text(rows.first.title)).dx,
      moreOrLessEquals(inset),
      reason: 'the first task’s name',
    );
    expect(
      tester.getTopLeft(find.text(rows.last.title)).dx,
      moreOrLessEquals(inset),
      reason: 'every task’s name',
    );

    // The header shares the rows' geometry: a lane's name starts where that
    // lane's column starts.
    expect(
      tester.getTopLeft(find.text('Implement')).dx,
      moreOrLessEquals(tester.getTopLeft(find.byType(Dot).first).dx),
    );
  });

  testWidgets('a note is its own mono line, and a long cell ellipsizes', (
    tester,
  ) async {
    await pumpKit(tester, view());

    final note = tester.widget<Text>(find.text('go test ./... passed'));
    expect(note.style!.fontFamily, Ember.monoFamily);
    expect(note.maxLines, 2);
    expect(note.overflow, TextOverflow.ellipsis);

    final word = tester.widget<Text>(find.text('Passed'));
    expect(word.maxLines, 1);
    expect(word.overflow, TextOverflow.ellipsis);

    // A long task title, detail and cell in a narrow column: each is cut with
    // an ellipsis rather than overflowing (§4).
    await pumpKit(
      tester,
      const SizedBox(
        width: 320,
        child: Lanes(
          lanes: lanes,
          rows: [
            LaneRow(
              title:
                  'Task 1 · A task title that runs on for a very long while '
                  'indeed',
              detail: 'A detail that also runs on and on',
              cells: [
                LaneCell(
                  status: Status.idle,
                  words: 'Pending for a long while',
                ),
                LaneCell(status: Status.running, words: 'Running for a while'),
                LaneCell(
                  status: Status.failed,
                  words: 'Failed',
                  note: 'A note that runs on and on and on',
                ),
                LaneCell(status: Status.idle, words: 'Waiting'),
              ],
            ),
          ],
        ),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
