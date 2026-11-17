import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/drafts_model.dart';

/// One draft's content, as the editor holds it.
WorkingDraftContent _content(String title, {String plan = ''}) =>
    WorkingDraftContent(
      title: title,
      plan: plan,
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

/// The bridge's answer to one draft read, version and title included.
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

/// A drafts model whose every route is recorded, so a test asserts exactly
/// what was written.
class _Routes {
  final creates = <WorkingDraftCreateRequest>[];
  final saves = <WorkingDraftSaveRequest>[];
  final reads = <String>[];

  /// The draft the bridge already holds: what a save is compared against and
  /// what a conflict read answers.
  WorkingDraft held = _draft('d-1', version: 3);

  /// What a save answers: null for the draft it wrote, or the failure the
  /// bridge would give.
  BridgeFailure? saveFailure;
  BridgeFailure? createFailure;

  /// When set, a save waits for it before answering, so a test can look at the
  /// model while the write is in flight.
  Completer<void>? hold;

  DraftsModel model({Duration autosave = const Duration(seconds: 2)}) =>
      DraftsModel(
        projectRoot: '/home/u/marshal',
        autosave: autosave,
        create: (request) async {
          creates.add(request);
          if (createFailure != null) throw createFailure!;
          held = _draft('d-2', title: request.content.title);
          return held;
        },
        save: (id, request) async {
          saves.add(request);
          if (hold != null) await hold!.future;
          if (saveFailure != null) throw saveFailure!;
          held = _draft(
            id,
            version: held.version + 1,
            title: request.content.title,
          );
          return held;
        },
        read: (id) async {
          reads.add(id);
          return held;
        },
        list: (root) async => [held],
        remove: (id, request) async {},
      );
}

void main() {
  test('autosaves after two seconds', () async {
    final routes = _Routes();
    // The default is the two seconds F6 decision 10 states.
    final model = DraftsModel(
      projectRoot: '/home/u/marshal',
      create: (request) async => _draft('d-1'),
      save: (id, request) async => _draft(id),
      read: (id) async => _draft(id),
      list: (root) async => const [],
      remove: (id, request) async {},
    );
    addTearDown(model.dispose);
    expect(model.autosave, const Duration(seconds: 2));

    // A model with a window a test can wait out: two edits inside the window
    // are one save, and it happens only after the window has passed.
    final fast = routes.model(autosave: const Duration(milliseconds: 40));
    addTearDown(fast.dispose);
    fast.adopt(routes.held);
    fast.scheduleSave(_content('First'));
    fast.scheduleSave(_content('Second'));

    expect(routes.saves, isEmpty, reason: 'nothing is saved before the window');
    await Future<void>.delayed(const Duration(milliseconds: 90));
    expect(routes.saves, hasLength(1), reason: 'one save for two edits');
    expect(routes.saves.single.content.title, 'Second');
  });

  test('a stale save shows both', () async {
    final routes = _Routes()..saveFailure = BridgeFailure(409, 'conflict');
    // The version that won, which the conflict read answers with.
    routes.held = _draft('d-1', version: 5, title: 'Theirs');
    final model = routes.model(autosave: const Duration(milliseconds: 10));
    addTearDown(model.dispose);
    model.adopt(_draft('d-1', version: 3, title: 'Mine'));

    model.scheduleSave(_content('Mine'));
    await Future<void>.delayed(const Duration(milliseconds: 40));

    expect(model.conflict, DraftConflict.changedElsewhere);
    expect(model.mine?.title, 'Mine');
    expect(model.theirsVersion?.title, 'Theirs');
    expect(model.theirsVersion?.version, 5);
  });

  test('keep mine overwrites with the new version', () async {
    final routes = _Routes()..saveFailure = BridgeFailure(409, 'conflict');
    routes.held = _draft('d-1', version: 5, title: 'Theirs');
    final model = routes.model(autosave: const Duration(milliseconds: 10));
    addTearDown(model.dispose);
    model.adopt(_draft('d-1', version: 3, title: 'Mine'));
    model.scheduleSave(_content('Mine'));
    await Future<void>.delayed(const Duration(milliseconds: 40));
    expect(model.conflict, DraftConflict.changedElsewhere);

    // The deliberate overrule names the version that won, not the stale one.
    routes.saveFailure = null;
    await model.keepMine();

    expect(routes.saves.last.expectedVersion, 5);
    expect(routes.saves.last.content.title, 'Mine');
    expect(model.conflict, DraftConflict.none);
  });

  test('take theirs replaces the form', () async {
    final routes = _Routes()..saveFailure = BridgeFailure(409, 'conflict');
    routes.held = _draft('d-1', version: 5, title: 'Theirs');
    final model = routes.model(autosave: const Duration(milliseconds: 10));
    addTearDown(model.dispose);
    model.adopt(_draft('d-1', version: 3, title: 'Mine'));
    model.scheduleSave(_content('Mine'));
    await Future<void>.delayed(const Duration(milliseconds: 40));

    await model.takeTheirs();

    expect(model.conflict, DraftConflict.none);
    expect(model.draft?.version, 5);
    expect(model.draft?.content.title, 'Theirs');
  });

  test('deleted elsewhere offers save as new', () async {
    final routes = _Routes()..saveFailure = BridgeFailure(404, 'not found');
    final model = routes.model(autosave: const Duration(milliseconds: 10));
    addTearDown(model.dispose);
    model.adopt(_draft('d-1', version: 3));
    model.scheduleSave(_content('Kept'));

    await Future<void>.delayed(const Duration(milliseconds: 40));

    expect(model.conflict, DraftConflict.deletedElsewhere);

    routes.saveFailure = null;
    await model.saveAsNew();

    expect(routes.creates, hasLength(1));
    expect(routes.creates.single.content.title, 'Kept');
    expect(routes.creates.single.projectRoot, '/home/u/marshal');
    expect(model.conflict, DraftConflict.none);
    expect(model.draftID, 'd-2');
  });

  test('a draft deleted elsewhere is not edited over', () async {
    final routes = _Routes()..saveFailure = BridgeFailure(404, 'not found');
    final model = routes.model(autosave: const Duration(milliseconds: 10));
    addTearDown(model.dispose);
    model.adopt(_draft('d-1', version: 3));
    model.scheduleSave(_content('Kept'));
    await Future<void>.delayed(const Duration(milliseconds: 40));

    expect(model.editable, isFalse);
    model.scheduleSave(_content('Typed after the deletion'));
    await Future<void>.delayed(const Duration(milliseconds: 40));

    // No second write: a deleted draft takes no save.
    expect(routes.saves, hasLength(1));
  });

  group('persistence', () {
    test('a draft the bridge holds is saved', () async {
      final routes = _Routes();
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);

      expect(
        model.persistence,
        DraftPersistence.localEdits,
        reason: 'no draft is open and nothing has been sent',
      );
      model.adopt(_draft('d-1', version: 3));

      expect(model.persistence, DraftPersistence.saved);
      expect(model.persistenceLabel, 'Saved to the bridge');
      expect(model.persistenceConsequence, contains('another device'));
      expect(
        model.persistenceConsequence,
        contains('reload'),
        reason: 'nothing is claimed about surviving a reload',
      );
    });

    test('edits queued for autosave are only in this editor', () async {
      final routes = _Routes();
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', version: 3));

      model.scheduleSave(_content('Typed'));

      expect(model.persistence, DraftPersistence.localEdits);
      expect(model.persistenceLabel, 'Only in this editor');
      expect(model.persistenceConsequence, contains('has not taken'));
      expect(routes.saves, isEmpty, reason: 'the window has not passed');
    });

    test('a write in flight reads as saving', () async {
      final routes = _Routes()..hold = Completer<void>();
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', version: 3));
      model.scheduleSave(_content('Typed'));

      final write = model.saveNow();
      await Future<void>.delayed(Duration.zero);
      expect(model.persistence, DraftPersistence.saving);
      expect(model.persistenceLabel, 'Saving…');
      expect(model.persistenceConsequence, contains('now'));

      routes.hold!.complete();
      await write;
      expect(model.persistence, DraftPersistence.saved);
    });

    test('edits typed during a write stay unsaved', () async {
      final routes = _Routes()..hold = Completer<void>();
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', version: 3));
      model.scheduleSave(_content('First'));

      final write = model.saveNow();
      await Future<void>.delayed(Duration.zero);
      expect(model.persistence, DraftPersistence.saving);

      // The person keeps typing while the bridge is taking the first text.
      model.scheduleSave(_content('Second'));

      routes.hold!.complete();
      await write;

      // The bridge holds the first text and nothing else. The newer edit is
      // still only here, so calling the draft "saved" would claim another
      // device sees words the bridge never received.
      expect(routes.saves.single.content.title, 'First');
      expect(model.persistence, DraftPersistence.localEdits);
      expect(model.persistenceLabel, 'Only in this editor');
    });

    test(
      'a failed save reports itself, keeps the text and retries nothing',
      () async {
        var offline = true;
        final saves = <WorkingDraftSaveRequest>[];
        final model = DraftsModel(
          projectRoot: '/home/u/marshal',
          autosave: const Duration(milliseconds: 10),
          create: (request) async => _draft('d-1'),
          save: (id, request) async {
            saves.add(request);
            if (offline) throw Exception('offline');
            return _draft(id, version: 4, title: request.content.title);
          },
          read: (id) async => _draft(id),
          list: (root) async => const [],
          remove: (id, request) async {},
        );
        addTearDown(model.dispose);
        model.adopt(_draft('d-1', version: 3));
        model.scheduleSave(_content('Typed'));

        await Future<void>.delayed(const Duration(milliseconds: 40));
        expect(model.persistence, DraftPersistence.failed);
        expect(model.persistenceLabel, 'Not saved');
        expect(model.persistenceConsequence, contains('did not reach'));
        expect(model.failure, contains('offline'));
        expect(saves, hasLength(1));

        // No blind retry: the window passes again and nothing is re-posted.
        await Future<void>.delayed(const Duration(milliseconds: 40));
        expect(
          saves,
          hasLength(1),
          reason: 'nothing is sent again automatically',
        );

        // The text is intact: a deliberate save still has it to send, under a
        // request of its own.
        offline = false;
        await model.saveNow();
        expect(saves, hasLength(2));
        expect(saves.last.content.title, 'Typed');
        expect(saves.last.requestID, isNot(saves.first.requestID));
        expect(model.persistence, DraftPersistence.saved);
      },
    );

    test('two editors conflict twice in a row', () async {
      final routes = _Routes()..saveFailure = BridgeFailure(409, 'conflict');
      routes.held = _draft('d-1', version: 5, title: 'Theirs 5');
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', version: 3, title: 'Mine'));
      model.scheduleSave(_content('Mine'));

      await model.saveNow();
      expect(model.persistence, DraftPersistence.conflict);
      expect(model.persistenceLabel, 'Changed elsewhere');
      expect(model.theirsVersion?.version, 5);

      // The other editor saves again while this one decides: the overrule is
      // stale too, and is refused rather than written over.
      routes.held = _draft('d-1', version: 6, title: 'Theirs 6');
      await model.keepMine();
      expect(model.conflict, DraftConflict.changedElsewhere);
      expect(model.theirsVersion?.version, 6);
      expect(model.persistence, DraftPersistence.conflict);

      routes.saveFailure = null;
      await model.keepMine();
      expect(model.conflict, DraftConflict.none);
      expect(model.persistence, DraftPersistence.saved);
      expect(
        [for (final save in routes.saves) save.expectedVersion],
        [3, 5, 6],
      );
      expect(
        routes.saves.map((save) => save.requestID).toSet(),
        hasLength(3),
        reason: 'each write names itself',
      );
    });

    test('a draft deleted elsewhere reads as a conflict, not a save', () async {
      final routes = _Routes()..saveFailure = BridgeFailure(404, 'not found');
      final model = routes.model(autosave: const Duration(seconds: 30));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', version: 3));
      model.scheduleSave(_content('Kept'));

      await model.saveNow();

      expect(model.persistence, DraftPersistence.conflict);
      expect(model.persistenceLabel, 'Deleted elsewhere');
      expect(model.persistenceConsequence, contains('deleted elsewhere'));
      expect(model.editable, isFalse);
    });

    test('a converted draft is read only', () async {
      final routes = _Routes();
      final model = routes.model(autosave: const Duration(milliseconds: 10));
      addTearDown(model.dispose);
      model.adopt(_draft('d-1', state: 'converted'));

      expect(model.persistence, DraftPersistence.readOnly);
      expect(model.persistenceLabel, 'Converted, read only');
      expect(model.persistenceConsequence, contains('read only'));

      model.scheduleSave(_content('Typed after conversion'));
      await Future<void>.delayed(const Duration(milliseconds: 40));
      expect(routes.saves, isEmpty, reason: 'a converted draft takes no save');
      expect(model.persistence, DraftPersistence.readOnly);
    });
  });
}
