import '../../model/bots_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';
import 'automations_place.dart' show AutomationsTabs;
import 'review_draft_sheet.dart';

/// The Bots tab of Automations (F7 decision 4): a project's review bot and CI
/// fixer, each with its settings and what it has done lately.
///
/// The review bot's held reviews open in place, with Post, Edit, Send to the
/// author's agent and Discard. The CI fixer's rule — it never deletes, skips
/// or disables a test — is stated as text, not as a setting.
class BotsTab extends StatelessWidget {
  const BotsTab({super.key, required this.model, this.tabs, this.state});

  final BotsModel model;

  /// The tab bar the section page draws; null when the tab is shown alone.
  final AutomationsTabs? tabs;

  /// The state the shell knows.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<Bot>(
      title: 'Bots',
      subtitle: 'What reviews and fixes a pull request without you',
      model: model,
      tabs: tabs?.items,
      tabIndex: tabs?.index ?? 0,
      onTab: tabs?.onChanged,
      state: state,
      readOnly: true,
      emptyMessage:
          'A project with a review bot or a CI fixer lists it here, with its '
          'recent work.',
      row: (bot, onTap) => RowItem(
        name: bot.name,
        description: bot.repoID.isEmpty ? 'No repository named' : bot.repoID,
        value: bot.enabled ? bot.summary : 'Off',
        onTap: onTap,
      ),
      detail: (context, bot) => _BotDetail(model: model, bot: bot),
    );
  }
}

/// One bot's detail: its settings, what it has done, and — for the CI fixer —
/// the rule it keeps and the automatic-fixing switch that a later step brings.
class _BotDetail extends StatefulWidget {
  const _BotDetail({required this.model, required this.bot});

  final BotsModel model;
  final Bot bot;

  @override
  State<_BotDetail> createState() => _BotDetailState();
}

class _BotDetailState extends State<_BotDetail> {
  /// The held review open in place; null shows the bot itself.
  ReviewDraft? _open;

  @override
  Widget build(BuildContext context) {
    final draft = _open;
    if (draft != null) {
      return ReviewDraftView(
        model: widget.model,
        draft: draft,
        onClose: () => setState(() => _open = null),
      );
    }
    final e = Ember.of(context);
    final bot = widget.bot;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final (label, value) in bot.facts)
          _Fact(label: label, value: value),
        if (bot.kind == BotKind.fixer) ...[
          const SizedBox(height: Space.s16),
          Text(
            'It never deletes, skips or disables a test. A failing check is '
            'reproduced, the cause fixed, and the suite run again.',
            style: e.body,
          ),
          const SizedBox(height: Space.s16),
          const StateMessage.planned(
            name: 'Automatic fixing',
            note:
                'The switch arrives with the automatic-fixing step; the '
                'bridge has no setting for it yet.',
          ),
        ],
        const SizedBox(height: Space.s20),
        if (bot.kind == BotKind.review) ..._drafts(context, bot.drafts),
        if (bot.kind == BotKind.fixer) ..._history(context, bot.history),
      ],
    );
  }

  /// The review bot's held reviews: one row each.
  List<Widget> _drafts(BuildContext context, List<ReviewDraft> drafts) {
    final e = Ember.of(context);
    if (drafts.isEmpty) {
      return [
        Text('No review is waiting on you.', style: e.small.tint(e.muted)),
      ];
    }
    return [
      Label('Waiting on you', count: drafts.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final draft in drafts)
          RowItem(
            name: draft.title ?? 'Pull request #${draft.number}',
            description:
                '${draft.repoID} #${draft.number} · '
                '${draft.findings.length} '
                '${draft.findings.length == 1 ? 'finding' : 'findings'}',
            value: '${widget.model.ageLabel(draft.createdAt)} ago',
            onTap: () => setState(() => _open = draft),
          ),
      ]),
    ];
  }

  /// The CI fixer's recent runs, newest first.
  List<Widget> _history(BuildContext context, List<CIHistory> history) {
    final e = Ember.of(context);
    if (history.isEmpty) {
      return [
        Text('It has not fixed a check yet.', style: e.small.tint(e.muted)),
      ];
    }
    return [
      Label('Recent runs', count: history.length),
      const SizedBox(height: Space.s8),
      Rows([
        for (final run in history)
          RowItem(
            name: run.check,
            description:
                '${run.branch ?? run.sha} · ${run.status}'
                '${run.reason == null || run.reason!.isEmpty ? '' : ' · ${run.reason}'}',
            value: '${widget.model.ageLabel(run.createdAt)} ago',
          ),
      ]),
    ];
  }
}

/// One labelled fact of a bot's settings: a label and its value.
class _Fact extends StatelessWidget {
  const _Fact({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(value, style: e.body),
        ],
      ),
    );
  }
}
