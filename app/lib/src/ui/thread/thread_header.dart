import '../../model/thread_ref.dart' as model;
import '../kit/kit.dart';

/// A thread's header (design-system.md §5, spec decision 10): project, kind,
/// branch, environment and host on one line; the status dot, the title and
/// the live headline on the next; Share and the overflow menu at the right.
///
/// Share is drawn disabled until F7 has a status link to hand out: a control
/// that cannot work is shown as unavailable, never as working (design-system
/// §7).
class ThreadHeader extends StatelessWidget {
  const ThreadHeader({
    super.key,
    required this.thread,
    this.branch,
    this.environment,
    this.onShare,
    this.onStop,
    this.onHold,
  });

  /// The thread this header names.
  final model.ThreadRef thread;

  /// The branch the thread works on, when the fleet reports one.
  final String? branch;

  /// The environment it runs in, when the fleet reports one.
  final String? environment;

  /// Shares a read-only status link. Null until F7: the control is disabled
  /// and its tooltip says so.
  final VoidCallback? onShare;

  /// Stops the thread. Null hides the action.
  final VoidCallback? onStop;

  /// Holds the thread, keeping its work. Null hides the action.
  final VoidCallback? onHold;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final branch = this.branch;
    final environment = this.environment;
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Layout.pageSide,
        Space.s12,
        Space.s12,
        Space.s12,
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  '${thread.project}  ›  $_kindWord  ·  '
                  '${[?branch, ?environment, thread.host].join('  ·  ')}',
                  style: e.monoSmall.tint(e.muted),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
                const SizedBox(height: Space.s6),
                Row(
                  children: [
                    Dot(_status(thread.status)),
                    const SizedBox(width: Space.s8),
                    Flexible(
                      child: Text(
                        thread.title,
                        style: e.title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    const SizedBox(width: Space.s12),
                    Expanded(
                      child: Text(
                        thread.headline,
                        style: e.small.tint(e.muted),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                  ],
                ),
              ],
            ),
          ),
          const SizedBox(width: Space.s8),
          IconBtn(
            Glyphs.share,
            tooltip: onShare == null
                ? 'Share a read-only status link: arrives in F7'
                : 'Share a read-only status link',
            onTap: onShare,
          ),
          if (onStop != null || onHold != null)
            Menu(
              choices: [
                if (onStop != null)
                  MenuChoice('Stop', 'End the turn now', onSelect: onStop!),
                if (onHold != null)
                  MenuChoice(
                    'Hold',
                    'Pause work and keep everything',
                    onSelect: onHold!,
                  ),
              ],
              anchor: (context, toggle) => IconBtn(
                Glyphs.ellipsis,
                tooltip: 'More actions',
                onTap: toggle,
              ),
            ),
        ],
      ),
    );
  }

  /// What the header calls the thread's kind (design-system.md §6): the word
  /// the kind icon's tooltip uses.
  String get _kindWord => switch (thread.kind) {
    model.ThreadKind.agent => 'Agent',
    model.ThreadKind.workItem => 'Work item',
    model.ThreadKind.planRun => 'Plan run',
  };

  /// design-system.md §6: a thread's status is one of the five, and its word
  /// is what the dot says.
  static Status _status(model.ThreadStatus status) => switch (status) {
    model.ThreadStatus.needsYou => Status.needsYou,
    model.ThreadStatus.running => Status.running,
    model.ThreadStatus.ready => Status.ready,
    model.ThreadStatus.failed => Status.failed,
    model.ThreadStatus.idle => Status.idle,
  };
}
