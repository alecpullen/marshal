import 'dart:async';

import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import '../client/transport.dart';

// The routes are functions passed in, so their fields stay private: Dart has
// no private named parameter.
// ignore_for_file: prefer_initializing_formals

/// Whether a draft this editor holds is still the one the bridge has (F6
/// decision 10).
enum DraftConflict {
  /// The draft on screen is the draft the bridge holds.
  none,

  /// Someone saved this draft elsewhere since it was opened. Both versions are
  /// shown and the person says which to keep; nothing is overwritten.
  changedElsewhere,

  /// The draft was deleted elsewhere. Nothing can be saved over it, so the
  /// choice is to keep the work as a new draft or to discard it.
  deletedElsewhere,
}

/// Where the text on screen stands, persistence-wise: whether the bridge has
/// it, and therefore whether another device can see it (design-system.md §2,
/// §7: a state is words first, never colour alone).
///
/// Nothing here claims the text survives a reload: this editor keeps no copy
/// of its own, and the bridge is the only place a draft is stored.
enum DraftPersistence {
  /// Content is queued for the autosave and the bridge has not acknowledged
  /// it: another device still sees the last version the bridge saved, if any.
  localEdits,

  /// A write is in flight; the bridge has not answered yet.
  saving,

  /// The bridge holds the version on screen, so another device that opens the
  /// draft sees it.
  saved,

  /// The last write did not reach the bridge. The text is still here, and
  /// nothing was sent again automatically.
  failed,

  /// The bridge refused the write because the draft changed or was deleted
  /// elsewhere; [DraftsModel.conflict] names which.
  conflict,

  /// The draft is no longer `editing` (it is converting, converted or
  /// deleted), so its text cannot be saved from here.
  readOnly,
}

/// One draft version as a screen draws it (F6 decision 10): the version, the
/// title and the plan, without the wire type. `ui/` reads `model/` and never
/// `client/` (F1's boundary), so the projection lives here.
@immutable
class DraftVersion {
  const DraftVersion({
    required this.version,
    required this.title,
    required this.plan,
    required this.spec,
    required this.state,
  });

  /// Reads one wire draft.
  factory DraftVersion.of(WorkingDraft draft) => DraftVersion(
    version: draft.version,
    title: draft.content.title,
    plan: draft.content.plan,
    spec: draft.content.spec,
    state: draft.state,
  );

  /// The version this is, or null for a draft that was never saved.
  final int? version;
  final String title;
  final String plan;
  final String spec;
  final String state;
}

/// One project's unfinished handoffs (F6 decision 10): the draft being edited,
/// the autosave that keeps it, and the conflict a save the bridge refused
/// raises.
///
/// A save names the version the editor last read, so a draft edited on two
/// devices is never overwritten silently: the bridge refuses the stale save
/// (409) and this model reads back the version that won. A draft deleted
/// elsewhere (404) is its own conflict, because the answer is a new draft
/// rather than a choice between two.
class DraftsModel extends ChangeNotifier {
  DraftsModel({
    required this.projectRoot,
    required Future<WorkingDraft> Function(WorkingDraftCreateRequest request)
    create,
    required Future<WorkingDraft> Function(
      String id,
      WorkingDraftSaveRequest request,
    )
    save,
    required Future<WorkingDraft> Function(String id) read,
    required Future<List<WorkingDraft>> Function(String projectRoot) list,
    required Future<void> Function(String id, WorkingDraftDeleteRequest request)
    remove,
    this.autosave = const Duration(seconds: 2),
  }) : _create = create,
       _save = save,
       _read = read,
       _list = list,
       _remove = remove;

  /// The project whose drafts these are. A draft never moves between projects.
  final String projectRoot;

  /// How long after the last edit the autosave runs (F6 decision 10).
  final Duration autosave;

  final Future<WorkingDraft> Function(WorkingDraftCreateRequest request)
  _create;
  final Future<WorkingDraft> Function(
    String id,
    WorkingDraftSaveRequest request,
  )
  _save;
  final Future<WorkingDraft> Function(String id) _read;
  final Future<List<WorkingDraft>> Function(String projectRoot) _list;
  final Future<void> Function(String id, WorkingDraftDeleteRequest request)
  _remove;

  WorkingDraft? _draft;
  DraftConflict _conflict = DraftConflict.none;
  WorkingDraft? _theirs;
  List<WorkingDraft> _drafts = const [];
  WorkingDraftContent? _pending;
  Timer? _timer;
  String? _failure;
  bool _saving = false;
  int _requests = 0;

  /// Whether the text on screen holds edits the bridge has not acknowledged.
  /// Distinct from `_pending`, which a save leaves in place as the content the
  /// next deliberate write would send.
  bool _unsaved = false;

  /// Whether the last write did not reach the bridge. A *list* read that fails
  /// sets `_failure` but not this: nothing was being written, so it is a
  /// different promise. A conflict read that fails does set it, because the
  /// refusal that raised the read already means the text on screen was not
  /// stored, and the version that won it could not be read.
  bool _writeFailed = false;

  /// The draft this editor holds, or null before one is created.
  WorkingDraft? get draft => _draft;

  /// The draft's id, when it has one.
  String? get draftID => _draft?.id;

  /// Whether the draft on screen is the bridge's.
  DraftConflict get conflict => _conflict;

  /// The version that won, when [conflict] is [DraftConflict.changedElsewhere]:
  /// what "take theirs" adopts and what "keep mine" saves over.
  WorkingDraft? get theirs => _theirs;

  /// Every unfinished draft of the project, as a screen draws them.
  List<DraftVersion> get drafts =>
      List.unmodifiable([for (final draft in _drafts) DraftVersion.of(draft)]);

  /// The version this editor holds, or null before anything was saved.
  DraftVersion? get mine => _draft == null ? null : DraftVersion.of(_draft!);

  /// The version that won a stale save, or null.
  DraftVersion? get theirsVersion =>
      _theirs == null ? null : DraftVersion.of(_theirs!);

  /// Why the last save failed, in the bridge's words.
  String? get failure => _failure;

  /// Whether a save is in flight.
  bool get saving => _saving;

  /// Where the text on screen stands against the bridge.
  ///
  /// A conflict outranks everything, because no write may pass it; a draft the
  /// bridge no longer holds as `editing` is read only; and a write in flight is
  /// the newer fact than the failure a previous attempt left.
  DraftPersistence get persistence {
    if (_conflict != DraftConflict.none) return DraftPersistence.conflict;
    final draft = _draft;
    if (draft != null && draft.state != 'editing') {
      return DraftPersistence.readOnly;
    }
    if (_saving) return DraftPersistence.saving;
    if (_writeFailed) return DraftPersistence.failed;
    if (_unsaved) return DraftPersistence.localEdits;
    return draft == null ? DraftPersistence.localEdits : DraftPersistence.saved;
  }

  /// The short label a draft editor prints for [persistence]. Under a conflict
  /// it names which refusal stands, because the two answers differ.
  String get persistenceLabel => switch (persistence) {
    DraftPersistence.localEdits => 'Only in this editor',
    DraftPersistence.saving => 'Saving…',
    DraftPersistence.saved => 'Saved to the bridge',
    DraftPersistence.failed => 'Not saved',
    DraftPersistence.conflict =>
      _conflict == DraftConflict.deletedElsewhere
          ? 'Deleted elsewhere'
          : 'Changed elsewhere',
    DraftPersistence.readOnly => switch (_draft?.state) {
      'converting' => 'Converting, read only',
      'converted' => 'Converted, read only',
      'deleted' => 'Deleted, read only',
      _ => 'Read only',
    },
  };

  /// One sentence saying what [persistence] means in practice: whether another
  /// device can see the text, what has not reached the bridge, and that no
  /// reload is promised.
  String get persistenceConsequence => switch (persistence) {
    DraftPersistence.localEdits =>
      'The bridge has not taken the text on screen yet, so another device '
          'still sees the last version it saved, if any.',
    DraftPersistence.saving =>
      'The bridge is taking this version now, so another device sees it once '
          'the bridge answers.',
    DraftPersistence.saved =>
      'The bridge holds the text on screen, so another device that opens this '
          'draft sees it; nothing here promises to survive a reload.',
    DraftPersistence.failed =>
      'The last save did not reach the bridge, so this text exists only in '
          'this editor and nothing was sent again automatically.',
    DraftPersistence.conflict =>
      _conflict == DraftConflict.deletedElsewhere
          ? 'The draft was deleted elsewhere, so this work can be kept only as a '
                'new draft.'
          : 'Another device saved a newer version, so choose which one to keep '
                'before typing more.',
    DraftPersistence.readOnly => switch (_draft?.state) {
      'converting' =>
        'This draft is being converted into a work item, so its text is read '
            'only here.',
      'converted' =>
        'This draft became a work item, so its text is read only '
            'here.',
      'deleted' => 'This draft was deleted, so its text is read only here.',
      _ =>
        'This draft is no longer being edited, so its text is read only '
            'here.',
    },
  };

  /// Whether the draft on screen can be edited: a converted draft is read
  /// only, and a deleted one is not a draft at all.
  bool get editable =>
      _conflict != DraftConflict.deletedElsewhere &&
      (_draft == null || _draft!.state == 'editing');

  /// Reads the project's unfinished drafts.
  Future<void> load() async {
    try {
      _drafts = List.unmodifiable(await _list(projectRoot));
      _failure = null;
    } catch (error) {
      _failure = '$error';
    }
    notifyListeners();
  }

  /// Adopts [draft] as the one being edited, so a person reopens a draft
  /// rather than starting again.
  void adopt(WorkingDraft draft) {
    _draft = draft;
    _conflict = DraftConflict.none;
    _theirs = null;
    _unsaved = false;
    _writeFailed = false;
    notifyListeners();
  }

  /// Queues a save [autosave] after the last edit (F6 decision 10): the timer
  /// restarts on every edit, so a person typing continuously is saved once.
  void scheduleSave(WorkingDraftContent content) {
    _pending = content;
    _unsaved = true;
    _timer?.cancel();
    if (!editable) return;
    _timer = Timer(autosave, () => unawaited(saveNow()));
  }

  /// Saves the queued content now. Closing the sheet calls this, so a draft is
  /// never lost to a timer that had not fired.
  Future<void> saveNow() async {
    _timer?.cancel();
    final content = _pending;
    if (content == null || _saving) return;
    await _saveContent(content);
  }

  /// Keeps this editor's version, overwriting the one that won (F6 decision
  /// 10): the save names the winning version, so it is a deliberate overrule
  /// rather than a silent one.
  Future<void> keepMine() async {
    final content = _pending;
    final theirs = _theirs;
    if (content == null || theirs == null) return;
    await _saveContent(content, expectedVersion: theirs.version);
  }

  /// Takes the version that won: the form is replaced by it, and the local
  /// edits are dropped.
  Future<void> takeTheirs() async {
    final theirs = _theirs;
    if (theirs == null) return;
    _draft = theirs;
    _pending = theirs.content;
    _conflict = DraftConflict.none;
    _theirs = null;
    _unsaved = false;
    _writeFailed = false;
    notifyListeners();
  }

  /// Keeps the local work as a new draft, after the one it was made from was
  /// deleted elsewhere.
  Future<void> saveAsNew() async {
    final content = _pending ?? _draft?.content;
    if (content == null) return;
    _conflict = DraftConflict.none;
    _theirs = null;
    _draft = null;
    await _saveContent(content, create: true);
  }

  /// Drops the local work: the draft is left as the bridge has it.
  void discard() {
    _timer?.cancel();
    _pending = null;
    _conflict = DraftConflict.none;
    _theirs = null;
    _unsaved = false;
    _writeFailed = false;
    notifyListeners();
  }

  /// Deletes the draft the editor holds, as a versioned write.
  Future<void> delete() async {
    final draft = _draft;
    if (draft == null) return;
    _timer?.cancel();
    final request = WorkingDraftDeleteRequest(
      requestID: _requestID(),
      expectedVersion: draft.version,
    );
    try {
      await _remove(draft.id, request);
      _draft = null;
      _pending = null;
      _conflict = DraftConflict.none;
      _theirs = null;
      _failure = null;
      _unsaved = false;
      _writeFailed = false;
      await load();
    } catch (error) {
      _failure = '$error';
      _writeFailed = true;
      notifyListeners();
    }
  }

  /// Writes [content]: a create when there is no draft yet (or [create] is
  /// set), otherwise a versioned save. A refusal is read as the conflict it
  /// is, never as a generic failure.
  Future<void> _saveContent(
    WorkingDraftContent content, {
    int? expectedVersion,
    bool create = false,
  }) async {
    _saving = true;
    notifyListeners();
    try {
      final draft = _draft;
      if (create || draft == null) {
        _draft = await _create(
          WorkingDraftCreateRequest(
            projectRoot: projectRoot,
            requestID: _requestID(),
            content: content,
          ),
        );
      } else {
        _draft = await _save(
          draft.id,
          WorkingDraftSaveRequest(
            requestID: _requestID(),
            expectedVersion: expectedVersion ?? draft.version,
            content: content,
          ),
        );
      }
      _conflict = DraftConflict.none;
      _theirs = null;
      _failure = null;
      _unsaved = false;
      _writeFailed = false;
    } on BridgeFailure catch (failure) {
      switch (failure.status) {
        case 409:
          _writeFailed = false;
          await _readConflict();
        case 404:
          _conflict = DraftConflict.deletedElsewhere;
          _failure = null;
          _writeFailed = false;
        default:
          _failure = failure.message;
          _writeFailed = true;
      }
    } catch (error) {
      _failure = '$error';
      _writeFailed = true;
    }
    _saving = false;
    notifyListeners();
  }

  /// Reads the version that won a stale save, so both can be shown.
  Future<void> _readConflict() async {
    final draft = _draft;
    if (draft == null) return;
    try {
      _theirs = await _read(draft.id);
      _conflict = _theirs!.state == 'deleted'
          ? DraftConflict.deletedElsewhere
          : DraftConflict.changedElsewhere;
      _failure = null;
    } catch (error) {
      // A read that fails leaves the conflict unnamed rather than claiming
      // the draft is gone: the person can retry.
      _failure = '$error';
      _writeFailed = true;
    }
  }

  String _requestID() =>
      'app-draft-$projectRoot-${DateTime.now().microsecondsSinceEpoch}-'
      '${_requests++}';

  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    _timer?.cancel();
    super.dispose();
  }

  @override
  void notifyListeners() {
    if (_disposed) return;
    super.notifyListeners();
  }
}
