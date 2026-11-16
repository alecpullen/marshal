import '../../model/pulls_model.dart';
import '../kit/kit.dart';
import '../pulls/pull_facts.dart';

/// The head of a ready thread (F5 decision 13): CI first, then reviews, then
/// the engineer's report, then the way into the code.
///
/// It is the one place a ready pull request is read rather than listed: every
/// check by name, the reviews, and the four things a person does with it —
/// review the changes, merge it, open it on the forge, or send it back to the
/// agent that shipped it.
class ReviewHead extends StatelessWidget {
  const ReviewHead({
    super.key,
    required this.pull,
    required this.now,
    this.onReviewChanges,
    this.onMerge,
    this.onOpenOnForge,
    this.onSendBack,
  });

  final PullView pull;

  /// What a relative age is measured against.
  final DateTime now;

  /// Opens the inspector on Changes (decision 13).
  final VoidCallback? onReviewChanges;

  final VoidCallback? onMerge;
  final VoidCallback? onOpenOnForge;

  /// Steers the thread's own agent with what the person says. Null when the
  /// agent has ended, and the button then says so rather than doing nothing.
  final VoidCallback? onSendBack;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final facts = pullFactsOf(pull, now: now);
    final report = pull.report;
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Label('Ready for review'),
          const SizedBox(height: Space.s8),
          Box.flush(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Padding(
                  padding: const EdgeInsets.fromLTRB(
                    Space.s16,
                    Space.s12,
                    Space.s16,
                    Space.s8,
                  ),
                  child: _title(e),
                ),
                const Hairline(),
                Padding(
                  padding: const EdgeInsets.all(Space.s16),
                  child: _checks(context, e, facts),
                ),
                if (report != null) ...[
                  const Hairline(),
                  Padding(
                    padding: const EdgeInsets.all(Space.s16),
                    child: _report(e, report),
                  ),
                ],
                const Hairline(),
                Padding(
                  padding: const EdgeInsets.all(Space.s16),
                  child: _actions(context),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _title(Ember e) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Row(
        children: [
          Text('#${pull.number}', style: e.mono.tint(e.muted)),
          const SizedBox(width: Space.s8),
          Expanded(
            child: Text(
              pull.title,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: e.title,
            ),
          ),
          const SizedBox(width: Space.s12),
          Text('+${pull.adds}', style: e.monoSmall.tint(e.ok)),
          const SizedBox(width: Space.s6),
          Text('−${pull.dels}', style: e.monoSmall.tint(e.err)),
        ],
      ),
      const SizedBox(height: Space.s4),
      Text(
        '${pull.repo} · ${pull.destination} · head ${_shortHead()} · checks '
        'ran on this head',
        maxLines: 2,
        overflow: TextOverflow.ellipsis,
        style: e.monoSmall.tint(e.muted),
      ),
    ],
  );

  /// The head commit, short enough for the line it sits on.
  String _shortHead() {
    final sha = pull.headSha;
    return sha.length <= 7 ? sha : sha.substring(0, 7);
  }

  /// The checks in the strip, then each one by name: a failing check is never
  /// only a red block.
  Widget _checks(BuildContext context, Ember e, PullRequestFacts facts) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: Space.s8,
          runSpacing: Space.s4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text('Checks', style: e.small.tint(e.muted)),
            if (facts.checks.isNotEmpty) CheckStrip(facts.checks),
            Text(facts.checkSummary, style: e.body),
          ],
        ),
        const SizedBox(height: Space.s8),
        Wrap(
          spacing: Space.s16,
          runSpacing: Space.s4,
          children: [
            for (final check in facts.checks)
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Tip(
                    message: check.state.word,
                    child: Icon(
                      check.state.icon,
                      size: Sizes.iconInline,
                      color: check.state.color(e),
                    ),
                  ),
                  const SizedBox(width: Space.s6),
                  Text(check.name, style: e.small),
                ],
              ),
          ],
        ),
        const SizedBox(height: Space.s8),
        Row(
          children: [
            Text('Reviews', style: e.small.tint(e.muted)),
            const SizedBox(width: Space.s8),
            Flexible(child: Text(pull.reviews, style: e.small)),
          ],
        ),
      ],
    );
  }

  /// The engineer's account of the change, in their own words.
  Widget _report(Ember e, String report) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      Text('Engineer’s report', style: e.small.tint(e.muted)),
      const SizedBox(height: Space.s6),
      Text(report, style: e.reading),
    ],
  );

  /// The four things a person does with a ready pull request. At most one is
  /// primary, and it is first.
  Widget _actions(BuildContext context) {
    final sendBack = onSendBack;
    return Wrap(
      spacing: Space.s8,
      runSpacing: Space.s8,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        Btn.primary('Review changes', onTap: onReviewChanges),
        Btn('Merge', onTap: onMerge),
        Btn('Open on forge', icon: Glyphs.arrow_up_right, onTap: onOpenOnForge),
        if (sendBack == null)
          Tip(
            message:
                'The agent that shipped this has ended, so there is nothing '
                'to send it back to',
            child: const Btn('Send back'),
          )
        else
          Btn('Send back', onTap: sendBack),
      ],
    );
  }
}
