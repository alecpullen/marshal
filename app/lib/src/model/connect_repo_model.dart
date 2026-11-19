import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'fleet_store.dart' show describeFailure;

// A repository's own type reaches its screens through the model, so `ui/`
// reads `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart' show Repo;

/// Registering a repository from the Studio (F9 B1): the id the URL suggests,
/// the bridge's refusal in its own words, and the discovery that follows.
///
/// A repository is registered for issue intake, not to create a project: a
/// project appears when the daemon discovers one (f9-parity decision 1).
class ConnectRepoModel extends ChangeNotifier {
  ConnectRepoModel(this._client);

  final BridgeClient _client;

  bool _busy = false;
  String? _failure;
  Repo? _registered;

  /// A register is in flight: the sheet's button says so and does nothing.
  bool get busy => _busy;

  /// Why the last register or discovery failed, in the bridge's own words.
  String? get failure => _failure;

  /// The repository the bridge registered, once it has.
  Repo? get registered => _registered;

  /// The id a URL suggests: the last two path segments, joined by '-', lower
  /// cased, with anything outside `[a-z0-9._-]` dropped and runs of '-' folded.
  /// `https://github.com/acme/marshal.git` becomes `acme-marshal`.
  ///
  /// A trailing slash and a query string name nothing extra, so neither
  /// changes the answer.
  static String idFor(String url) {
    final path = url.split('#').first.split('?').first;
    final segments = [
      for (final segment in path.split('/'))
        if (segment.isNotEmpty) segment,
    ];
    final tail = segments.length <= 2
        ? segments
        : segments.sublist(segments.length - 2);
    var id = tail.join('-');
    if (id.endsWith('.git')) id = id.substring(0, id.length - 4);
    id = id.toLowerCase();
    id = id.replaceAll(RegExp(r'[^a-z0-9._-]'), '-');
    id = id.replaceAll(RegExp('-+'), '-');
    return id.replaceAll(RegExp(r'^-+|-+$'), '');
  }

  /// Registers [url] under [id] through the bridge, and keeps what it answered.
  /// A refusal is kept in the bridge's words so the sheet can say what it said.
  Future<void> register({
    required String url,
    required String id,
    String forge = 'github',
    String? credRef,
    String? branch,
  }) async {
    _busy = true;
    _failure = null;
    notifyListeners();
    try {
      _registered = await _client.registerRepo(
        Repo(
          id: id,
          url: url,
          ownerID: '',
          forge: forge,
          credRef: credRef,
          branch: branch,
        ),
      );
    } catch (error) {
      _registered = null;
      _failure = describeFailure(error);
    }
    _busy = false;
    notifyListeners();
  }

  /// Discovers the repository just registered: what makes its issues appear.
  /// What the report answers is not shown here, only whether it ran.
  Future<void> discover() async {
    final repo = _registered;
    if (repo == null) return;
    _failure = null;
    notifyListeners();
    try {
      await _client.discoverRepo(repo.id);
    } catch (error) {
      _failure = describeFailure(error);
    }
    notifyListeners();
  }
}
