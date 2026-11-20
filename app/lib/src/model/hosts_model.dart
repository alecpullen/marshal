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
/// A host appears because discovery found it, from the table `marshal fleet`
/// keeps in `fleet.toml`. A native build adds and removes hosts by running
/// those commands through [add] and [remove]; a browser cannot, and its add
/// row stays planned. The browser does sign in to the hosts a bridge relays
/// to it, until plan 3 replaces that with the daemon's own tunnels.
class HostsModel extends SectionModel<HostView> {
  HostsModel(
    this._read, {
    this.profiles,
    this.rediscover,
    this.forget,
    this.retry,
    this.storeToken,
    this.clearToken,
    this.tunnels,
  });

  /// A model already holding [hosts], for a test or the gallery.
  HostsModel.fixture(
    List<HostView> hosts, {
    this.profiles,
    this.rediscover,
    this.forget,
    this.retry,
    this.storeToken,
    this.clearToken,
    this.tunnels,
  }) : _read = (() => hosts) {
    replaceItems(hosts);
  }

  /// The fleet store's rows, so a host that stops answering keeps its place
  /// and is marked with its last contact rather than dropped (F2 decision 8).
  final List<HostView> Function() _read;

  /// The `marshal fleet` commands that edit `fleet.toml` (spec D6, D7), or
  /// null in a build whose composition root gave it none.
  final FleetProfiles? profiles;

  /// Finds the hosts again, so the one just added is listed and the rows are
  /// read afresh.
  final Future<void> Function()? rediscover;

  /// Drops a removed host from the fleet (spec D9).
  final void Function(String host)? forget;

  /// Asks a host to try its stream again after its token changed.
  final void Function(String host)? retry;

  /// Stores [host]'s token in the host's own browser store (decision 6). Null
  /// in a native build, which signs in to nothing: the daemon reaches a host
  /// over its own tunnel.
  final Future<void> Function(String host, String token)? storeToken;

  /// Forgets [host]'s token. It is the browser's alone: a native build keeps no
  /// token of its own.
  final void Function(String host)? clearToken;

  /// Reads the tunnels the local daemon keeps, by host name (D7). Null in a
  /// browser, which has no daemon of its own to ask.
  final Future<Map<String, HostTunnel>> Function()? tunnels;

  /// The draft the open add form holds, so the shell's guard sees it.
  HostDraft? _draft;

  /// Whether this build can add a host: a browser cannot.
  bool get canAdd => profiles?.supported ?? false;

  /// Whether this build can sign in to a host: only one that keeps a token of
  /// its own does (decision 6). A native build signs in to nothing, because
  /// the daemon's tunnel carries its hosts.
  bool get canSignIn => storeToken != null;

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

  /// Signs in to [host] with [token], then asks it to try again: a refused
  /// token stopped its stream, and waiting would never clear it (decision 6).
  ///
  /// Only a build that keeps a token of its own signs in: a browser writes the
  /// host's own key through [storeToken] (spec D21). A native build has
  /// nothing to sign in to — the daemon's tunnel is the way to a host — and
  /// says so.
  Future<void> signIn(String host, String token) async {
    final store = storeToken;
    if (store == null) {
      throw const FleetProfileFailure('This build cannot sign in to a host');
    }
    await store(host, token);
    retry?.call(host);
    notifyListeners();
  }

  /// Whether [host] can be signed out of: a relayed host, in a build that keeps
  /// the token that reached it (decision 6). A native build has nothing to
  /// clear, and a host reached directly carries the page's own token.
  bool canSignOut(HostView host) => clearToken != null && host.via != null;

  /// Forgets [host]'s token, then asks it to try again, so the host reads
  /// "token refused" again until it is signed in to with a new one
  /// (decision 6, spec D22).
  Future<void> signOut(String host) async {
    final clear = clearToken;
    if (clear == null) return;
    clear(host);
    retry?.call(host);
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
