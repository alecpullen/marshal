import '../../model/fleet_store.dart' show describeFailure;
import '../../model/pulls_model.dart';
import '../kit/kit.dart';

/// Confirms a merge and posts it (F5 decision 13).
///
/// A merge cannot be undone, so it asks first and names both what lands and
/// where: the pull request's number and title, and the branch it merges into.
/// The route behind it is the model's — an agent's own merge, or a factory
/// item's `merge_and_complete` command.
Future<void> confirmMerge(
  BuildContext context, {
  required PullView pull,
  required PullsModel model,
}) async {
  final e = Ember.of(context);
  final confirmed = await Sheet.show<bool>(
    context,
    Sheet(
      title: 'Merge #${pull.number} into ${pull.base}?',
      actions: [
        Btn.primary('Merge', onTap: () => Navigator.maybePop(context, true)),
        Btn('Keep it open', onTap: () => Navigator.maybePop(context, false)),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text('“${pull.title}”', style: e.strong),
          const SizedBox(height: Space.s4),
          Text(
            '${pull.repo} · ${pull.destination}',
            style: e.monoSmall.tint(e.muted),
          ),
          const SizedBox(height: Space.s12),
          Text(
            '${pull.branch} is merged into ${pull.base} on the forge. The pull '
            'request is not deleted, and nothing in this thread changes.',
            style: e.body,
          ),
        ],
      ),
    ),
  );
  if (confirmed != true) return;
  try {
    await model.merge(pull);
  } catch (error) {
    if (!context.mounted) return;
    await Sheet.show<void>(
      context,
      Sheet(
        title: 'The merge did not go through',
        child: Text(describeFailure(error), style: e.body.tint(e.err)),
      ),
    );
  }
}
