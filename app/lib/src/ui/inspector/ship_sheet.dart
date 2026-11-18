import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/changes_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../kit/kit.dart';

/// Opens the ship flow in a Sheet (f4-inspector.md decision 6).
Future<void> showShipSheet(BuildContext context, ChangesModel model) =>
    Sheet.show<void>(context, shipSheet(model));

/// The ship flow: the drafted commit message, editable; whether to open a
/// pull request; then Ship — or, over a gate that failed, Override and ship,
/// which needs a typed reason.
///
/// Every outcome (decision 7) replaces the form with its own view and what to
/// do next.
Sheet shipSheet(ChangesModel model) => Sheet(
  title: 'Ship this work',
  child: _Ship(model: model),
);

class _Ship extends StatefulWidget {
  const _Ship({required this.model});

  final ChangesModel model;

  @override
  State<_Ship> createState() => _ShipState();
}

class _ShipState extends State<_Ship> {
  late final _message = TextEditingController(
    text: widget.model.draft?.message ?? '',
  );
  final _reason = TextEditingController();
  final _patch = TextEditingController();

  bool _openPr = true;
  bool _busy = false;
  String? _failure;
  ShipResult? _result;

  /// Whether the gate ran and did not pass: shipping over it needs a reason.
  bool get _gateFailed {
    final gate = widget.model.gate;
    return gate != null && !gate.skipped && !gate.ok;
  }

  bool get _canShip {
    if (_busy) return false;
    if (_message.text.trim().isEmpty) return false;
    if (_gateFailed) return _reason.text.trim().isNotEmpty;
    return true;
  }

  @override
  void dispose() {
    _message.dispose();
    _reason.dispose();
    _patch.dispose();
    super.dispose();
  }

  Future<void> _ship() async {
    if (!_canShip) return;
    setState(() {
      _busy = true;
      _failure = null;
    });
    try {
      final result = await widget.model.ship(
        message: _message.text.trim(),
        openPr: _openPr,
        overrideReason: _gateFailed ? _reason.text.trim() : null,
      );
      if (!mounted) return;
      setState(() {
        _busy = false;
        _result = result;
      });
      if (result.outcome == ShipOutcome.patch) {
        final text = await widget.model.exportPatch();
        if (!mounted) return;
        setState(() => _patch.text = text);
      }
    } catch (error) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _failure = describeFailure(error);
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final result = _result;
    if (result != null) return _outcome(context, result);
    return _form(context);
  }

  Widget _form(BuildContext context) {
    final e = Ember.of(context);
    final failure = _failure;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Field(
          label: 'Commit message',
          controller: _message,
          hint: 'What this change does',
          onChanged: (_) => setState(() {}),
        ),
        const SizedBox(height: Space.s12),
        Row(
          children: [
            Btn(
              _openPr ? 'Open a pull request' : 'Push without a pull request',
              icon: _openPr ? Glyphs.checkmark : null,
              onTap: _busy ? null : () => setState(() => _openPr = !_openPr),
            ),
          ],
        ),
        if (_gateFailed) ...[
          const SizedBox(height: Space.s16),
          Text(
            'The gate failed. Overriding it needs a reason, and the reason is '
            'kept in the audit log.',
            style: e.small.tint(e.muted),
          ),
          const SizedBox(height: Space.s8),
          Field(
            label: 'Why this can ship anyway',
            controller: _reason,
            hint: 'The flaky test is unrelated to this change',
            onChanged: (_) => setState(() {}),
          ),
        ],
        if (failure != null) ...[
          const SizedBox(height: Space.s16),
          StateMessage.error(
            message: failure,
            retry: StateAction('Try again', onTap: _ship),
          ),
        ],
        const SizedBox(height: Space.s20),
        Row(
          children: [
            Btn.primary(
              _gateFailed
                  ? (_busy ? 'Shipping' : 'Override and ship')
                  : (_busy ? 'Shipping' : 'Ship'),
              onTap: _canShip ? _ship : null,
            ),
          ],
        ),
      ],
    );
  }

  /// One view per outcome, each saying what happened and what is next.
  Widget _outcome(BuildContext context, ShipResult result) => ShipOutcomeView(
    result: result,
    patch: _patch.text,
    // The ship did not finish, and the work may already be on the forge: the
    // form is still there, so trying again is the sheet's own action.
    onRetry: result.outcome == ShipOutcome.failed
        ? () {
            setState(() => _result = null);
            unawaited(_ship());
          }
        : null,
  );
}

/// The result of one ship attempt (decision 7): what happened, in that
/// outcome's own words, and the next step.
class ShipOutcomeView extends StatelessWidget {
  const ShipOutcomeView({
    super.key,
    required this.result,
    this.patch = '',
    this.onRetry,
  });

  final ShipResult result;

  /// The change set's patch text, for the patch outcome.
  final String patch;

  /// What Try again does, for an outcome that can be tried again. Null when
  /// the outcome is final.
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final (title, next) = switch (result.outcome) {
      ShipOutcome.pullRequest => (
        'Pull request opened',
        'Its checks run on the forge; Pull requests shows them as they finish.',
      ),
      ShipOutcome.pushed => (
        'Branch pushed',
        'Open a pull request on the forge when you are ready.',
      ),
      ShipOutcome.gateRefused => (
        'The gate refused',
        'Fix what it named, run the gate again, or override it with a reason.',
      ),
      ShipOutcome.forgeRefused => (
        'The forge refused',
        'Nothing was pushed. The forge’s reason is below.',
      ),
      ShipOutcome.patch => (
        'A patch, not a push',
        'This source is read-only. Copy the patch and apply it where the work '
            'belongs.',
      ),
      ShipOutcome.failed => (
        'The ship did not finish',
        'The bridge answers after it pushes, so this work may already be on '
            'the forge. Check the branch before trying again.',
      ),
    };
    final url = result.url;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(title, style: e.title),
        const SizedBox(height: Space.s8),
        Text(next, style: e.small.tint(e.muted)),
        if (url != null && url.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          Row(
            children: [
              Expanded(child: Text(url, style: e.mono)),
              const SizedBox(width: Space.s8),
              Btn(
                'Copy link',
                icon: Glyphs.doc_on_doc,
                onTap: () => Clipboard.setData(ClipboardData(text: url)),
              ),
            ],
          ),
        ],
        if (result.message.isNotEmpty) ...[
          const SizedBox(height: Space.s12),
          OutputBlock(text: result.message),
        ],
        if (result.outcome == ShipOutcome.patch) ...[
          const SizedBox(height: Space.s12),
          OutputBlock(text: patch, truncated: false),
        ],
        const SizedBox(height: Space.s20),
        Row(
          children: [
            if (result.outcome == ShipOutcome.failed && onRetry != null) ...[
              Btn('Try again', onTap: onRetry),
              const SizedBox(width: Space.s8),
            ],
            if (result.outcome == ShipOutcome.patch)
              Btn(
                'Copy patch',
                icon: Glyphs.doc_on_doc,
                onTap: () => Clipboard.setData(ClipboardData(text: patch)),
              ),
            const SizedBox(width: Space.s8),
            Btn('Done', onTap: () => Navigator.maybePop(context)),
          ],
        ),
      ],
    );
  }
}
