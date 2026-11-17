import 'package:flutter/foundation.dart';

/// A plan revision being written (F6 decision 6, F6 plan 1 task 5): the plan
/// that was approved, the plan as it is proposed, and what sending it did.
///
/// A proposal names the revision and hash it was written against, so one made
/// against a plan that has since moved is refused instead of silently
/// rebasing. The model holds no transport: the screen hands it the one call it
/// makes, and the store builds the command.
class RevisionModel extends ChangeNotifier {
  RevisionModel({
    required this.approved,
    required this.spec,
    required this.baseRevision,
    required this.baseHash,
    required this.submit,
  }) : proposal = approved;

  /// The plan that is in force, which the proposal is shown against.
  final String approved;

  /// The specification the proposal carries unchanged.
  final String spec;

  /// The approved revision and its hash: what the proposal names as its base.
  final int baseRevision;
  final String baseHash;

  /// Sends the proposal. The screen supplies it, so the model never touches a
  /// client (F1's boundary).
  final Future<void> Function(String plan) submit;

  /// The plan as it is proposed.
  String proposal;

  bool _sending = false;
  String? _error;
  bool _disposed = false;

  /// Whether the proposal is being sent.
  bool get sending => _sending;

  /// Why the last send failed, in the bridge's own words.
  String? get error => _error;

  /// Whether the proposal says anything the approved plan does not.
  bool get changed => proposal.trim() != approved.trim();

  /// Whether it can be sent: it must say something, and something different.
  bool get canSend => changed && proposal.trim().isNotEmpty && !_sending;

  /// Replaces the proposed plan.
  void change(String plan) {
    if (plan == proposal) return;
    proposal = plan;
    _error = null;
    notifyListeners();
  }

  /// Sends the proposal. A failure keeps the text so it can be corrected and
  /// sent again.
  Future<void> send() async {
    if (!canSend) return;
    _sending = true;
    _error = null;
    notifyListeners();
    try {
      await submit(proposal);
    } catch (error) {
      _error = error.toString();
    } finally {
      _sending = false;
      if (!_disposed) notifyListeners();
    }
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }
}
