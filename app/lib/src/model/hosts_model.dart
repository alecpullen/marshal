import 'package:flutter/foundation.dart';

import '../client/fleet_profiles.dart';
import 'fleet_store.dart' show HostTunnel, HostView;
import 'section_model.dart';

/// What the add form holds: a host's name and the SSH destination it is
/// reached at.
@immutable
class HostDraft {
  const HostDraft({this.name = '', this.destination = ''});

  final String name, destination;
}

/// Settings' Hosts section (F7 decision 7): every host discovery found, with
/// what this app can reach of each.
///
/// A host appears because discovery found it: from the table `marshal fleet`
/// keeps in `fleet.toml`, or — in a browser — because the daemon that served
/// the page forwards to it (one-daemon D10). Both builds add and remove hosts
/// through [add] and [remove], which run on the daemon this app is attached
/// to: the one that keeps the tunnels (one-daemon D15).
class HostsModel extends SectionModel<HostView> {
  HostsModel(
    this._read, {
    this.profiles,
    this.rediscover,
    this.forget,
    this.tunnels,
  });

  /// A model already holding [hosts], for a test or the gallery.
  HostsModel.fixture(
    List<HostView> hosts, {
    this.profiles,
    this.rediscover,
    this.forget,
    this.tunnels,
  }) : _read = (() => hosts) {
    replaceItems(hosts);
  }

  /// The fleet store's rows, so a host that stops answering keeps its place
  /// and is marked with its last contact rather than dropped (F2 decision 8).
  final List<HostView> Function() _read;

  /// The hosts commands that edit `fleet.toml` (spec D6, D7), as the daemon
  /// this app is attached to runs them, or null in a build whose composition
  /// root gave it none.
  final FleetProfiles? profiles;

  /// Finds the hosts again, so the one just added is listed and the rows are
  /// read afresh.
  final Future<void> Function()? rediscover;

  /// Drops a removed host from the fleet (spec D9).
  final void Function(String host)? forget;

  /// Reads the tunnels the local daemon keeps, by host name (D7). Null in a
  /// browser, which has no daemon of its own to ask.
  final Future<Map<String, HostTunnel>> Function()? tunnels;

  /// The draft the open add form holds, so the shell's guard sees it.
  HostDraft? _draft;

  /// Whether this app can add a host: it can, with the daemon's command
  /// runner to add through.
  bool get canAdd => profiles?.supported ?? false;

  /// The open add form changed: a draft that holds anything is an edit the
  /// shell's guard asks about (design-system.md §9).
  void draftChanged(HostDraft? draft) {
    _draft = draft;
    notifyListeners();
  }

  @override
  bool get hasUnsavedEdits {
    final draft = _draft;
    final written =
        draft != null &&
        (draft.name.isNotEmpty || draft.destination.isNotEmpty);
    return written || super.hasUnsavedEdits;
  }

  @override
  void discardEdits() {
    _draft = null;
    super.discardEdits();
  }

  /// Adds a host: `fleet add NAME DEST`, then discovery.
  ///
  /// A failed add throws its words and stores nothing.
  Future<void> add(HostDraft draft) async {
    final name = draft.name.trim();
    final destination = draft.destination.trim();
    if (name.isEmpty || destination.isEmpty) {
      throw const FleetProfileFailure(
        'A host needs a name and an SSH destination',
      );
    }
    final runner = profiles;
    if (runner == null || !runner.supported) {
      throw const FleetProfileFailure('This build cannot add a host');
    }
    await runner.add(name, destination);
    await rediscover?.call();
    notifyListeners();
  }

  /// Removes a host and its table, then drops it from the fleet (spec D9).
  Future<void> remove(String host) async {
    final runner = profiles;
    if (runner == null || !runner.supported) {
      throw const FleetProfileFailure('This build cannot remove a host');
    }
    await runner.remove(host);
    forget?.call(host);
    notifyListeners();
  }

  /// The rows, each with the tunnel the local daemon keeps for it when a
  /// reader is set. A reader that fails leaves the rows as they are: a tunnel
  /// is an extra fact about a host, not a reason to say the host list failed.
  @override
  Future<List<HostView>> fetch() async {
    final rows = _read();
    final readTunnels = tunnels;
    if (readTunnels == null) return rows;
    final Map<String, HostTunnel> kept;
    try {
      kept = await readTunnels();
    } catch (_) {
      return rows;
    }
    return [for (final row in rows) row.withTunnel(kept[row.name])];
  }

  @override
  Object keyOf(HostView item) => item.name;

  @override
  bool isNew(HostView item) => false;

  /// Nothing to write: the detail of a host states what it is. Adding and
  /// removing are [add] and [remove], which run the fleet's own commands.
  @override
  Future<HostView> persist(HostView item) async => item;
}
