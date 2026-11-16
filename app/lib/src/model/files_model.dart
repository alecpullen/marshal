import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';

/// One file's text as the viewer reads it.
@immutable
class FileText {
  const FileText({
    required this.path,
    required this.text,
    this.binary = false,
    this.truncated = false,
  });

  final String path;
  final String text;

  /// The agent found no text in it, so there is nothing to show.
  final bool binary;

  /// The agent capped it: the viewer says so rather than showing half a file
  /// as the whole one.
  final bool truncated;
}

/// The agent's workspace, read a directory at a time (f4-inspector.md
/// decision 8).
///
/// Nothing is fetched until a directory is opened, and nothing outside the
/// workspace is asked for: a path that climbs out is refused here, before it
/// reaches the bridge. The model imports nothing from Flutter beyond
/// `foundation.dart`, so its rules are plain unit tests (model/README.md).
class FilesModel extends ChangeNotifier {
  FilesModel(
    this._client, {
    required this.agentId,
    Iterable<String> changed = const [],
    this.root = '',
  }) : _changed = {...changed};

  /// A model holding what a caller gives it, for a test or the gallery.
  FilesModel.seeded({
    Iterable<String> changed = const [],
    Map<String, List<FileEntry>> tree = const {},
    Map<String, String> files = const {},
    this.root = '',
  }) : _client = null,
       agentId = '',
       _changed = {...changed} {
    _files.addAll(files);
    for (final entry in tree.entries) {
      _children[entry.key] = entry.value;
    }
    // The root is open; a directory below it opens on its first click, as it
    // does when read from the bridge.
    _expanded.add('');
  }

  final BridgeClient? _client;
  final String agentId;
  final Set<String> _changed;
  final Map<String, List<FileEntry>> _children = {};
  final Set<String> _expanded = {};
  final Set<String> _loading = {};
  final Map<String, String> _errors = {};

  /// The files a seeded model holds, by path: a fixture's content.
  final Map<String, String> _files = {};

  /// The workspace directory the paths are relative to, once it has been
  /// read.
  String root;

  /// The entries of [path] that have been read already; empty until the
  /// directory is expanded.
  List<FileEntry> children(String path) => _children[path] ?? const [];

  /// Whether [path] is open.
  bool expanded(String path) => _expanded.contains(path);

  /// Whether [path]'s entries are being read.
  bool loading(String path) => _loading.contains(path);

  /// Why [path] could not be read, in words, or null.
  String? errorFor(String path) => _errors[path];

  /// Whether this thread changed [path] (decision 8's marker).
  bool changed(String path) => _changed.contains(path);

  /// Replaces the set of changed paths: what the change set names.
  void setChanged(Iterable<String> paths) {
    _changed
      ..clear()
      ..addAll(paths);
    notifyListeners();
  }

  /// Whether [path] is inside the workspace. A path that is absolute or
  /// climbs out with `..` is not.
  static bool inside(String path) {
    if (path.startsWith('/')) return false;
    for (final segment in path.split('/')) {
      if (segment == '..') return false;
    }
    return true;
  }

  /// Opens [path] if it is closed, and closes it if it is open.
  Future<void> toggle(String path) async {
    if (_expanded.remove(path)) {
      notifyListeners();
      return;
    }
    await expand(path);
  }

  /// Reads [path]'s entries, once. A second call is free.
  Future<void> expand(String path) async {
    if (!inside(path)) {
      _errors[path] = 'That path is outside the workspace.';
      notifyListeners();
      return;
    }
    if (_children.containsKey(path)) {
      if (_expanded.add(path)) notifyListeners();
      return;
    }
    _errors.remove(path);
    _expanded.add(path);
    final client = _client;
    if (client == null) {
      notifyListeners();
      return;
    }
    _loading.add(path);
    notifyListeners();
    try {
      final list = await client.files(agentId, path);
      _children[path] = list.entries;
      if (list.root.isNotEmpty) root = list.root;
    } catch (error) {
      _expanded.remove(path);
      _errors[path] = '$error';
    } finally {
      _loading.remove(path);
      notifyListeners();
    }
  }

  /// Reads [path]'s content.
  Future<FileText> open(String path) async {
    if (!inside(path)) {
      return FileText(path: path, text: 'That path is outside the workspace.');
    }
    final seeded = _files[path];
    if (seeded != null) return FileText(path: path, text: seeded);
    final client = _client;
    if (client == null) return FileText(path: path, text: '');
    final file = await client.file(agentId, path);
    return FileText(
      path: file.path,
      text: file.content,
      binary: file.binary,
      truncated: file.truncated,
    );
  }
}
