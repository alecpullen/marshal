import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/files_model.dart';
import 'package:marshal_app/src/ui/inspector/files_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../support/kit.dart';

FilesModel _model({Set<String> changed = const {}}) => FilesModel.seeded(
  changed: changed,
  root: '/home/u/marshal',
  tree: const {
    '': [
      FileEntry(name: 'internal', dir: true, size: 0),
      FileEntry(name: 'scripts', dir: true, size: 0),
      FileEntry(name: 'go.mod', dir: false, size: 412),
      FileEntry(name: 'README.md', dir: false, size: 900),
    ],
    'internal': [FileEntry(name: 'symbols.go', dir: false, size: 200)],
  },
  files: const {
    'go.mod': 'module marshal\n\ngo 1.25\n',
    'README.md': '# Marshal\n',
  },
);

void main() {
  testWidgets('the tree is read a directory at a time, and opens a file', (
    tester,
  ) async {
    final model = _model();
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: FilesTab(model: model)),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    // The root is open; its children are named.
    expect(find.text('internal'), findsOneWidget);
    expect(find.text('go.mod'), findsOneWidget);

    // A directory opens to its entries.
    await tester.tap(find.text('internal'));
    await tester.pumpAndSettle();
    expect(find.text('symbols.go'), findsOneWidget);

    // A file opens in the viewer, in mono.
    await tester.tap(find.text('go.mod'));
    await tester.pumpAndSettle();
    expect(find.textContaining('module marshal'), findsOneWidget);
  });

  testWidgets('a changed file carries a marker with a tooltip', (tester) async {
    final model = _model(changed: const {'go.mod'});
    await pumpKit(
      tester,
      SizedBox(width: 400, height: 800, child: FilesTab(model: model)),
      width: 500,
      height: 900,
    );
    await tester.pumpAndSettle();

    final tips = tester
        .widgetList<Tip>(find.byType(Tip))
        .map((tip) => tip.message)
        .toList();
    expect(tips.where((tip) => tip == 'Changed in this thread'), hasLength(1));
  });

  testWidgets('each §7 state is drawn', (tester) async {
    for (final state in StateKind.values) {
      await pumpKit(
        tester,
        SizedBox(
          width: 400,
          height: 600,
          child: FilesTab(model: _model(), state: state),
        ),
        width: 500,
        height: 800,
      );
      expect(find.byType(StateMessage), findsOneWidget, reason: state.name);
    }
  });
}
