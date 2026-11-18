import 'package:flutter/foundation.dart';

import '../client/fleet_profiles.dart';
import 'fleet_store.dart' show HostView, describeFailure;
import 'section_model.dart';

/// What the add form holds: a server profile's name, its URL, the file whose
/// certificates it is trusted with, and the token to sign in to it with.
@immutable
class HostDraft {
  const HostDraft({
    this.name = '',
    this.url = '',
    this.caFile = '',
    this.token = '',
  });

  final String name, url, caFile, token;
}

/// Settings' Hosts section (F7 decision 7): every host discovery found, with
/// what this app can reach of each.
///
/// A host appears because discovery found it or because `marshal fleet
/// servers` lists it (decision 9). A build that can run `marshal fleet` adds,
/// removes and signs in to a server profile through [add], [remove] and
/// [signIn]; a browser cannot, and its add row stays planned.
class HostsModel extends SectionModel<HostView> {
  HostsModel(
    this._read, {
    this.profiles,
    this.rediscover,
    this.forget,
    this.retry,
    this.storeToken,
    this.clearToken,
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

  /// Stores [host]'s token: `marshal fleet login` in a native build, and the
  /// host's own browser store in a page (decision 6). Null when this build has
  /// neither, which is what [signIn] falls back on [profiles] for.
  final Future<void> Function(String host, String token)? storeToken;

  /// Forgets [host]'s token. It is the browser's alone: a native build keeps no
  /// token of its own, and `marshal fleet token` resolves one when a request
  /// needs it (decision 6).
  final void Function(String host)? clearToken;

  /// What a failed sign-in said, by host: the add already worked, and only
  /// the token is wrong (decision 5).
  final _loginFailures = <String, String>{};

  /// The draft the open add form holds, so the shell's guard sees it.
  HostDraft? _draft;

  /// Whether this build can add a server profile: a browser cannot.
  bool get canAdd => profiles?.supported ?? false;

  /// What the last sign-in to [host] said, or null when it never failed.
  String? loginFailure(String host) => _loginFailures[host];

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
        (draft.name.isNotEmpty ||
            draft.url.isNotEmpty ||
            draft.caFile.isNotEmpty ||
            draft.token.isNotEmpty);
    return written || super.hasUnsavedEdits;
  }

  @override
  void discardEdits() {
    _draft = null;
    super.discardEdits();
  }

  /// Adds a server profile: `fleet add`, then `fleet login` when a token was
  /// given, then discovery.
  ///
  /// A failed add throws its words and stores nothing. A failed login does
  /// not: the profile is added, the form closes as added, and the host's
  /// detail says why beside Sign in (decision 5).
  Future<void> add(HostDraft draft) async {
    final name = draft.name.trim();
    final url = draft.url.trim();
    if (name.isEmpty || url.isEmpty) {
      throw const FleetProfileFailure('A host needs a name and a URL');
    }
    final uri = Uri.tryParse(url);
    if (uri == null) {
      throw const FleetProfileFailure('That URL could not be read');
    }
    final runner = profiles;
    if (runner == null || !runner.supported) {
      throw const FleetProfileFailure('This build cannot add a host');
    }
    final caFile = draft.caFile.trim();
    await runner.add(name, uri, caFile: caFile.isEmpty ? null : caFile);
    final token = draft.token.trim();
    if (token.isNotEmpty) {
      try {
        await runner.login(name, token);
        _loginFailures.remove(name);
      } catch (failure) {
        _loginFailures[name] = describeFailure(failure);
      }
    }
    await rediscover?.call();
    notifyListeners();
  }

  /// Removes a server profile and its stored token, then drops it from the
  /// fleet (spec D9).
  Future<void> remove(String host) async {
    final runner = profiles;
    if (runner == null) return;
    await runner.remove(host);
    _loginFailures.remove(host);
    forget?.call(host);
    notifyListeners();
  }

  /// Signs in to [host] with [token], then asks it to try again: a refused
  /// token stopped its stream, and waiting would never clear it (decision 6).
  ///
  /// Where the build keeps a token of its own the token is stored through
  /// [storeToken] — a browser writes the host's own key (spec D21) — and
  /// everywhere else through `marshal fleet login`, as plan 2 built it.
  Future<void> signIn(String host, String token) async {
    final store = storeToken;
    if (store == null) {
      final runner = profiles;
      if (runner == null) {
        throw const FleetProfileFailure('This build cannot sign in to a host');
      }
      await runner.login(host, token);
    } else {
      await store(host, token);
    }
    _loginFailures.remove(host);
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

  @override
  Future<List<HostView>> fetch() async => _read();

  @override
  Object keyOf(HostView item) => item.name;

  @override
  bool isNew(HostView item) => false;

  /// Nothing to write: the detail of a host states what it is. Adding and
  /// removing are [add] and [remove], which run the fleet's own commands.
  @override
  Future<HostView> persist(HostView item) async => item;
}
