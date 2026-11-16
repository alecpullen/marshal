import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/section_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/sections/section_page.dart';
import 'package:marshal_app/src/ui/sections/unsaved_guard.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

/// A section over strings, whose write a test scripts.
class _Section extends SectionModel<String> {
  _Section(this.rows);

  List<String> rows;
  Future<String> Function(String item) write = (item) async => item;

  @override
  Future<List<String>> fetch() async => rows;

  @override
  Object keyOf(String item) => item;

  @override
  bool isNew(String item) => !rows.contains(item);

  @override
  Future<String> persist(String item) => write(item);
}

/// The section page under the kit, with fixture rows.
Future<void> _pump(
  WidgetTester tester,
  _Section model, {
  double width = 1000,
}) async {
  tester.view.physicalSize = Size(width, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: SectionPage<String>(
        title: 'Schedules',
        model: model,
        row: (item, onTap) => RowItem(
          name: item,
          description: 'The nightly gate, on a cron',
          value: 'Daily',
          onTap: onTap,
        ),
        detail: (context, item) => Text('Editing $item'),
      ),
    ),
  );
}

void main() {
  testWidgets('save shows the refusal in place', (tester) async {
    final model = _Section(['nightly']);
    model.write = (_) async =>
        throw BridgeFailure(500, '{"error":"A schedule needs a recipe"}');
    await model.load();

    await _pump(tester, model);
    await tester.tap(find.text('nightly'));
    await tester.pumpAndSettle();

    model.change('nightly gate');
    await tester.pump();
    expect(find.text('Editing nightly gate'), findsOneWidget);

    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    // The bridge's own words, where the edit is, and the edit is kept.
    expect(find.text('A schedule needs a recipe'), findsOneWidget);
    expect(find.text('Editing nightly gate'), findsOneWidget);
    expect(model.editing, 'nightly gate');
    expect(model.saveState, SaveState.failed);
  });

  testWidgets('a stale save keeps the edit and offers reload', (tester) async {
    final model = _Section(['nightly']);
    model.write = (_) async =>
        throw BridgeFailure(409, 'Somebody else saved it first');
    await model.load();

    await _pump(tester, model);
    await tester.tap(find.text('nightly'));
    await tester.pumpAndSettle();
    model.change('nightly gate');
    await tester.pump();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    expect(model.saveState, SaveState.stale);
    expect(find.text('Somebody else saved it first'), findsOneWidget);
    expect(find.text('Reload'), findsOneWidget);
    expect(find.text('Editing nightly gate'), findsOneWidget);

    // Reload reads what the bridge holds now and keeps the edit.
    model.rows = ['nightly gate', 'weekly'];
    await tester.tap(find.text('Reload'));
    await tester.pumpAndSettle();
    expect(model.items, ['nightly gate', 'weekly']);
    expect(model.editing, 'nightly gate');
    expect(model.saveState, SaveState.dirty);
  });

  testWidgets('leaving with edits asks first', (tester) async {
    final model = _Section(['nightly']);
    await model.load();
    model.edit('nightly');
    model.change('nightly gate');
    final guard = UnsavedGuard()..watch(model);
    final selected = <Place>[];

    tester.view.physicalSize = const Size(900, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Shell(
          nav: NavModel(
            fixed: const [
              NavPlace(kind: PlaceKind.overview, label: 'Overview'),
            ],
            projects: const [],
          ),
          hosts: const [
            HostView(
              name: 'local',
              kind: HostKind.local,
              reach: HostReach.live,
              lastContact: null,
            ),
          ],
          place: const Place(PlaceKind.automations),
          content: const SizedBox.shrink(),
          onSelect: selected.add,
          unsaved: guard,
        ),
      ),
    );

    await tester.tap(find.text('Overview'));
    await tester.pumpAndSettle();
    expect(find.text('Leave without saving?'), findsOneWidget);
    expect(selected, isEmpty);

    // Dismissing the sheet is not an answer: the person stays put.
    await tester.tap(find.text('Keep editing'));
    await tester.pumpAndSettle();
    expect(selected, isEmpty);
    expect(model.hasUnsavedEdits, isTrue);

    await tester.tap(find.text('Overview'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Discard the edit'));
    await tester.pumpAndSettle();
    expect(selected.single.kind, PlaceKind.overview);
    expect(model.hasUnsavedEdits, isFalse);
  });

  testWidgets('a detail is a sheet at 760 and wider', (tester) async {
    final model = _Section(['nightly']);
    await model.load();
    await _pump(tester, model, width: 760);
    await tester.tap(find.text('nightly'));
    await tester.pumpAndSettle();
    expect(find.byType(Sheet), findsOneWidget);
    expect(find.text('Editing nightly'), findsOneWidget);
  });

  testWidgets('a detail is a page below 760', (tester) async {
    final model = _Section(['nightly']);
    await model.load();
    await _pump(tester, model, width: 390);
    await tester.tap(find.text('nightly'));
    await tester.pumpAndSettle();
    expect(find.byType(Sheet), findsNothing);
    // The detail's own page; the list's is offstage behind it.
    expect(find.byType(PageBody), findsOneWidget);
    expect(find.text('Editing nightly'), findsOneWidget);
  });
}
