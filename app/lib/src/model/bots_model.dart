import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A bot's own types reach their screens through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart'
    show CIHistory, CIFixerSettings, Finding, ReviewBotSettings, ReviewDraft;

/// Which bot a row is about (F7 decision 4).
enum BotKind { review, fixer }

/// One bot, as its row and detail show it: its settings, and what it has done
/// lately — held reviews for the review bot, recent runs for the CI fixer.
@immutable
class Bot {
  const Bot({
    required this.kind,
    required this.enabled,
    required this.repoID,
    required this.summary,
    required this.facts,
    this.drafts = const [],
    this.history = const [],
  });

  final BotKind kind;
  final bool enabled;

  /// The forge repository it acts on.
  final String repoID;

  /// One line, in the row: what it is doing.
  final String summary;

  /// Its settings, label and value, as the detail shows them.
  final List<(String, String)> facts;

  /// Held reviews, newest first. The review bot's only.
  final List<ReviewDraft> drafts;

  /// Recent runs, newest first. The CI fixer's only.
  final List<CIHistory> history;

  String get name => kind == BotKind.review ? 'The review bot' : 'The CI fixer';

  Bot withDrafts(List<ReviewDraft> next) => Bot(
    kind: kind,
    enabled: enabled,
    repoID: repoID,
    summary: summary,
    facts: facts,
    drafts: next,
    history: history,
  );

  @override
  String toString() => 'Bot(${kind.name}, $repoID)';
}

/// The Bots tab of Automations (F7 decision 4): the review bot and the CI
/// fixer, per project, with their settings and recent work.
///
/// A bot's settings are the project settings document's, so this section
/// reads them and never writes half of that document; the review drafts it
/// does change are its own routes.
class BotsModel extends SectionModel<Bot> {
  BotsModel(
    this._bridge, {
    required this.project,
    required this.now,
    this.onWrote,
  }) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  BotsModel.fixture(
    List<Bot> rows, {
    required this.project,
    required this.now,
    this.onWrote,
  }) : _bridge = null,
       _fixture = rows;

  final BridgeClient? _bridge;
  final List<Bot>? _fixture;

  /// The project whose automations these are.
  final String project;

  /// What an age is measured against.
  final DateTime now;

  /// How a write tells the host's signals to be read again (spec D4): called
  /// once after a draft is posted, sent to its author or discarded, and never
  /// for a write the bridge refused.
  final void Function()? onWrote;

  @override
  Future<List<Bot>> fetch() async {
    final fixed = _fixture;
    if (fixed != null) return fixed;
    final bridge = _requireBridge();
    final settings = await bridge.projectAutomations(project);
    final drafts = await bridge.reviewDrafts(project: project, status: 'draft');
    final history = await bridge.ciHistory(project: project);
    final automations = settings.automations;
    return [
      _reviewBot(automations?.reviewBot, drafts),
      _ciFixer(automations?.ciFixer, history),
    ];
  }

  @override
  Object keyOf(Bot bot) => bot.kind.name;

  @override
  bool isNew(Bot bot) => false;

  @override
  Future<Bot> persist(Bot bot) => throw StateError(
    'a bot’s settings belong to the project settings document, not this '
    'section',
  );

  /// POST /api/automations/review/drafts/{id}/post: posts the review, and the
  /// row becomes what the bridge answered.
  Future<ReviewDraft> postDraft(ReviewDraft draft) async {
    final posted = await _requireBridge().postReviewDraft(draft.id);
    _replaceDraft(posted);
    onWrote?.call();
    return posted;
  }

  /// POST /api/automations/review/drafts/{id}/send-to-author: hands every
  /// finding to the agent that owns the pull request.
  Future<void> sendDraftToAuthor(ReviewDraft draft) async {
    await _requireBridge().sendDraftToAuthor(
      draft.id,
      findingIds: [for (final finding in draft.findings) finding.id],
    );
    onWrote?.call();
  }

  /// POST /api/automations/review/drafts/{id}/discard: drops the draft, and
  /// its row with it.
  Future<void> discardDraft(ReviewDraft draft) async {
    await _requireBridge().discardReviewDraft(draft.id);
    _dropDraft(draft);
    onWrote?.call();
  }

  /// PUT /api/automations/review/drafts/{id}: stores an edit of the findings
  /// and the summary.
  Future<ReviewDraft> editDraft(
    ReviewDraft draft, {
    required List<Finding> findings,
    required String summary,
  }) async {
    final edited = await _requireBridge().editReviewDraft(
      draft.id,
      findings: findings,
      summary: summary,
    );
    _replaceDraft(edited);
    return edited;
  }

  /// POST /api/automations/review/run: reviews a pull request now.
  Future<void> runReviewBot(String repoID, int number) =>
      _requireBridge().runReviewBot(repoID: repoID, number: number);

  /// How long ago a wire time was, short (§8): "4m", "1h 12m", "2d".
  String ageLabel(String? at) {
    final then = DateTime.tryParse(at ?? '')?.toUtc();
    if (then == null) return 'just now';
    final span = now.difference(then);
    if (span.inMinutes < 1) return 'now';
    if (span.inMinutes < 60) return '${span.inMinutes}m';
    if (span.inHours < 24) {
      final rest = span.inMinutes - span.inHours * 60;
      return rest == 0 ? '${span.inHours}h' : '${span.inHours}h ${rest}m';
    }
    return '${span.inDays}d';
  }

  /// The held reviews of the project, in the bridge's order.
  List<ReviewDraft> get drafts =>
      items.expand((bot) => bot.drafts).toList(growable: false);

  /// The review bot's row: its filter, its hold rule, and the reviews waiting
  /// on a person.
  Bot _reviewBot(ReviewBotSettings? settings, List<ReviewDraft> drafts) {
    final enabled = settings?.enabled ?? false;
    return Bot(
      kind: BotKind.review,
      enabled: enabled,
      repoID: settings?.repoID ?? '',
      summary: _reviewSummary(enabled, drafts.length),
      facts: [
        ('State', enabled ? 'On' : 'Off'),
        ('Repository', _orNone(settings?.repoID)),
        (
          'Reviews',
          _filter(
            settings?.labels ?? const [],
            settings?.authors ?? const [],
            settings?.skipDrafts ?? false,
          ),
        ),
        (
          'Posts on its own',
          (settings?.autoPost ?? false)
              ? 'Yes, unless a finding is ${_list(settings?.holdSeverities)}'
              : 'No: every review waits for you',
        ),
      ],
      drafts: drafts,
    );
  }

  /// The CI fixer's row: the branches it watches and where a fix goes.
  Bot _ciFixer(CIFixerSettings? settings, List<CIHistory> history) {
    final enabled = settings?.enabled ?? false;
    return Bot(
      kind: BotKind.fixer,
      enabled: enabled,
      repoID: settings?.repoID ?? '',
      summary: history.isEmpty
          ? (enabled ? 'Watching for a failed check' : 'Off')
          : '${history.length} ${history.length == 1 ? 'run' : 'runs'} lately',
      facts: [
        ('State', enabled ? 'On' : 'Off'),
        ('Repository', _orNone(settings?.repoID)),
        ('Watches', _orNone(_list(settings?.branches))),
        (
          'Sends a fix',
          (settings?.push ?? false)
              ? 'Straight to ${_orNone(_list(settings?.pushBranches))}'
              : 'As a pull request',
        ),
      ],
      history: history,
    );
  }

  static String _reviewSummary(bool enabled, int waiting) {
    if (waiting == 0) {
      return enabled
          ? 'Watching for pull requests'
          : 'Off: no pull request is reviewed';
    }
    return waiting == 1
        ? '1 review waits for you'
        : '$waiting reviews wait for you';
  }

  static String _orNone(String? value) =>
      value == null || value.isEmpty ? 'None' : value;

  static String? _list(List<String>? values) =>
      values == null || values.isEmpty ? null : values.join(', ');

  static String _filter(List<String> labels, List<String> authors, bool skip) {
    final parts = <String>[
      if (labels.isNotEmpty) 'labelled ${labels.join(', ')}',
      if (authors.isNotEmpty) 'by ${authors.join(', ')}',
      if (!skip) 'drafts too',
    ];
    return parts.isEmpty ? 'Every pull request' : parts.join(' · ');
  }

  /// One review bot whose drafts changed: the row is the list again, and its
  /// summary counts what is left.
  void _withDrafts(List<ReviewDraft> next) {
    replaceItems([
      for (final bot in items)
        if (bot.kind == BotKind.review)
          Bot(
            kind: bot.kind,
            enabled: bot.enabled,
            repoID: bot.repoID,
            summary: _reviewSummary(bot.enabled, next.length),
            facts: bot.facts,
            drafts: next,
            history: bot.history,
          )
        else
          bot,
    ]);
  }

  void _replaceDraft(ReviewDraft draft) => _withDrafts([
    for (final bot in items)
      if (bot.kind == BotKind.review)
        for (final held in bot.drafts)
          if (held.id == draft.id) draft else held,
  ]);

  void _dropDraft(ReviewDraft draft) => _withDrafts([
    for (final bot in items)
      if (bot.kind == BotKind.review)
        for (final held in bot.drafts)
          if (held.id != draft.id) held,
  ]);

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
