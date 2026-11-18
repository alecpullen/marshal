import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A watch reaches its screens through the model, so `ui/` reads `model/` and
// never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart'
    show
        LastReroute,
        OnTrip,
        RerouteRule,
        WatchCreate,
        WatchInfo,
        WatchSample,
        WatchSpec,
        WatchStarted;

/// Whether a watch has tripped: its own state says so, or it fired at some
/// point (F7 decision 4).
///
/// It is top level so [SignalsStore] derives a host's tripped watches from the
/// rows it read without copying the filter `WatchesModel.tripped` uses.
bool watchTripped(WatchInfo watch) =>
    watch.state == 'fired' || (watch.lastFiredAt ?? 0) > 0;

/// How long ago a Unix-millisecond time was, short (§8): "4m", "1h 12m".
///
/// [WatchesModel.ageLabel] keeps its own name and signature; this is the body
/// both it and [trippedWatchOf] use.
String ageLabelSince(int? millis, DateTime now) {
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

/// One tripped watch as the Overview's Automations card lists it: on [host],
/// what it did, and when (F7 decision 4, plan Task 3).
TrippedWatch trippedWatchOf(
  WatchInfo watch, {
  required String host,
  required DateTime now,
}) => TrippedWatch(
  host: host,
  name: watch.name,
  agent: watch.agentID,
  action: WatchesModel.describeOnTrip(watch),
  age: ageLabelSince(watch.lastFiredAt, now),
);

/// A watch that tripped, as the Overview's Automations card lists it: the host
/// it tripped on, what it watches, what it did, and when (F7 decision 4, plan
/// Task 3).
@immutable
class TrippedWatch {
  const TrippedWatch({
    required this.host,
    required this.name,
    required this.agent,
    required this.action,
    required this.age,
  });

  /// The host whose watch tripped.
  final String host;

  final String name;

  /// The agent that started it, or 'studio' for the bridge's own session.
  final String agent;

  /// What it did when it tripped.
  final String action;

  /// How long ago it tripped, short: "4m".
  final String age;

  @override
  String toString() => 'TrippedWatch($host/$name, $agent)';
}

/// The roles and presets a Studio watch's reroute may name, as the models
/// config reports them (F7 decision 4). Both are empty when the bridge could
/// not answer, which the form reads as "no reroute available".
@immutable
class WatchChoices {
  const WatchChoices({this.roles = const [], this.presets = const []});

  final List<String> roles;
  final List<String> presets;

  bool get isEmpty => roles.isEmpty && presets.isEmpty;
}

/// The Watches section of Automations (F7 decision 4): what is watched, on
/// what condition, a day's readings, the action on trip and the state.
///
/// A watch is started by an agent or by the bridge, not edited here, so this
/// section has no save; what it adds is the undo of a reroute a trip made.
class WatchesModel extends SectionModel<WatchInfo> {
  WatchesModel(
    this._bridge, {
    required this.now,
    required this.host,
    this.onWrote,
  }) : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  WatchesModel.fixture(
    List<WatchInfo> rows, {
    required this.now,
    required this.host,
    this.onWrote,
  }) : _bridge = null,
       _fixture = rows;

  final BridgeClient? _bridge;
  final List<WatchInfo>? _fixture;

  /// The host whose watches these are.
  final String host;

  /// What a trip's age is measured against.
  final DateTime now;

  /// How a write tells the host's signals to be read again (spec D4): called
  /// once after a watch is started, and never for a start the bridge refused.
  final void Function()? onWrote;

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
        if (watchTripped(watch)) watch,
    ];
    fired.sort((a, b) {
      final byTime = (b.lastFiredAt ?? 0).compareTo(a.lastFiredAt ?? 0);
      return byTime != 0
          ? byTime
          : keyOf(a).toString().compareTo(keyOf(b).toString());
    });
    return List.unmodifiable([
      for (final watch in fired) trippedWatchOf(watch, host: host, now: now),
    ]);
  }

  /// POST /api/reroutes/{id}/undo: puts back the role binding a trip
  /// changed.
  ///
  /// The id comes from the watch row's `lastReroute`
  /// (web/bridge/contract/watches.go), which the bridge fills in while it
  /// still holds the record; a trip whose record is gone carries none, and
  /// the screen says so instead of offering the button.
  Future<void> undoReroute(String rerouteID) =>
      _requireBridge().undoReroute(rerouteID);

  /// POST /api/watches: starts a watch, then reads the list again so the new
  /// row is the bridge's own report of it rather than what this model guessed.
  Future<WatchStarted> create(WatchCreate request) async {
    final started = await _requireBridge().createWatch(request);
    // The watch is started: the host's signals changed, so they are read again
    // whether or not the list read below answers (spec D4).
    onWrote?.call();
    await load();
    return started;
  }

  /// DELETE /api/watches/{owner}/{id}: stops one. [owner] is the watch's agent
  /// id, or `studio` for the bridge's own control session — it is the row's
  /// `agentId`, which is what the route's path takes.
  Future<void> stop(WatchInfo watch) async {
    await _requireBridge().stopWatch(watch.agentID, watch.id);
    await load();
  }

  /// The roles and presets a Studio watch's reroute may name, read once for
  /// the form. A bridge that cannot answer leaves both empty, and the form
  /// says the reroute is unavailable rather than inventing choices.
  Future<WatchChoices> choices() async {
    final bridge = _bridge;
    if (bridge == null) return const WatchChoices();
    final config = await bridge.models();
    return WatchChoices(
      roles: List.unmodifiable(config.roles),
      presets: List.unmodifiable(
        [for (final name in config.presets.keys) name]..sort(),
      ),
    );
  }

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
  String ageLabel(int? millis) => ageLabelSince(millis, now);

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
