import 'dart:async';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/event_stream.dart';
import 'section_model.dart';

/// A failure of the person's own making: an import with no name or no path.
@immutable
class EnvironmentError implements Exception {
  const EnvironmentError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// One version as the builds view draws it: its number, what it is, and the
/// state it is in.
@immutable
class EnvironmentVersion {
  const EnvironmentVersion({
    required this.n,
    required this.detail,
    required this.state,
  });

  final int n;

  /// Its image tag, or its digest when it has no tag.
  final String detail;

  /// pending, building, ok or failed.
  final String state;
}

/// A template's builds, as the section draws them.
@immutable
class EnvironmentBuilds {
  const EnvironmentBuilds({
    required this.versions,
    required this.poolSize,
    required this.poolIdle,
    required this.poolStarting,
    required this.coldMs,
    required this.warmMs,
    required this.sizeBytes,
  });

  final List<EnvironmentVersion> versions;
  final int poolSize;
  final int poolIdle;
  final int poolStarting;
  final int coldMs;
  final int warmMs;

  /// The newest build's size, or 0 before one exists.
  final int sizeBytes;
}

/// One build's log, streamed from its event route.
///
/// The model owns the subscription so the section never holds a client
/// stream: it starts it, keeps the lines and notifies as they arrive.
class BuildLog extends ChangeNotifier {
  BuildLog(this._stream);

  final EventStream? _stream;
  StreamSubscription<StreamEvent>? _subscription;
  final _lines = <String>[];

  /// The lines that have arrived, oldest first.
  List<String> get lines => List.unmodifiable(_lines);

  /// Starts reading the build's log from its first line.
  void start() {
    final stream = _stream;
    if (stream == null) return;
    _subscription = stream.events.listen((event) {
      _lines.add(event.data);
      notifyListeners();
    });
    stream.start();
  }

  @override
  void dispose() {
    _subscription?.cancel();
    _stream?.stop();
    super.dispose();
  }
}

/// One row of the Environments gallery (F7 decision 6): a template's name, its
/// contents in one line, the version published and how many agents run on it.
@immutable
class EnvironmentRow {
  const EnvironmentRow({
    required this.name,
    required this.source,
    required this.project,
    required this.contents,
    required this.version,
    required this.usage,
    this.draftChanges = false,
  });

  final String name;

  /// `studio` or `repo`.
  final String source;

  /// The project root a repository template lives in; empty for a Studio one.
  final String project;

  /// What is in it, in one line: its base, its toolchains, its network mode.
  final String contents;

  /// The published version; 0 when nothing is published.
  final int version;

  /// How many agents run on it.
  final int usage;

  /// Whether a draft differs from the published version.
  final bool draftChanges;

  /// The version and the use, in the design system's words.
  String get value => version == 0
      ? 'No published version · ${agents(usage)}'
      : 'v$version · ${agents(usage)}';

  static String agents(int count) => count == 1 ? '1 agent' : '$count agents';
}

/// Settings' Environments section (F7 decision 6): the gallery of workspace
/// templates, and the builds behind each.
///
/// The gallery is read here. What writes a template — the layers, the source,
/// Save draft, Build and Publish — is the designer (Task 5's model), so a save
/// in this section is only an import, which starts a draft the designer opens.
class EnvironmentsModel extends SectionModel<EnvironmentRow> {
  EnvironmentsModel(this._client);

  /// A model already holding its rows, for a test or the gallery.
  EnvironmentsModel.fixture({
    List<EnvListItem> items = const [],
    Map<String, EnvLoaded> loaded = const {},
  }) : _client = null {
    _items = items;
    _loaded = loaded;
    replaceItems(_rows());
  }

  final BridgeClient? _client;

  List<EnvListItem> _items = const [];
  Map<String, EnvLoaded> _loaded = const {};

  /// The source and parse a template last answered with, by name.
  EnvLoaded? loadedOf(String name) => _loaded[name];

  @override
  Future<List<EnvironmentRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    _items = await client.environments();
    final loaded = <String, EnvLoaded>{};
    for (final item in _items) {
      try {
        loaded[item.name] = await client.environment(item.name);
      } catch (_) {
        // A template whose source cannot be read yet still has a row: the
        // gallery lists what exists, not only what parsed.
      }
    }
    _loaded = loaded;
    return _rows();
  }

  @override
  Object keyOf(EnvironmentRow item) => item.name;

  @override
  bool isNew(EnvironmentRow item) => false;

  /// The list is read here: the designer writes a template, and an import is
  /// the section's own action rather than a save of a row.
  @override
  Future<EnvironmentRow> persist(EnvironmentRow item) async => item;

  /// Starts a template from a `devcontainer.json` and answers the record the
  /// bridge made, so the designer can open the draft it starts.
  Future<EnvMeta> importDevcontainer(String name, String path) async {
    final client = _client;
    if (name.trim().isEmpty) {
      throw const EnvironmentError('An imported environment needs a name');
    }
    if (path.trim().isEmpty) {
      throw const EnvironmentError(
        'A devcontainer import needs a path: owner/repo/.devcontainer/'
        'devcontainer.json',
      );
    }
    if (client == null) {
      throw const EnvironmentError('Nothing to import into');
    }
    final meta = await client.createEnvironment(
      name.trim(),
      'devcontainer:${path.trim()}',
    );
    await load();
    return meta;
  }

  /// The versions, the warm pool and the median start times of [name], as the
  /// builds view draws them.
  Future<EnvironmentBuilds> buildsOf(String name) async {
    final client = _client;
    final EnvBuilds builds;
    if (client == null) {
      final loaded = _loaded[name];
      builds = EnvBuilds(
        versions: loaded?.meta.versions ?? const [],
        pool: const EnvPool(size: 0, idle: 0, starting: 0),
        starts: const EnvStarts(coldMs: 0, warmMs: 0),
      );
    } else {
      builds = await client.environmentBuilds(name);
    }
    return EnvironmentBuilds(
      versions: [
        for (final version in builds.versions)
          EnvironmentVersion(
            n: version.n,
            detail: version.imageTag ?? version.sha256,
            state: version.buildStatus,
          ),
      ],
      poolSize: builds.pool.size,
      poolIdle: builds.pool.idle,
      poolStarting: builds.pool.starting,
      coldMs: builds.starts.coldMs,
      warmMs: builds.starts.warmMs,
      sizeBytes: builds.versions.isEmpty
          ? 0
          : builds.versions.last.sizeBytes ?? 0,
    );
  }

  /// Starts a build of [version], or of the published version when none is
  /// named, and answers which version it is building.
  Future<int> startBuild(String name, {int? version}) async {
    final client = _client;
    if (client == null) {
      throw const EnvironmentError('Nothing to build from here');
    }
    final started = await client.startEnvironmentBuild(name, version: version);
    return started.version;
  }

  /// A build's log, which the caller starts and disposes.
  BuildLog openBuildLog(String name, int n) =>
      BuildLog(_client?.environmentBuildLog(name, n));

  /// What a template's one-line contents say: its base, its toolchains, its
  /// network mode.
  String _contentsOf(EnvListItem item) {
    final doc = _loaded[item.name]?.doc;
    if (doc == null) {
      return item.source == 'repo'
          ? 'A repository template, from ${item.project}'
          : 'A Studio template';
    }
    final toolchains = doc.workspace.toolchains ?? const <String>[];
    return [
      if (doc.workspace.base.isNotEmpty) doc.workspace.base,
      if (toolchains.isNotEmpty) toolchains.join(', '),
      if ((doc.network.mode ?? '').isNotEmpty) 'network ${doc.network.mode}',
    ].join(' · ');
  }

  List<EnvironmentRow> _rows() => [
    for (final item in _items)
      EnvironmentRow(
        name: item.name,
        source: item.source,
        project: item.project ?? '',
        contents: _contentsOf(item),
        version: item.published ?? 0,
        usage: item.usage,
        draftChanges: item.draftChanges ?? false,
      ),
  ];
}
