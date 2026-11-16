import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/changes_model.dart';
import 'package:marshal_app/src/ui/inspector/changes_tab.dart';
import 'package:marshal_app/src/ui/inspector/ship_sheet.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../support/kit.dart';

/// The Btn that carries [label], so a test can ask whether it is enabled.
Btn button(WidgetTester tester, String label) =>
    tester.widget<Btn>(find.widgetWithText(Btn, label));

void main() {
  testWidgets('a failed gate requires a reason', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(
        ok: false,
        skipped: false,
        failedCommand: 'go test ./...',
      ),
      draft: const CommitDraft(message: 'f4: the inspector'),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
      ],
    );

    await pumpKit(tester, shipSheet(model));

    // The action is Override and ship, and it does nothing until a reason is
    // typed.
    expect(find.text('Override and ship'), findsOneWidget);
    expect(button(tester, 'Override and ship').onTap, isNull);
    expect(find.textContaining('kept in the audit log'), findsOneWidget);

    await tester.enterText(
      find.byType(Field).last,
      'the flaky test is unrelated to this change',
    );
    await tester.pump();

    expect(button(tester, 'Override and ship').onTap, isNotNull);
  });

  testWidgets('a gate that passed ships without a reason', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      draft: const CommitDraft(message: 'f4: the inspector'),
    );
    await pumpKit(tester, shipSheet(model));

    expect(find.text('Ship'), findsOneWidget);
    expect(find.text('Why this can ship anyway'), findsNothing);
    expect(button(tester, 'Ship').onTap, isNotNull);
  });

  testWidgets('the pull request choice is one control, and it says its state', (
    tester,
  ) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      draft: const CommitDraft(message: 'm'),
    );
    await pumpKit(tester, shipSheet(model));
    expect(find.text('Open a pull request'), findsOneWidget);

    await tester.tap(find.text('Open a pull request'));
    await tester.pump();
    expect(find.text('Push without a pull request'), findsOneWidget);
  });

  testWidgets('a pull request result links to it', (tester) async {
    await pumpKit(
      tester,
      const ShipOutcomeView(
        result: ShipResult(
          ShipOutcome.pullRequest,
          url: 'https://example.com/marshal/pull/7',
          message: 'f4-inspector',
        ),
      ),
    );
    expect(find.text('Pull request opened'), findsOneWidget);
    expect(find.text('https://example.com/marshal/pull/7'), findsOneWidget);
    expect(find.text('Copy link'), findsOneWidget);
    expect(find.text('Done'), findsOneWidget);
  });

  testWidgets('a pushed branch says what to do next', (tester) async {
    await pumpKit(
      tester,
      const ShipOutcomeView(
        result: ShipResult(ShipOutcome.pushed, message: 'f4-inspector'),
      ),
    );
    expect(find.text('Branch pushed'), findsOneWidget);
    expect(
      find.textContaining('Open a pull request on the forge'),
      findsOneWidget,
    );
    expect(find.text('f4-inspector'), findsOneWidget);
  });

  testWidgets('a gate refusal names what failed', (tester) async {
    await pumpKit(
      tester,
      const ShipOutcomeView(
        result: ShipResult(
          ShipOutcome.gateRefused,
          message: 'The gate refused: go test ./... failed.',
        ),
      ),
    );
    expect(find.text('The gate refused'), findsOneWidget);
    expect(find.textContaining('go test ./...'), findsOneWidget);
    expect(find.text('Copy link'), findsNothing);
  });

  testWidgets('a forge refusal says nothing was pushed', (tester) async {
    await pumpKit(
      tester,
      const ShipOutcomeView(
        result: ShipResult(
          ShipOutcome.forgeRefused,
          message: 'forge: no credential',
        ),
      ),
    );
    expect(find.text('The forge refused'), findsOneWidget);
    expect(find.textContaining('no credential'), findsOneWidget);
  });

  testWidgets('a patch is shown, and copied', (tester) async {
    await pumpKit(
      tester,
      const ShipOutcomeView(
        result: ShipResult(
          ShipOutcome.patch,
          message: 'This source is read-only',
        ),
        patch: 'diff --git a/a.go b/a.go\n+one\n',
      ),
    );
    expect(find.text('A patch, not a push'), findsOneWidget);
    expect(find.text('Copy patch'), findsOneWidget);
    expect(find.textContaining('diff --git a/a.go b/a.go'), findsOneWidget);
  });

  testWidgets('discard confirms, and names what is lost', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
        DiffFile(path: 'internal/acp/new.go', added: 7, removed: 0),
      ],
    );
    tester.view.physicalSize = const Size(500, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: Align(
          alignment: Alignment.topLeft,
          child: SizedBox(
            width: 400,
            height: 800,
            child: ChangesTab(model: model),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(model.files, hasLength(2));

    await tester.tap(find.widgetWithText(Btn, 'Discard'));
    await tester.pumpAndSettle();
    expect(find.text('Discard this work?'), findsOneWidget);
    expect(find.textContaining('2 changed files'), findsOneWidget);
    expect(find.textContaining('cannot be undone'), findsOneWidget);

    // Keeping the work discards nothing.
    await tester.tap(find.text('Keep the work'));
    await tester.pumpAndSettle();
    expect(model.files, hasLength(2));

    // Discarding the work does, and the tab says there is nothing to show.
    await tester.tap(find.widgetWithText(Btn, 'Discard'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Discard the work'));
    await tester.pumpAndSettle();
    expect(model.files, isEmpty);
    expect(find.textContaining('has not changed a file yet'), findsOneWidget);
  });
}
