import 'dart:async';

import 'package:flutter/services.dart';

import '../../model/connections_model.dart';
import '../../model/fleet_store.dart' show describeFailure;
import '../automations/confirm_action.dart';
import '../kit/kit.dart';

/// One forge's webhook, as Settings' Connections detail draws it (ported from
/// the Svelte client's `WebhookSetup.svelte`).
///
/// It states whether the bridge already holds the secret the forge signs its
/// deliveries with — "secret set", "no secret", or "secret unknown" when the
/// secrets backend cannot answer — and offers to create one, or, because
/// replacing breaks the forge's configured webhook until the new secret is
/// pasted, to replace it behind a confirmation. A secret the bridge issues is
/// shown once, in mono, with a note saying it will not be shown again.
///
/// The app does not know the bridge's origin, so the payload is named by its
/// path rather than by a URL the widget would have to invent.
class WebhookSetup extends StatefulWidget {
  const WebhookSetup({super.key, required this.model, required this.repoID});

  final ConnectionsModel model;

  /// The repository's id: the same id the secret is filed under, `hooks/<id>`.
  final String repoID;

  @override
  State<WebhookSetup> createState() => _WebhookSetupState();
}

class _WebhookSetupState extends State<WebhookSetup> {
  /// Whether a secret is set: true, false, or null when the secrets backend
  /// cannot say. Null before the first check answers, and after one that
  /// could not.
  bool? _isSet;

  /// The first check is in flight.
  bool _checking = true;

  /// A secret the bridge just issued: shown once, then never again.
  String? _secret;

  /// A create or replace is in flight.
  bool _busy = false;

  /// Replacing takes a second decision: the first click arms the confirm.
  bool _armed = false;

  /// Why the last create failed, in the bridge's own words.
  String? _error;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  @override
  void didUpdateWidget(WebhookSetup old) {
    super.didUpdateWidget(old);
    if (old.repoID != widget.repoID) unawaited(_load());
  }

  /// Asks whether a secret is already set. A backend that cannot answer is not
  /// an error here: the widget says the state is unknown, and Create secret
  /// reports why if issuing one fails.
  Future<void> _load() async {
    final id = widget.repoID;
    setState(() {
      _checking = true;
      _isSet = null;
      _secret = null;
      _armed = false;
      _error = null;
    });
    final set = await widget.model.webhookSecretSet(id);
    if (!mounted || id != widget.repoID) return;
    setState(() {
      _isSet = set;
      _checking = false;
    });
  }

  /// The create/replace button: replacing arms the confirmation first, because
  /// a new secret breaks the forge's configured webhook until it is pasted.
  void _begin() {
    if (_isSet == true) {
      setState(() => _armed = true);
      return;
    }
    unawaited(_create());
  }

  Future<void> _create() async {
    final id = widget.repoID;
    setState(() {
      _armed = false;
      _busy = true;
      _error = null;
    });
    try {
      final secret = await widget.model.createWebhookSecret(id);
      if (!mounted || id != widget.repoID) return;
      setState(() {
        _secret = secret;
        _isSet = true;
      });
    } catch (error) {
      if (!mounted || id != widget.repoID) return;
      setState(() => _error = describeFailure(error));
    } finally {
      if (mounted && id == widget.repoID) {
        setState(() => _busy = false);
      }
    }
  }

  Future<void> _copy(String text) async {
    try {
      await Clipboard.setData(ClipboardData(text: text));
    } catch (_) {
      // The value is on screen to copy by hand.
    }
  }

  /// The path the forge's deliveries arrive on. Only the path is named: the
  /// app does not know the bridge's origin, and inventing one would be a URL
  /// the forge cannot reach.
  String get _payloadPath =>
      '/hooks/forge/${Uri.encodeComponent(widget.repoID)}';

  String get _statusLabel => switch (_isSet) {
    true => 'secret set',
    false => 'no secret',
    null => 'secret unknown',
  };

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final secret = _secret;
    final error = _error;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (_checking)
          const StateMessage.loading(lines: 1)
        else ...[
          Wrap(
            spacing: Space.s8,
            runSpacing: Space.s8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Pill(_statusLabel),
              if (!_armed && !_busy)
                Btn(
                  _isSet == true ? 'Replace secret' : 'Create secret',
                  onTap: () => _begin(),
                ),
            ],
          ),
          if (_armed) ...[
            const SizedBox(height: Space.s12),
            ConfirmAction(
              question: 'Replace the webhook secret?',
              loss:
                  "The forge's configured webhook stops verifying deliveries "
                  'until you paste the new secret here.',
              confirmLabel: 'Replace it',
              onConfirm: () => unawaited(_create()),
              onCancel: () => setState(() => _armed = false),
            ),
          ],
        ],
        if (_isSet == null && !_checking) ...[
          const SizedBox(height: Space.s8),
          Text(
            'The secrets backend did not answer, so whether a secret is set '
            'is unknown.',
            style: e.small.tint(e.muted),
          ),
        ],
        if (secret != null) ...[
          const SizedBox(height: Space.s12),
          Box(
            tone: BoxTone.attention,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  'Copy the secret now. It is not shown again.',
                  style: e.bodyStrong,
                ),
                const SizedBox(height: Space.s8),
                Text(secret, style: e.mono),
                const SizedBox(height: Space.s12),
                Align(
                  alignment: Alignment.centerLeft,
                  child: Btn(
                    'Copy secret',
                    onTap: () => unawaited(_copy(secret)),
                  ),
                ),
              ],
            ),
          ),
        ],
        if (error != null) ...[
          const SizedBox(height: Space.s12),
          Text(error, style: e.small.tint(e.err)),
        ],
        const SizedBox(height: Space.s12),
        Text(
          "Point the forge's webhook at $_payloadPath on this bridge",
          style: e.body,
        ),
        const SizedBox(height: Space.s4),
        Text(
          'Content type JSON; the forge signs each delivery with this secret. '
          'Without a secret the bridge polls instead.',
          style: e.small.tint(e.muted),
        ),
      ],
    );
  }
}
