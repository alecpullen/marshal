import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/share_model.dart';
import '../kit/kit.dart';

/// The thread header's Share (F7 decision 7): mints a read-only status link at
/// the hours Settings offers, shows the one URL the bridge gives out, copies
/// it, and revokes it.
///
/// It is a sheet because the URL is shown once: closing it and sharing again
/// mints a second link, which is what the revoke beside it makes plain.
///
/// Nothing is minted when the sheet opens: the hours a link lasts are a
/// person's choice, made with the button that makes it (A2).
Future<void> showShareSheet(BuildContext context, ShareModel model) {
  return Sheet.show<void>(
    context,
    Sheet(
      title: 'Share this thread',
      child: _ShareBody(model: model),
    ),
  );
}

class _ShareBody extends StatefulWidget {
  const _ShareBody({required this.model});

  final ShareModel model;

  @override
  State<_ShareBody> createState() => _ShareBodyState();
}

class _ShareBodyState extends State<_ShareBody> {
  /// How long the link lasts, in the range Settings offers: 1–720 hours, a
  /// day unless a person says otherwise.
  final _hours = TextEditingController(text: '24');

  @override
  void dispose() {
    _hours.dispose();
    super.dispose();
  }

  /// What is wrong with the hours, in the Settings screen's own words. Null
  /// when the value is one a status link can last.
  String? get _hoursError {
    final hours = int.tryParse(_hours.text.trim());
    if (hours == null) return 'Hours is a whole number';
    if (hours < 1 || hours > 720) {
      return 'A status link lasts between 1 and 720 hours';
    }
    return null;
  }

  /// The hours a link would last, or null while the field names none it can.
  int? get _chosenHours {
    if (_hoursError != null) return null;
    return int.tryParse(_hours.text.trim());
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    // Made, shown, copied and revoked without closing and reopening the
    // sheet, so the one URL the bridge gives out is not asked for twice.
    return AnimatedBuilder(
      animation: model,
      builder: (context, _) {
        final made = model.made;
        final failure = model.failure;
        final hours = _chosenHours;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (made == null) ...[
              Field(
                label: 'Hours it lasts',
                hint: '24',
                controller: _hours,
                error: _hoursError,
                onChanged: (_) => setState(() {}),
              ),
              const SizedBox(height: Space.s16),
              Align(
                alignment: Alignment.centerLeft,
                child: Btn.primary(
                  'Make a link',
                  onTap: hours == null || model.busy
                      ? null
                      : () => unawaited(model.mint(hours: hours)),
                ),
              ),
              const SizedBox(height: Space.s16),
            ],
            // A failure with no link is a mint that was refused, and Try again
            // is the mint again. A failure over a live link is a revoke that
            // did not finish: the link is still there, and Try again is the
            // revoke again — never a second link.
            if (failure != null)
              StateMessage.error(
                message: failure,
                retry: StateAction(
                  'Try again',
                  onTap: made == null
                      ? () {
                          final hours = _chosenHours;
                          if (hours != null) {
                            unawaited(model.mint(hours: hours));
                          }
                        }
                      : () => unawaited(_revoke(context)),
                ),
              )
            else if (model.busy && made == null)
              const StateMessage.loading(lines: 2)
            else if (made == null)
              Text(
                'A link shows this thread’s status only, never its '
                'transcript.',
                style: e.small.tint(e.muted),
              ),
            if (made != null) ...[
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
                child: Btn(
                  'Revoke',
                  onTap: model.busy ? null : () => unawaited(_revoke(context)),
                ),
              ),
            ],
          ],
        );
      },
    );
  }

  /// Revokes the link, and closes the sheet once it is gone: the URL the
  /// sheet was showing no longer works.
  Future<void> _revoke(BuildContext context) async {
    await widget.model.revoke();
    if (widget.model.made != null || !context.mounted) return;
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
