import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';

/// The status link a thread's Share mints: the one URL the bridge gives out,
/// and the revoke that stops it working.
///
/// It is a model rather than a widget's state so the two rules it has — one
/// link in flight at a time, and a refusal is shown in the bridge's words —
/// are plain tests.
class ShareModel extends ChangeNotifier {
  ShareModel(
    BridgeClient client, {
    required this.agentId,
    // The sheet reads the expiry the bridge answered with, so the model needs
    // no clock of its own; the parameter stays so a caller that pins time
    // passes the same one it gives everything else.
    DateTime Function()? now,
  }) : _client = client;

  final BridgeClient _client;

  /// The agent whose status the link shows: a running thread's id is its
  /// agent id, which is what the route takes.
  final String agentId;

  bool _busy = false;
  StatusLinkCreated? _made;
  String? _failure;

  /// True while a link is being minted or revoked.
  bool get busy => _busy;

  /// The link made in this window, and its URL — shown once.
  StatusLinkCreated? get made => _made;

  /// What went wrong, in the bridge's words. Null when nothing has.
  String? get failure => _failure;

  /// Mints the link, for [hours], and keeps the one URL the bridge gives out.
  ///
  /// A refusal is reported rather than thrown: the sheet says it in the
  /// bridge's words, and no URL is shown for a link that was not made.
  Future<void> mint({int hours = 24}) async {
    _busy = true;
    _failure = null;
    notifyListeners();
    try {
      _made = await _client.createStatusLink(agentId, ttlHours: hours);
    } catch (error) {
      _made = null;
      _failure = '$error';
    } finally {
      _busy = false;
      notifyListeners();
    }
  }

  /// Revokes the link made in this window, so its URL stops working at once.
  ///
  /// Nothing to revoke is not a failure: the call returns without a request.
  Future<void> revoke() async {
    final made = _made;
    if (made == null) return;
    _busy = true;
    _failure = null;
    notifyListeners();
    try {
      await _client.revokeStatusLink(made.id);
      _made = null;
    } catch (error) {
      _failure = '$error';
    } finally {
      _busy = false;
      notifyListeners();
    }
  }
}
