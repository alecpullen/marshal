import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A watch reaches its screens through the model, so `ui/` reads `model/` and
// never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart'
    show OnTrip, RerouteRule, WatchInfo, WatchSample;

/// A watch that tripped, as the Overview's Automations card lists it: what it
/// watches, what it did, and when (F7 decision 4, plan Task 3).
@immutable
class TrippedWatch {
  const TrippedWatch({
    required this.name,
    required this.agent,
    required this.action,
    required this.age,
  });

  final String name;

  /// The agent that started it, or 'studio' for the bridge's own session.
  final String agent;

  /// What it did when it tripped.
  final String action;

  /// How long ago it tripped, short: "4m".
  final String age;

  @override
  String toString() => 'TrippedWatch($name, $agent)';
}

/// The Watches section of Automations (F7 decision 4): what is watched, on
/// what condition, a day's readings, the action on trip and the state.
///
/// A watch is started by an agent or by the bridge, not edited here, so this
/// section has no save; what it adds is the undo of a reroute a trip made.
class WatchesModel extends SectionModel<WatchInfo> {
  WatchesModel(this._bridge, {required this.now}) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  WatchesModel.fixture(List<WatchInfo> rows, {required this.now})
    : _bridge = null,
      _fixture = rows;

  final BridgeClient? _bridge;
  final List<WatchInfo>? _fixture;

  /// What a trip's age is measured against.
  final DateTime now;

  @override
  Future<List<WatchInfo>> fetch() async =>
      _fixture ?? await _requireBridge().watches();

  @override
  Object keyOf(WatchInfo watch) => '${watch.agentID}|${watch.id}';

  @override
  bool isNew(WatchInfo watch) => false;

  @override
  Future<WatchInfo> persist(WatchInfo watch) =>
      throw StateError('a watch is started by an agent, not edited here');

  /// The watches that tripped, most recent first, as the Overview's card
  /// lists them.
  List<TrippedWatch> get tripped {
    final fired = [
      for (final watch in items)
        if (watch.state == 'fired' || (watch.lastFiredAt ?? 0) > 0) watch,
    ];
    fired.sort((a, b) {
      final byTime = (b.lastFiredAt ?? 0).compareTo(a.lastFiredAt ?? 0);
      return byTime != 0
          ? byTime
          : keyOf(a).toString().compareTo(keyOf(b).toString());
    });
    return List.unmodifiable([
      for (final watch in fired)
        TrippedWatch(
          name: watch.name,
          agent: watch.agentID,
          action: describeOnTrip(watch),
          age: ageLabel(watch.lastFiredAt),
        ),
    ]);
  }

  /// POST /api/reroutes/{id}/undo: puts back the role binding a trip
  /// changed.
  ///
  /// The bridge's watch row does not report which reroute record its last
  /// trip made (web/bridge/contract/watches.go), so no screen can offer this
  /// yet: it is here for the row that carries the id.
  Future<void> undoReroute(String rerouteID) =>
      _requireBridge().undoReroute(rerouteID);

  /// What a watch does when it trips, in the words the row and the detail
  /// use.
  static String describeOnTrip(WatchInfo watch) {
    final reroute = watch.onTrip?.reroute;
    if (reroute != null) {
      return 'Reroutes ${reroute.role} to ${reroute.preset}';
    }
    if (watch.onTrip?.resume == true || watch.resume == true) {
      return 'Resumes the agent that started it';
    }
    if (watch.onTrip?.notify == true || watch.notify == true) {
      return 'Notifies you';
    }
    return 'Stays quiet';
  }

  /// How long ago a Unix-millisecond time was, short (§8): "4m", "1h 12m".
  String ageLabel(int? millis) {
    if (millis == null || millis <= 0) return 'just now';
    final then = DateTime.fromMillisecondsSinceEpoch(millis, isUtc: true);
    final span = now.difference(then);
    if (span.inMinutes < 1) return 'now';
    if (span.inMinutes < 60) return '${span.inMinutes}m';
    if (span.inHours < 24) {
      final rest = span.inMinutes - span.inHours * 60;
      return rest == 0 ? '${span.inHours}h' : '${span.inHours}h ${rest}m';
    }
    return '${span.inDays}d';
  }

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
