import '../../model/inbox_model.dart' as model;
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
class ItemAsks extends StatefulWidget {
  const ItemAsks({
    super.key,
    required this.item,
    required this.now,
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
  final _reply = TextEditingController();

  /// The asks whose command is in flight, by ask id: an action taken is
  /// acknowledged at once and shows as pending until the item changes (§7).
  final _pending = <String>{};

  @override
  void didUpdateWidget(ItemAsks old) {
    super.didUpdateWidget(old);
    if (old.item.version != widget.item.version) _pending.clear();
  }

  @override
  void dispose() {
    _reply.dispose();
    super.dispose();
  }

  Future<void> _run(String id, Future<void> Function() action) async {
    if (_pending.contains(id)) return;
    setState(() => _pending.add(id));
    try {
      await action();
    } catch (_) {
      // The store keeps the bridge's words for a conflict; anything else is
      // the host's to say. Either way the action stays available.
    } finally {
      if (mounted) setState(() => _pending.remove(id));
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
    final pending = _pending.contains(ask.id);
    final enabled = !pending;
    final waiting = relativeAgeWords(widget.now, ask.since);
    final body = ask.body.trim();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (ask.kind == model.AskKind.decision) ...[
          Field(
            label: 'Your answer',
            controller: _reply,
            hint: 'In your own words',
          ),
          const SizedBox(height: Space.s8),
        ],
        AskCard(
          kind: _kind(ask.kind),
          placement: AskPlacement.thread,
          title: ask.title,
          body: body.isEmpty ? null : body,
          waiting: waiting,
          actions: [
            for (final action in ask.actions)
              AskAction(
                action.label,
                onTap: enabled ? () => _act(ask, action) : null,
              ),
          ],
        ),
        if (ask.kind == model.AskKind.recovery) ...[
          const SizedBox(height: Space.s12),
          RecoveryForm(
            diagnosis: '',
            attempts: '',
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
        final answer = _reply.text.trim();
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
