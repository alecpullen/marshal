import '../../model/notifications_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// Settings' Notifications section (F7 decision 7): where Marshal reaches the
/// user — its webhooks — with Send a test, which proves one can be reached.
///
/// Signal and Element are planned rows (decision 9): the bridge posts to
/// webhooks, so a row for a messenger says it is planned rather than pretending
/// to be a target.
class NotificationsSection extends StatelessWidget {
  const NotificationsSection({super.key, required this.model, this.state});

  final NotificationsModel model;

  /// The state the shell knows, when it knows more than the model does.
  final StateKind? state;

  @override
  Widget build(BuildContext context) {
    return SectionPage<NotifyRow>(
      title: 'Notifications',
      subtitle: 'Where Marshal reaches you when something needs you',
      model: model,
      state: state,
      readOnly: true,
      emptyMessage: 'No webhook is configured, so nothing is sent out.',
      actions: [
        Btn.primary(
          'Send a test',
          onTap: model.items.any((row) => row.kind == NotifyRowKind.webhook)
              ? () => model.sendTest()
              : null,
        ),
      ],
      footer: const [
        Rows([
          RowItem.planned(
            name: 'Signal',
            description:
                'A Signal bridge is planned: the bridge posts to webhooks '
                'only, so this row is where it will appear.',
          ),
          RowItem.planned(
            name: 'Element',
            description:
                'An Element (Matrix) room is planned, on the same terms as '
                'Signal.',
          ),
        ]),
      ],
      row: (row, onTap) => RowItem(
        name: row.name,
        description: row.description,
        value: row.value,
        onTap: onTap,
      ),
      detail: (context, row) => Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (row.kind == NotifyRowKind.test)
            Text(
              'Send a test posts one notification to every webhook above, '
              'signed with its own secret when it has one. Nothing is retried '
              'for the test, so a delivery that fails says so at once.',
              style: Ember.of(context).body.tint(Ember.of(context).muted),
            )
          else
            Box(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (final (term, meaning) in row.facts) _Fact(term, meaning),
                ],
              ),
            ),
        ],
      ),
    );
  }
}

/// One named fact of a webhook.
class _Fact extends StatelessWidget {
  const _Fact(this.term, this.meaning);

  final String term;
  final String meaning;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: Space.s12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(term, style: e.small.tint(e.muted)),
          const SizedBox(height: Space.s2),
          Text(meaning, style: e.body),
        ],
      ),
    );
  }
}
