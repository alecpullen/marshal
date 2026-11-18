import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/share_model.dart';
import '../kit/kit.dart';

/// The thread header's Share (F7 decision 7): mints a read-only status link,
/// shows the one URL the bridge gives out, copies it, and revokes it.
///
/// It is a sheet because the URL is shown once: closing it and sharing again
/// mints a second link, which is what the revoke beside it makes plain.
Future<void> showShareSheet(BuildContext context, ShareModel model) {
  // Share means "give me a link now": the sheet opens on the link it is
  // making, and says so while the bridge answers.
  unawaited(model.mint());
  return Sheet.show<void>(
    context,
    Sheet(
      title: 'Share this thread',
      child: _ShareBody(model: model),
    ),
  );
}

class _ShareBody extends StatelessWidget {
  const _ShareBody({required this.model});

  final ShareModel model;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    // One sheet for the link's whole life: minted, shown, copied and revoked
    // without closing and reopening it.
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final made = model.made;
        final failure = model.failure;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (model.busy)
              const StateMessage.loading(lines: 2)
            else if (failure != null)
              StateMessage.error(
                message: failure,
                retry: StateAction('Try again', onTap: model.mint),
              )
            else if (made != null) ...[
              // The URL is shown once, in mono, as the Access section shows
              // the one a link is made with: there is no second copy to read.
              Box(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      'This link’s URL, shown once',
                      style: e.small.tint(e.muted),
                    ),
                    const SizedBox(height: Space.s4),
                    Text(made.url, style: e.mono),
                  ],
                ),
              ),
              const SizedBox(height: Space.s12),
              Align(
                alignment: Alignment.centerLeft,
                child: Btn(
                  'Copy link',
                  icon: Glyphs.doc_on_doc,
                  onTap: () => unawaited(
                    Clipboard.setData(ClipboardData(text: made.url)),
                  ),
                ),
              ),
              const SizedBox(height: Space.s8),
              Text(
                'Expires ${_day(made.expiresAt)}',
                style: e.small.tint(e.muted),
              ),
              const SizedBox(height: Space.s12),
              Align(
                alignment: Alignment.centerLeft,
                child: Btn('Revoke', onTap: () => _revoke(context)),
              ),
            ],
            const SizedBox(height: Space.s16),
            Text(
              'The link shows this thread’s status only, never its '
              'transcript.',
              style: e.small.tint(e.muted),
            ),
          ],
        );
      },
    );
  }

  /// Revokes the link, and closes the sheet once it is gone: the URL the
  /// sheet was showing no longer works.
  Future<void> _revoke(BuildContext context) async {
    await model.revoke();
    if (model.made != null || !context.mounted) return;
    Navigator.maybePop(context);
  }
}

/// The day a link stops working, from the bridge's own answer: a date, since
/// the hour it expires is not what a person plans around.
String _day(String expiresAt) {
  final at = DateTime.tryParse(expiresAt);
  if (at == null) return expiresAt;
  String two(int value) => value.toString().padLeft(2, '0');
  return '${at.year}-${two(at.month)}-${two(at.day)}';
}
