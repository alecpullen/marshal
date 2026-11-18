import '../../model/inbox_model.dart' as model;
import '../../model/merge_target.dart';
import '../../model/work_item_model.dart';
import '../kit/kit.dart';
import 'recovery_form.dart';

/// What the item waits on a person for, as the cards its thread pins at the
/// top (F6 decision 5).
///
/// Each of the four asks states the consequence of every choice it offers: a
/// draft says what approving authorises, a decision takes the person's answer,
/// a recovery carries its settings inline, and a completion says whether the
/// work is done. An action taken shows as pending until the item changes, and
/// a version conflict leaves the action available with the reason beside it —
/// the person decides again on what is there now.
///
/// What the person is writing and what is in flight live in a
/// [WorkItemAskDraft], which the item's screen owns: the card is rebuilt by
/// every poll and unmounted by a worker drill-in, and neither may lose an
/// answer half typed.
class ItemAsks extends StatefulWidget {
  const ItemAsks({
    super.key,
    required this.item,
    required this.now,
    this.draft,
    this.conflict,
    this.onApprove,
    this.onAnswer,
    this.onRecover,
    this.onComplete,
    this.onMergeAndComplete,
    this.onReopen,
    this.onProposeChange,
  });

  final WorkItemView item;

  /// What a relative age is measured against.
  final DateTime now;

  /// The person's unsaved work, when the item's screen keeps it. Null leaves
  /// the cards owning their own, which lasts as long as they are drawn.
  final WorkItemAskDraft? draft;

  /// The bridge's refusal of the last action, in its own words: a conflict is
  /// not an error the person made.
  final String? conflict;

  /// Approves a draft and starts it.
  final Future<void> Function()? onApprove;

  /// Answers the decision with the person's words.
  final Future<void> Function(String answer)? onAnswer;

  /// Recovers the item with the choice the form collected.
  final Future<void> Function(RecoveryChoice choice)? onRecover;

  /// Says the work counts as done.
  final Future<void> Function()? onComplete;

  /// Merges the reviewed pull request and completes the item.
  final Future<void> Function()? onMergeAndComplete;

  /// Reopens a completed item.
  final Future<void> Function()? onReopen;

  /// Starts a plan revision (F6 plan 1, task 5).
  final VoidCallback? onProposeChange;

  @override
  State<ItemAsks> createState() => _ItemAsksState();
}

class _ItemAsksState extends State<ItemAsks> {
  /// The person's unsaved work: the screen's when it keeps one, so a poll or a
  /// worker drill-in does not lose it.
  late WorkItemAskDraft _draft;
  bool _ownsDraft = false;

  @override
  void initState() {
    super.initState();
    _adopt(widget.draft);
  }

  void _adopt(WorkItemAskDraft? draft) {
    _draft = draft ?? WorkItemAskDraft();
    _ownsDraft = draft == null;
    _draft.addListener(_changed);
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  void didUpdateWidget(ItemAsks old) {
    super.didUpdateWidget(old);
    if (!identical(old.draft, widget.draft)) {
      _draft.removeListener(_changed);
      if (_ownsDraft) _draft.dispose();
      _adopt(widget.draft);
    }
    // A new version has answered whatever was in flight.
    _draft.settle(widget.item.version);
  }

  @override
  void dispose() {
    _draft.removeListener(_changed);
    if (_ownsDraft) _draft.dispose();
    super.dispose();
  }

  Future<void> _run(String id, Future<void> Function() action) async {
    if (_draft.isPending(id)) return;
    _draft.begin(id);
    try {
      await action();
    } catch (_) {
      // The store keeps the bridge's words for a conflict; anything else is
      // the host's to say. Either way the action stays available.
    } finally {
      _draft.end(id);
    }
  }

  @override
  Widget build(BuildContext context) {
    final asks = widget.item.asks;
    if (asks.isEmpty) return const SizedBox.shrink();
    final e = Ember.of(context);
    final conflict = widget.conflict;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (conflict != null && conflict.isNotEmpty) ...[
          Text(conflict, style: e.small.tint(e.err)),
          const SizedBox(height: Space.s8),
        ],
        for (final ask in asks) ...[
          _card(context, ask),
          const SizedBox(height: Space.s8),
        ],
      ],
    );
  }

  Widget _card(BuildContext context, model.Ask ask) {
    final pending = _draft.isPending(ask.id);
    final enabled = !pending;
    final waiting = relativeAgeWords(widget.now, ask.since);
    final body = ask.body.trim();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        AskCard(
          kind: _kind(ask.kind),
          placement: AskPlacement.thread,
          title: ask.title,
          body: body.isEmpty ? null : body,
          waiting: waiting,
          actions: [
            for (final action in ask.actions) _action(ask, action, enabled),
          ],
        ),
        if (ask.kind == model.AskKind.decision) ...[
          const SizedBox(height: Space.s8),
          // At most one decision ask is built from asksOf (it is gated on the
          // item's single pending decision), so one answer controller is
          // enough; a second decision would need its own.
          Field(
            label: 'Your answer',
            controller: _draft.answer,
            hint: 'In your own words',
            // The answer action is drawn disabled until there are words, so
            // the card is rebuilt as the person types.
            onChanged: (_) => setState(() {}),
          ),
        ],
        if (ask.kind == model.AskKind.recovery) ...[
          const SizedBox(height: Space.s12),
          RecoveryForm(
            enabled: enabled,
            onSubmit: (choice) => _run(ask.id, () async {
              await widget.onRecover?.call(choice);
            }),
          ),
        ],
        if (pending) ...[
          const SizedBox(height: Space.s4),
          Text(
            'Sending…',
            style: Ember.of(context).small.tint(Ember.of(context).muted),
          ),
        ],
      ],
    );
  }

  /// One action of [ask]: offered while nothing is in flight, and drawn
  /// disabled with its reason when the item cannot take it.
  ///
  /// A completion's merge needs the reviewed identity the item holds, and a
  /// decision's answer needs words. With neither, the action is drawn disabled
  /// and says why rather than offering a control that would do nothing or a
  /// command the bridge would refuse (web/bridge/factory_completion_batch.go).
  AskAction _action(model.Ask ask, model.AskAction action, bool enabled) {
    final blocked =
        ask.kind == model.AskKind.completion &&
        action.label == _mergeLabel &&
        widget.item.merge == null;
    final unanswered =
        ask.kind == model.AskKind.decision &&
        action.kind == model.AskActionKind.reply &&
        _draft.answer.text.trim().isEmpty;
    return AskAction(
      action.label,
      onTap: enabled && !blocked && !unanswered
          ? () => _act(ask, action)
          : null,
      note: blocked
          ? noMergeTargetNote
          : unanswered
          ? _emptyAnswerNote
          : null,
    );
  }

  /// The label the completion ask gives its merge.
  static const _mergeLabel = 'Merge and complete';

  /// Why a decision's answer is drawn disabled: an answer with nothing in it is
  /// not worth a round trip.
  static const _emptyAnswerNote = 'Type your answer first';

  /// What one action does. A recovery's own settings are sent by its form.
  void _act(model.Ask ask, model.AskAction action) {
    switch (ask.kind) {
      case model.AskKind.approvalDraft:
        if (action.label.toLowerCase().startsWith('approve')) {
          _run(ask.id, () async {
            await widget.onApprove?.call();
          });
        } else {
          widget.onProposeChange?.call();
        }
      case model.AskKind.decision:
        final answer = _draft.answer.text.trim();
        if (answer.isEmpty) return;
        _run(ask.id, () async {
          await widget.onAnswer?.call(answer);
        });
      case model.AskKind.completion:
        switch (action.label) {
          case 'Complete':
            _run(ask.id, () async {
              await widget.onComplete?.call();
            });
          case 'Merge and complete':
            _run(ask.id, () async {
              await widget.onMergeAndComplete?.call();
            });
          default:
            _run(ask.id, () async {
              await widget.onReopen?.call();
            });
        }
      case model.AskKind.recovery:
      case model.AskKind.approval:
      case model.AskKind.question:
      case model.AskKind.intake:
      case model.AskKind.pull:
      case model.AskKind.reviewDraft:
      case model.AskKind.network:
        return;
    }
  }

  /// A work item's four asks are drawn with the kit's existing kinds (F6):
  /// the card's title and body say what is being decided.
  static AskKind _kind(model.AskKind kind) => switch (kind) {
    model.AskKind.approvalDraft => AskKind.approval,
    model.AskKind.decision => AskKind.decision,
    model.AskKind.recovery => AskKind.decision,
    model.AskKind.completion => AskKind.decision,
    model.AskKind.approval => AskKind.approval,
    model.AskKind.question => AskKind.question,
    model.AskKind.intake => AskKind.intake,
    model.AskKind.pull => AskKind.pull,
    model.AskKind.reviewDraft => AskKind.review,
    model.AskKind.network => AskKind.network,
  };
}

/// How long ago [since] was, against [now], in the design system's short form.
String relativeAgeWords(DateTime now, DateTime since) {
  final delta = now.difference(since);
  if (delta.inMinutes < 1) return 'now';
  if (delta.inMinutes < 60) return '${delta.inMinutes}m';
  if (delta.inHours < 24) return '${delta.inHours}h';
  return '${delta.inDays}d';
}

/// The person's unsaved work on an item's asks: what they are typing and
/// which commands are in flight.
///
/// It belongs to the item's screen rather than to the card that draws an
/// ask: a worker drill-in rebuilds the body, and the answer being typed must
/// still be there on the way back.
class WorkItemAskDraft extends ChangeNotifier {
  /// The words being written for a decision or a recovery.
  final TextEditingController answer = TextEditingController();

  final Set<String> _pending = {};
  int _version = -1;
  bool _disposed = false;

  /// Whether [askID]'s command is in flight.
  bool isPending(String askID) => _pending.contains(askID);

  /// Acknowledges a command at once (design-system.md §7).
  void begin(String askID) {
    if (_pending.add(askID) && !_disposed) notifyListeners();
  }

  /// The command has finished: it is no longer pending.
  ///
  /// A command can outlive the screen that started it — the person leaves the
  /// item while an answer is sending — so this never notifies a draft that has
  /// already been disposed.
  void end(String askID) {
    if (_pending.remove(askID) && !_disposed) notifyListeners();
  }

  /// Forgets what was in flight when the item itself moved: a new version has
  /// answered it. It does not notify, because it runs while the item's widget
  /// is already rebuilding.
  void settle(int version) {
    if (_version == version) return;
    _version = version;
    _pending.clear();
  }

  @override
  void dispose() {
    _disposed = true;
    answer.dispose();
    super.dispose();
  }
}
