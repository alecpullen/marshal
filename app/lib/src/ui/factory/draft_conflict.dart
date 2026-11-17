import '../../model/drafts_model.dart';
import '../kit/kit.dart';

/// What a draft conflict looks like (F6 decision 10): both versions side by
/// side when the draft changed elsewhere, and the two choices a draft deleted
/// elsewhere leaves.
///
/// It is drawn where the draft was edited, above the fields, so the person
/// decides before they keep typing into something that no longer is what the
/// bridge has.
class DraftConflictBar extends StatelessWidget {
  const DraftConflictBar({super.key, required this.drafts, this.onDecision});

  final DraftsModel drafts;

  /// Called after a choice was taken, so the screen can close a sheet or
  /// reload its form.
  final VoidCallback? onDecision;

  @override
  Widget build(BuildContext context) {
    final conflict = drafts.conflict;
    if (conflict == DraftConflict.none) return const SizedBox.shrink();
    final e = Ember.of(context);
    final theirs = drafts.theirsVersion;
    return Box(
      tone: BoxTone.failing,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            conflict == DraftConflict.deletedElsewhere
                ? 'This draft was deleted elsewhere'
                : 'This draft changed elsewhere',
            style: e.strong,
          ),
          const SizedBox(height: Space.s4),
          Text(
            conflict == DraftConflict.deletedElsewhere
                ? 'Nothing here was saved over the deletion. Keep this work as '
                      'a new draft, or discard it.'
                : 'Someone saved this draft since you opened it. Nothing here '
                      'has been written over either version.',
            style: e.small.tint(e.muted),
          ),
          if (conflict == DraftConflict.changedElsewhere && theirs != null) ...[
            const SizedBox(height: Space.s12),
            _versions(context, theirs),
          ],
          const SizedBox(height: Space.s12),
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            children: conflict == DraftConflict.deletedElsewhere
                ? [
                    Btn.primary(
                      'Save as a new draft',
                      onTap: () async {
                        await drafts.saveAsNew();
                        onDecision?.call();
                      },
                    ),
                    Btn(
                      'Discard this work',
                      onTap: () {
                        drafts.discard();
                        onDecision?.call();
                      },
                    ),
                  ]
                : [
                    Btn.primary(
                      'Keep mine',
                      onTap: () async {
                        await drafts.keepMine();
                        onDecision?.call();
                      },
                    ),
                    Btn(
                      'Take theirs',
                      onTap: () async {
                        await drafts.takeTheirs();
                        onDecision?.call();
                      },
                    ),
                  ],
          ),
          if (drafts.failure != null) ...[
            const SizedBox(height: Space.s6),
            Text(drafts.failure!, style: e.small.tint(e.err)),
          ],
        ],
      ),
    );
  }

  /// Both versions, side by side: what this editor holds, and what the bridge
  /// has now. They are read only; the actions below decide.
  Widget _versions(BuildContext context, DraftVersion theirs) {
    final mine = drafts.mine;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Expanded(child: _version(context, 'Yours', mine)),
        const SizedBox(width: Space.s12),
        Expanded(child: _version(context, 'Theirs', theirs)),
      ],
    );
  }

  Widget _version(BuildContext context, String who, DraftVersion? row) {
    final e = Ember.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(who, style: e.bodyStrong),
        const SizedBox(height: Space.s2),
        Text(
          row == null || row.title.trim().isEmpty
              ? 'Untitled work'
              : row.title.trim(),
          style: e.small,
        ),
        const SizedBox(height: Space.s2),
        Text(
          row?.version == null ? 'Not saved yet' : 'Version ${row!.version}',
          style: e.monoSmall.tint(e.muted),
        ),
      ],
    );
  }
}
