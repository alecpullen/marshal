import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/drafts_model.dart';
import 'package:marshal_app/src/ui/factory/draft_conflict.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// One draft's content, as the editor holds it.
WorkingDraftContent _content(String title) => WorkingDraftContent(
  title: title,
  plan: '',
  spec: '',
  repoID: 'repo-1',
  targetBranchOverride: '',
  workspaceOverride: '',
  implementerOverride: '',
  reviewerOverride: '',
  reviewerInstructions: '',
  maxRoundsOverride: '',
  ciPollOverride: '',
  ciWaitOverride: '',
  checksMode: 'inherit',
  expectedChecksText: '',
  native: true,
  nativeChosen: false,
  implementerAuto: true,
  noRequiredCIAcknowledged: false,
);

/// The bridge's answer to one draft read.
WorkingDraft _draft(
  String id, {
  int version = 1,
  String title = 'Mine',
  String state = 'editing',
}) => WorkingDraft(
  id: id,
  projectRoot: '/home/u/marshal',
  version: version,
  state: state,
  content: _content(title),
  updatedAt: '2026-11-17T04:00:00Z',
);

/// A drafts model holding the bridge's version 3, whose saves answer
/// [saveFailure] or succeed, so a test can stand in any persistence state
/// without a bridge. The autosave window is long enough never to fire on its
/// own: a test drives the writes it means.
DraftsModel _model({BridgeFailure? saveFailure, String state = 'editing'}) {
  var held = _draft('d-1', version: 3, state: state);
  final model = DraftsModel(
    projectRoot: '/home/u/marshal',
    autosave: const Duration(seconds: 30),
    create: (request) async => _draft('d-2', title: request.content.title),
    save: (id, request) async {
      if (saveFailure != null) throw saveFailure;
      held = _draft(
        id,
        version: held.version + 1,
        title: request.content.title,
      );
      return held;
    },
    // The version that won, which a 409 is read back as.
    read: (id) async => _draft(id, version: 5, title: 'Their title'),
    list: (root) async => [held],
    remove: (id, request) async {},
  );
  model.adopt(_draft('d-1', version: 3, state: state));
  return model;
}

/// The line on a constrained surface, in the light theme, with the conflict
/// panel beneath it when a test wants both.
Widget _surface(
  DraftsModel model, {
  Widget? below,
  double width = 420,
}) => EmberApp(
  brightness: Brightness.light,
  home: Center(
    child: SizedBox(
      width: width,
      child: Padding(
        padding: const EdgeInsets.all(Space.s16),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            DraftPersistenceLine(drafts: model),
            if (below != null) ...[const SizedBox(height: Space.s16), below],
          ],
        ),
      ),
    ),
  ),
);

void main() {
  testWidgets('queued edits say the text has not reached the bridge', (
    tester,
  ) async {
    final model = _model();
    addTearDown(model.dispose);
    model.scheduleSave(_content('Typed'));

    await tester.pumpWidget(_surface(model));

    expect(find.text('Only in this editor'), findsOneWidget);
    expect(find.textContaining('has not taken the text'), findsOneWidget);
    expect(find.byIcon(Glyphs.pencil), findsOneWidget);
    expect(
      find.text('Saved to the bridge'),
      findsNothing,
      reason: "queued text is not claimed as the bridge's",
    );
  });

  testWidgets('a saved draft says the bridge holds it', (tester) async {
    final model = _model();
    addTearDown(model.dispose);

    await tester.pumpWidget(_surface(model));

    expect(find.text('Saved to the bridge'), findsOneWidget);
    expect(find.textContaining('another device'), findsOneWidget);
    expect(find.textContaining('survive a reload'), findsOneWidget);
    expect(find.byIcon(Glyphs.checkmark_circle_fill), findsOneWidget);
  });

  testWidgets('a failed save says the text did not reach the bridge', (
    tester,
  ) async {
    final model = _model(saveFailure: BridgeFailure(503, 'bridge unavailable'));
    addTearDown(model.dispose);
    model.scheduleSave(_content('Typed'));
    await model.saveNow();

    await tester.pumpWidget(_surface(model));

    expect(find.text('Not saved'), findsOneWidget);
    expect(find.textContaining('did not reach the bridge'), findsOneWidget);
    expect(find.textContaining('nothing was sent again'), findsOneWidget);
    expect(find.text('bridge unavailable'), findsOneWidget);
    expect(find.byIcon(Glyphs.xmark_circle_fill), findsOneWidget);
  });

  testWidgets('a converted draft reads as read only', (tester) async {
    final model = _model(state: 'converted');
    addTearDown(model.dispose);

    await tester.pumpWidget(_surface(model));

    expect(find.text('Converted, read only'), findsOneWidget);
    expect(find.textContaining('became a work item'), findsOneWidget);
    expect(find.byIcon(Glyphs.lock), findsOneWidget);
  });

  testWidgets('the line follows the model as it saves', (tester) async {
    final model = _model();
    addTearDown(model.dispose);
    model.scheduleSave(_content('Typed'));
    await tester.pumpWidget(_surface(model));
    expect(find.text('Only in this editor'), findsOneWidget);

    await model.saveNow();
    await tester.pump();

    expect(find.text('Saved to the bridge'), findsOneWidget);
    expect(find.text('Only in this editor'), findsNothing);
  });

  testWidgets('a changed-elsewhere conflict still shows both versions and '
      'both choices', (tester) async {
    final model = _model(saveFailure: BridgeFailure(409, 'conflict'));
    addTearDown(model.dispose);
    model.scheduleSave(_content('Mine'));
    await model.saveNow();
    expect(model.conflict, DraftConflict.changedElsewhere);

    await tester.pumpWidget(
      _surface(model, below: DraftConflictBar(drafts: model)),
    );

    // The line names the state; the panel keeps both versions and the two
    // choices exactly as they were.
    expect(find.text('Changed elsewhere'), findsOneWidget);
    expect(find.text('This draft changed elsewhere'), findsOneWidget);
    expect(find.text('Yours'), findsOneWidget);
    expect(find.text('Theirs'), findsOneWidget);
    expect(find.text('Their title'), findsOneWidget);
    expect(find.text('Version 5'), findsOneWidget);
    expect(find.text('Keep mine'), findsOneWidget);
    expect(find.text('Take theirs'), findsOneWidget);
  });

  testWidgets('a deleted-elsewhere conflict still offers its two choices', (
    tester,
  ) async {
    final model = _model(saveFailure: BridgeFailure(404, 'not found'));
    addTearDown(model.dispose);
    model.scheduleSave(_content('Kept'));
    await model.saveNow();
    expect(model.conflict, DraftConflict.deletedElsewhere);

    await tester.pumpWidget(
      _surface(model, below: DraftConflictBar(drafts: model)),
    );

    expect(find.text('Deleted elsewhere'), findsOneWidget);
    expect(find.text('This draft was deleted elsewhere'), findsOneWidget);
    expect(find.text('Save as a new draft'), findsOneWidget);
    expect(find.text('Discard this work'), findsOneWidget);
  });

  // The conflict panel puts two versions side by side, so it is the one place
  // here that could overflow a phone. Neither size is covered by
  // overflow_test.dart, because this widget is composed into the editor rather
  // than registered as a screen.
  testWidgets('both versions fit a phone and a desktop', (tester) async {
    final model = _model(saveFailure: BridgeFailure(409, 'conflict'));
    addTearDown(model.dispose);
    model.scheduleSave(_content('Typed'));
    await model.saveNow();

    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    tester.view.physicalSize = const Size(390, 780);
    await tester.pumpWidget(
      _surface(
        model,
        below: DraftConflictBar(drafts: model),
        width: double.infinity,
      ),
    );
    await tester.pump();
    expect(tester.takeException(), isNull, reason: 'a phone overflows');
    expect(find.text('Yours'), findsOneWidget);
    expect(find.text('Theirs'), findsOneWidget);

    tester.view.physicalSize = const Size(1440, 900);
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull, reason: 'a desktop overflows');
  });
}
