import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';
import 'fleet_store.dart';
import 'pulls_store.dart';
import 'thread_ref.dart';

/// The five groups the Pull requests place lists, in its order (F5 decision
/// 10): ready for review, checks failing, being fixed, in progress, checks
/// unavailable.
enum PullGroup { ready, failing, beingFixed, inProgress, unavailable }

/// One pull request as a screen draws it: the record, the thread it came from,
/// its group, and the one-line summary of its checks.
///
/// [sending] and [error] are the app's own: a dispatch that has been posted
/// and not yet answered, and what the bridge said when it refused one.
@immutable
class PullView {
  const PullView({
    required this.record,
    required this.thread,
    required this.group,
    required this.checkSummary,
    this.sending = false,
    this.error,
  });

  final PullRecord record;
  final ThreadRef? thread;
  final PullGroup group;

  /// The checks in one line, in the card's own words.
  final String checkSummary;

  /// A dispatch for this pull request is in flight.
  final bool sending;

  /// Why the last dispatch failed, in the bridge's words.
  final String? error;

  PullView copyWith({bool? sending, String? error, bool clearError = false}) =>
      PullView(
        record: record,
        thread: thread,
        group: group,
        checkSummary: checkSummary,
        sending: sending ?? this.sending,
        error: clearError ? null : (error ?? this.error),
      );

  @override
  String toString() =>
      'PullView(${record.id}, ${group.name}'
      '${sending ? ', sending' : ''})';
}

/// The pull requests, grouped and summarised (F5 decisions 10–12).
///
/// It reads [PullsStore] — the one writer of pull state — and posts what a
/// person does with one: a fix, a stop, and the project's automatic-fixing
/// setting. A dispatch it posts is shown as sending at once and as accepted
/// when the bridge answers, never as fixed (decision 15).
class PullsModel extends ChangeNotifier {
  PullsModel(this._pulls, {required this.client}) {
    _pulls.addListener(_rebuild);
    _rebuild();
  }

  final PullsStore _pulls;

  /// The client that reaches a host, for the dispatch a person asks for.
  final BridgeClient Function(String host) client;

  /// What a view is doing that the record does not say: a dispatch in flight,
  /// and the refusal that ended one.
  final _overlays = <String, ({bool sending, String? error})>{};
  Map<PullGroup, List<PullView>> _groups = const {};

  /// Every group in [PullGroup] order, empty ones included. A screen omits the
  /// empty ones; the map keeps its order so it never has to sort them.
  Map<PullGroup, List<PullView>> get groups => _groups;

  /// How many pull requests wait for review: the sidebar's count.
  int get readyCount => _groups[PullGroup.ready]?.length ?? 0;

  /// Every view, in group order.
  List<PullView> get views => List.unmodifiable([
    for (final group in PullGroup.values) ...?_groups[group],
  ]);

  /// The views [thread] shipped, in group order. F6 reads it for a work item.
  List<PullView> forThread(ThreadRef thread) {
    final records = {for (final record in _pulls.forThread(thread)) record.id};
    return List.unmodifiable([
      for (final view in views)
        if (records.contains(view.record.id)) view,
    ]);
  }

  /// Dispatches a worker for [pull]'s first failing check.
  ///
  /// One click posts it: the mode defaults to a fix and the check is the one
  /// that is failing, so nothing else is asked. [note] is what the person
  /// suspects, when they offered one.
  Future<void> fix(PullView pull, {String mode = 'fix', String? note}) async {
    final check = _failingCheck(pull.record);
    final host = _hostOf(pull.record.id);
    if (check == null || host == null) return;
    final key = _key(host, pull.record.id);
    if (_overlays[key]?.sending ?? false) return;
    _overlays[key] = (sending: true, error: null);
    _rebuild();
    try {
      final record = await client(host).fixPull(
        pull.record.id,
        PullFixRequest(check: check.name, mode: mode, note: note),
      );
      _overlays.remove(key);
      _pulls.apply(host, record);
    } on BridgeFailure catch (failure) {
      if (failure.status == 409) {
        // A fix is already running: show it rather than reporting a refusal.
        // The bridge answers the running one's id, and the record carries it.
        _overlays.remove(key);
        await _reread(host, pull.record.id);
        return;
      }
      _overlays[key] = (sending: false, error: describeFailure(failure));
    } catch (error) {
      _overlays[key] = (sending: false, error: error.toString());
    }
    _rebuild();
  }

  /// Stops the worker on [pull].
  Future<void> stopFix(PullView pull) async {
    final host = _hostOf(pull.record.id);
    if (host == null) return;
    final key = _key(host, pull.record.id);
    if (_overlays[key]?.sending ?? false) return;
    _overlays[key] = (sending: true, error: null);
    _rebuild();
    try {
      await client(host).stopPullFix(pull.record.id);
      _overlays.remove(key);
      await _reread(host, pull.record.id);
      return;
    } on BridgeFailure catch (failure) {
      _overlays[key] = (sending: false, error: describeFailure(failure));
    } catch (error) {
      _overlays[key] = (sending: false, error: error.toString());
    }
    _rebuild();
  }

  /// Turns this project's automatic fixing on or off (decision 7). It is the
  /// caret's last item, and it changes a project setting rather than this pull
  /// request.
  Future<void> setAutoFix(String projectRoot, bool on) async {
    final host = _hostOfProject(projectRoot);
    if (host == null) return;
    await client(host).setProjectAutoFix(projectRoot, on);
  }

  /// Reads one record again, so a stop or a refused dispatch shows what the
  /// bridge holds now.
  Future<void> _reread(String host, String id) async {
    try {
      _pulls.apply(host, await client(host).pull(id));
    } catch (_) {
      // The next delta or read answers it.
    }
    _rebuild();
  }

  /// The first check failing on the record's current head: what a dispatch
  /// names.
  static PullCheck? _failingCheck(PullRecord record) {
    for (final check in record.checks) {
      if (check.state == CheckState.checkFailed) return check;
    }
    return null;
  }

  /// The checks in one line, in the words the card uses: a failing check by
  /// name, every check passed, the rest still running, or why they cannot be
  /// read at all.
  static String summaryOf(PullRecord record) {
    final unavailable = record.unavailable ?? '';
    if (unavailable.isNotEmpty) return 'Checks unavailable: $unavailable';
    final total = record.checks.length;
    if (total == 0) return 'No checks on this head yet';
    final failed = [
      for (final check in record.checks)
        if (check.state == CheckState.checkFailed) check.name,
    ];
    if (failed.isNotEmpty) {
      return '${failed.length} failing: ${failed.join(', ')}';
    }
    final passed = record.checks
        .where((check) => check.state == CheckState.checkPassed)
        .length;
    if (passed == total) return '$total of $total checks passed';
    return '$passed of $total checks passed, the rest running';
  }

  /// The group a record belongs to. A state this build does not know is shown
  /// as in progress: it claims neither that the pull request is ready nor that
  /// it failed.
  static PullGroup groupOf(PullRecord record) => switch (record.state) {
    PullState.pullReady => PullGroup.ready,
    PullState.pullFailing => PullGroup.failing,
    PullState.pullBeingFixed => PullGroup.beingFixed,
    PullState.pullInProgress => PullGroup.inProgress,
    PullState.pullChecksUnavailable => PullGroup.unavailable,
    PullState.unknown => PullGroup.inProgress,
  };

  /// The host whose index holds [id], or null when none does.
  String? _hostOf(String id) {
    for (final host in _pulls.hosts) {
      if (_pulls.byId(host, id) != null) return host;
    }
    return null;
  }

  /// The host that holds a pull request of [projectRoot], else the only host
  /// there is. A project's settings live on the daemon that knows it.
  String? _hostOfProject(String projectRoot) {
    for (final host in _pulls.hosts) {
      for (final record in _pulls.all(host: host)) {
        if (record.projectRoot == projectRoot) return host;
      }
    }
    final hosts = _pulls.hosts;
    return hosts.isEmpty ? null : hosts.first;
  }

  static String _key(String host, String id) => '$host|$id';

  void _rebuild() {
    if (_disposed) return;
    final grouped = <PullGroup, List<PullView>>{
      for (final group in PullGroup.values) group: <PullView>[],
    };
    for (final host in _pulls.hosts) {
      for (final record in _pulls.all(host: host)) {
        final overlay = _overlays[_key(host, record.id)];
        grouped[groupOf(record)]!.add(
          PullView(
            record: record,
            thread: _pulls.threadOf(host, record),
            group: groupOf(record),
            checkSummary: summaryOf(record),
            sending: overlay?.sending ?? false,
            error: overlay?.error,
          ),
        );
      }
    }
    for (final views in grouped.values) {
      views.sort((a, b) {
        final byTime = b.record.updatedAt.compareTo(a.record.updatedAt);
        return byTime != 0 ? byTime : a.record.id.compareTo(b.record.id);
      });
    }
    _groups = {
      for (final MapEntry(key: group, value: views) in grouped.entries)
        group: List.unmodifiable(views),
    };
    notifyListeners();
  }

  bool _disposed = false;

  @override
  void dispose() {
    _disposed = true;
    _pulls.removeListener(_rebuild);
    super.dispose();
  }
}
