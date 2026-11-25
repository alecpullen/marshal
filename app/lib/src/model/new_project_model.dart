import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart' show BridgeFailure;
import 'fleet_store.dart' show describeFailure;

// A creation's own type reaches the sheet through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart' show CreationState, ProjectCreation;

/// The three ways a project is made from the Studio
/// (docs/studio-flutter/new-project.md decision 1).
enum NewProjectWay {
  blank('Blank', 'An empty git repository', NewProjectKind.newProjectBlank),
  clone(
    'Clone',
    'Clone a repository by its URL',
    NewProjectKind.newProjectClone,
  ),
  existing(
    'Existing folder',
    'A folder already on the host',
    NewProjectKind.newProjectExisting,
  );

  const NewProjectWay(this.word, this.note, this.kind);

  /// What the sheet's choice says.
  final String word;

  /// What the choice's tooltip says it does.
  final String note;

  /// The kind the bridge is asked for.
  final NewProjectKind kind;
}

/// Making a project on a host from the Studio: blank, cloned, or a folder the
/// host already has.
///
/// The host's projects folder and a name give the folder (decision 2): the
/// name follows a clone's URL, and the folder follows the name, each until it
/// is edited. A clone runs on the host after the bridge answers (decision 4),
/// so the model reads its creation until it has ended, and says what git said
/// meanwhile.
class NewProjectModel extends ChangeNotifier {
  NewProjectModel({
    required Map<String, BridgeClient> clients,
    required String host,
    this.poll = const Duration(seconds: 1),
  }) : _clients = Map.unmodifiable(clients),
       _host = clients.containsKey(host) ? host : clients.keys.first;

  final Map<String, BridgeClient> _clients;

  /// How long a clone is left between reads of its creation.
  final Duration poll;

  String _host;
  NewProjectWay _way = NewProjectWay.blank;
  String _url = '';
  String _name = '';
  String _path = '';
  String _branch = '';
  bool _nameEdited = false;
  bool _pathEdited = false;

  String? _projectsDir;
  bool _loading = false;
  String? _loadFailure;

  bool _busy = false;
  String? _failure;
  ProjectCreation? _creation;
  bool _disposed = false;

  /// Every host a project can be made on, in the order the Studio lists them.
  List<String> get hosts => List.unmodifiable(_clients.keys);

  /// The host the project is made on.
  String get host => _host;

  NewProjectWay get way => _way;
  String get url => _url;
  String get name => _name;

  /// The project's folder on the host, in full.
  String get path => _path;
  String get branch => _branch;

  /// The host's projects folder, once it has said.
  String? get projectsDir => _projectsDir;

  /// The host's projects folder is being read.
  bool get loading => _loading;

  /// Why the host's projects folder could not be read.
  String? get loadFailure => _loadFailure;

  /// A creation is being asked for, or a clone is running.
  bool get busy => _busy;

  /// Why the last creation failed, in the bridge's or git's words.
  String? get failure => _failure;

  /// What the bridge said of the creation last.
  ProjectCreation? get creation => _creation;

  /// git's latest progress line while a clone runs.
  String? get progress {
    final line = _creation?.progress;
    return line == null || line.isEmpty ? null : line;
  }

  /// The folder of the project the host made, once it is registered.
  String? get created =>
      _creation?.state == CreationState.creationReady ? _creation!.root : null;

  /// Why the project cannot be made yet, or null when it can.
  String? get problem {
    if (_way == NewProjectWay.clone && _url.trim().isEmpty) {
      return 'Give the repository URL';
    }
    if (_way != NewProjectWay.existing) {
      final name = _name.trim();
      if (name.isEmpty && !_pathEdited) return 'Name the project';
      if (name.contains('/') || name == '.' || name == '..') {
        return 'A name is one folder, with no "/"';
      }
    }
    final path = _path.trim();
    // An existing folder starts as the projects folder, which is never the
    // project itself: it is there to be finished.
    if (path.isEmpty || (_way == NewProjectWay.existing && !_pathEdited)) {
      return 'Give the folder';
    }
    if (!path.startsWith('/')) return 'The folder is a full path on $_host';
    return null;
  }

  /// Whether Create may be pressed.
  bool get canCreate => !_busy && created == null && problem == null;

  /// The folder name a URL suggests: its last segment, without `.git`.
  /// `git@github.com:acme/marshal.git` and
  /// `https://github.com/acme/marshal/` are both `marshal`.
  static String nameFor(String url) {
    var path = url.trim().split('#').first.split('?').first;
    path = path.replaceAll(RegExp(r'/+$'), '');
    final cut = path.lastIndexOf(RegExp('[/:]'));
    var name = cut < 0 ? path : path.substring(cut + 1);
    if (name.endsWith('.git')) name = name.substring(0, name.length - 4);
    return name;
  }

  /// Reads the host's projects folder, and fills the folder from it.
  Future<void> load() async {
    final client = _clients[_host]!;
    final asked = _host;
    _loading = true;
    _loadFailure = null;
    _notify();
    try {
      final options = await client.newProjectOptions();
      if (asked != _host) return;
      _projectsDir = options.projectsDir;
    } catch (error) {
      if (asked != _host) return;
      _loadFailure = describeFailure(error);
    } finally {
      if (asked == _host) {
        _loading = false;
        _follow();
        _notify();
      }
    }
  }

  /// Makes the project on [host] instead, and reads that host's folder.
  Future<void> selectHost(String host) async {
    if (host == _host || !_clients.containsKey(host)) return;
    _host = host;
    _projectsDir = null;
    _failure = null;
    await load();
  }

  void selectWay(NewProjectWay way) {
    if (way == _way) return;
    _way = way;
    _failure = null;
    if (way == NewProjectWay.clone && !_nameEdited) _name = nameFor(_url);
    _follow();
    _notify();
  }

  void setUrl(String url) {
    _url = url;
    _failure = null;
    if (!_nameEdited) _name = nameFor(url);
    _follow();
    _notify();
  }

  void setName(String name) {
    _name = name;
    _nameEdited = true;
    _failure = null;
    _follow();
    _notify();
  }

  void setPath(String path) {
    _path = path;
    _pathEdited = true;
    _failure = null;
    _notify();
  }

  void setBranch(String branch) {
    _branch = branch;
    _notify();
  }

  /// The folder follows the projects folder and the name until it is edited.
  /// An existing folder starts as the projects folder itself, for the person
  /// to finish.
  void _follow() {
    if (_pathEdited) return;
    final dir = _projectsDir;
    if (dir == null) return;
    final base = dir.endsWith('/') ? dir : '$dir/';
    _path = _way == NewProjectWay.existing ? base : '$base${_name.trim()}';
  }

  /// Asks the host to make the project, and for a clone, follows it to its
  /// end. A refusal is kept in the bridge's words, and what was typed stays.
  Future<void> create() async {
    if (!canCreate) return;
    final client = _clients[_host]!;
    _busy = true;
    _failure = null;
    _creation = null;
    _notify();
    try {
      final creation = await client.createProject(
        NewProjectRequest(
          kind: _way.kind,
          root: _path.trim(),
          url: _way == NewProjectWay.clone ? _url.trim() : null,
          branch: _way == NewProjectWay.clone && _branch.trim().isNotEmpty
              ? _branch.trim()
              : null,
        ),
      );
      _creation = creation;
      _notify();
      if (creation.state == CreationState.creationCloning) {
        await _watch(client, creation.id);
      }
      final ended = _creation;
      if (ended?.state == CreationState.creationFailed) {
        _failure = ended!.error ?? 'The clone failed';
      }
    } catch (error) {
      _failure = describeFailure(error);
    } finally {
      _busy = false;
      _notify();
    }
  }

  /// Reads the creation until its clone has ended, or the sheet has closed.
  /// A read that fails is tried again — the clone runs on the host either
  /// way — unless the host has forgotten the creation.
  Future<void> _watch(BridgeClient client, String id) async {
    while (!_disposed) {
      await Future<void>.delayed(poll);
      if (_disposed) return;
      try {
        final creation = await client.projectCreation(id);
        _creation = creation;
        _failure = null;
        _notify();
        if (creation.state != CreationState.creationCloning) return;
      } on BridgeFailure catch (error) {
        if (error.status == 404) {
          _failure =
              'The host no longer knows this clone: it may have restarted. '
              'Look in ${_path.trim()} before trying again.';
          return;
        }
        _failure = describeFailure(error);
        _notify();
      } catch (error) {
        _failure = describeFailure(error);
        _notify();
      }
    }
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }
}
