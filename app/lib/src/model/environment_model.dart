import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'environments_model.dart'
    show BuildLog, EnvironmentBuilds, EnvironmentVersion;
import 'section_model.dart' show SaveState, UnsavedEditsSource;

/// The nine layers the designer names, in the order the design lists them
/// (F7 decision 6). Two of them share a table — a workspace's name, base and
/// toolchains all live in `[workspace]` — so a layer is a range of the source,
/// not always a table of its own.
enum LayerKind {
  base,
  toolchains,
  packages,
  mounts,
  files,
  secrets,
  network,
  resources,
  setup,
}

/// What a layer is called wherever it is named.
extension LayerKindName on LayerKind {
  String get label => switch (this) {
    LayerKind.base => 'Base image',
    LayerKind.toolchains => 'Toolchains',
    LayerKind.packages => 'Packages',
    LayerKind.mounts => 'Mounts',
    LayerKind.files => 'Shared files',
    LayerKind.secrets => 'Secrets',
    LayerKind.network => 'Network',
    LayerKind.resources => 'Resources',
    LayerKind.setup => 'Setup',
  };

  /// The table keys this layer is drawn from, as the workspace document names
  /// them.
  List<String> get keys => switch (this) {
    LayerKind.base || LayerKind.toolchains => const ['workspace'],
    LayerKind.packages => const ['packages'],
    LayerKind.mounts => const ['mounts'],
    LayerKind.files => const ['files'],
    LayerKind.secrets => const ['secretsEnv', 'inject', 'secrets'],
    LayerKind.network => const ['network'],
    LayerKind.resources => const ['resources'],
    LayerKind.setup => const ['setup'],
  };
}

/// One layer: its kind, a one-line summary and the 1-based inclusive lines of
/// the source it owns.
@immutable
class Layer {
  const Layer({
    required this.kind,
    required this.summary,
    required this.sourceStart,
    required this.sourceEnd,
  });

  final LayerKind kind;

  /// What is in it, in one line.
  final String summary;

  final int sourceStart;
  final int sourceEnd;
}

/// One parse diagnostic the bridge reported after a save.
@immutable
class LayerDiagnostic {
  const LayerDiagnostic({
    required this.line,
    required this.message,
    required this.severity,
  });

  final int line;
  final String message;

  /// error or warning.
  final String severity;
}

/// Settings' environment designer (F7 decision 6): layers on the left, the
/// source on the right, and one document behind both.
///
/// The source is the authority. Every edit — in the source view or in a layer
/// — rewrites [source], and the layers are re-read from it, so the two views
/// cannot disagree. A save hands the source to the bridge, which parses and
/// validates it: a diagnostic it reports is shown in place while the edit is
/// kept.
class EnvironmentModel extends ChangeNotifier implements UnsavedEditsSource {
  EnvironmentModel({
    required this.name,
    required String source,
    this.client,
    this.published = 0,
  }) : _saved = source,
       _source = source {
    _rescan();
  }

  /// A model holding a source and no bridge, for a test or the gallery.
  EnvironmentModel.fixture({
    required this.name,
    required String source,
    this.published = 0,
    List<LayerDiagnostic> diagnostics = const [],
    EnvironmentBuilds? builds,
  }) : client = null,
       _saved = source,
       _source = source,
       _diagnostics = List.unmodifiable(diagnostics) {
    _builds = builds;
    _rescan();
  }

  final String name;

  /// Where the template is read and written, when there is a bridge.
  final BridgeClient? client;

  /// The published version, or 0 when nothing is published.
  final int published;

  String _source;
  String _saved;
  List<Layer> _layers = const [];
  LayerKind? _selected;
  List<LayerDiagnostic> _diagnostics = const [];
  SaveState _saveState = SaveState.clean;
  String? _saveError;
  EnvironmentBuilds? _builds;
  int? _buildVersion;
  String? _buildStatus;
  BuildLog? _log;
  var _building = false;

  /// The document as it stands, source view and layers alike.
  String get source => _source;

  /// Every layer the source holds, in the design's order.
  List<Layer> get layers => _layers;

  /// The layer the person is looking at, or null.
  LayerKind? get selected => _selected;

  /// What the bridge said about the last save.
  List<LayerDiagnostic> get diagnostics => _diagnostics;

  /// Whether the source has an edit the bridge has not accepted.
  @override
  bool get hasUnsavedEdits => _source != _saved;

  SaveState get saveState => _saveState;

  /// Why the last save failed, in the bridge's own words.
  String? get saveError => _saveError;

  /// The builds the panel shows, or null before they were read.
  EnvironmentBuilds? get builds => _builds;

  /// The version being built or last built, and its state.
  int? get buildVersion => _buildVersion;
  String? get buildStatus => _buildStatus;
  bool get building => _building;

  /// A build may be published once the newest build passed: the gate runs
  /// what a passing build made, so there is nothing to publish before one.
  bool get canPublish {
    final builds = _builds;
    if (builds == null || builds.versions.isEmpty) return false;
    final latest = builds.versions.reduce((a, b) => a.n >= b.n ? a : b);
    return latest.state == 'ok';
  }

  /// The line of the source [kind] starts on, and the one it ends on.
  Layer? layerOf(LayerKind kind) {
    for (final layer in _layers) {
      if (layer.kind == kind) return layer;
    }
    return null;
  }

  /// The layer's own text: its lines, and no others.
  String textOf(LayerKind kind) {
    final layer = layerOf(kind);
    if (layer == null) return '';
    final lines = _source.split('\n');
    final start = (layer.sourceStart - 1).clamp(0, lines.length);
    final end = layer.sourceEnd.clamp(start, lines.length);
    return lines.sublist(start, end).join('\n');
  }

  /// The layer's range as character offsets, which is what the source view
  /// selects when a layer is chosen.
  (int, int) charRangeOf(LayerKind kind) {
    final layer = layerOf(kind);
    if (layer == null) return (0, 0);
    final lines = _source.split('\n');
    var offset = 0;
    var start = 0;
    var end = 0;
    for (var i = 0; i < lines.length; i++) {
      final lineStart = offset;
      offset += lines[i].length + 1;
      if (i + 1 == layer.sourceStart) start = lineStart;
      if (i + 1 == layer.sourceEnd) end = lineStart + lines[i].length;
    }
    return (start, end);
  }

  /// Chooses a layer; the source view then selects its range.
  void select(LayerKind? kind) {
    if (kind == _selected) return;
    _selected = kind;
    notifyListeners();
  }

  /// Replaces the whole document, as the source view does on every keystroke.
  void editSource(String next) {
    if (next == _source) return;
    _source = next;
    _saveState = SaveState.dirty;
    _saveError = null;
    _rescan();
    notifyListeners();
  }

  /// Replaces one layer's lines and nothing else: the layers before and after
  /// it keep their text to the character.
  void editLayer(LayerKind kind, String next) {
    final layer = layerOf(kind);
    if (layer == null) return;
    final lines = _source.split('\n');
    final start = (layer.sourceStart - 1).clamp(0, lines.length);
    final end = layer.sourceEnd.clamp(start, lines.length);
    final replacement = next.split('\n');
    // A trailing blank line would grow the document on every save; the layer
    // keeps the blank line that separated it from the next one.
    if (replacement.length > 1 && replacement.last.isEmpty) {
      replacement.removeLast();
    }
    final nextLines = [
      ...lines.sublist(0, start),
      ...replacement,
      ...lines.sublist(end),
    ];
    final joined = nextLines.join('\n');
    if (joined == _source) return;
    _source = joined;
    _saveState = SaveState.dirty;
    _saveError = null;
    _rescan();
    notifyListeners();
  }

  /// Throws the edit away, as the shell's guard offers.
  @override
  void discardEdits() {
    if (_source == _saved) return;
    _source = _saved;
    _saveState = SaveState.clean;
    _saveError = null;
    _rescan();
    notifyListeners();
  }

  /// Reads the template from the bridge, keeping nothing of the old draft.
  Future<void> load() async {
    final client = this.client;
    if (client == null) return;
    final loaded = await client.environment(name);
    _adopt(loaded);
    notifyListeners();
  }

  /// Writes the source as the draft. The bridge parses it, and what it says —
  /// the diagnostics and the layers it found — replaces what this model read
  /// for itself.
  Future<void> saveDraft() async {
    final client = this.client;
    if (client == null) {
      _saved = _source;
      _saveState = SaveState.clean;
      _diagnostics = const [];
      notifyListeners();
      return;
    }
    _saveState = SaveState.saving;
    _saveError = null;
    notifyListeners();
    try {
      final loaded = await client.saveEnvironmentDraft(name, _source);
      _adopt(loaded);
      _saveState = SaveState.clean;
    } catch (error) {
      _saveState = SaveState.failed;
      _saveError = error.toString();
    }
    notifyListeners();
  }

  /// Starts a build and answers its version. Its log is [openBuildLog].
  Future<int> build() async {
    final client = this.client;
    if (client == null) return _buildVersion ?? published;
    _building = true;
    notifyListeners();
    try {
      final started = await client.startEnvironmentBuild(name);
      _buildVersion = started.version;
      _buildStatus = 'building';
      await refreshBuilds();
      return started.version;
    } finally {
      _building = false;
      notifyListeners();
    }
  }

  /// Publishes the draft, which only a passing build allows.
  Future<void> publish() async {
    final client = this.client;
    if (client == null || !canPublish) return;
    await client.publishEnvironment(name);
    await refreshBuilds();
    notifyListeners();
  }

  /// Reads the builds again: the versions, the pool and the start times.
  Future<void> refreshBuilds() async {
    final client = this.client;
    if (client == null) return;
    final builds = await _buildsOf(client);
    _builds = builds;
    notifyListeners();
  }

  /// A build's log, which the caller starts and disposes.
  BuildLog openBuildLog(int n) {
    _log?.dispose();
    final log = BuildLog(client?.environmentBuildLog(name, n))..start();
    _log = log;
    return log;
  }

  Future<EnvironmentBuilds> _buildsOf(BridgeClient client) async {
    final builds = await client.environmentBuilds(name);
    return EnvironmentBuilds(
      versions: [
        for (final version in builds.versions)
          EnvironmentVersionOf(version).value,
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

  /// The side panel's facts: what it costs, how fast it starts, and what the
  /// gate runs in it.
  List<(String, String)> get panelFacts {
    final builds = _builds;
    return [
      (
        'Size',
        builds == null || builds.sizeBytes == 0
            ? 'Not built yet'
            : '${(builds.sizeBytes / (1024 * 1024)).round()} MB',
      ),
      if (builds != null)
        (
          'Starts',
          builds.coldMs == 0 && builds.warmMs == 0
              ? 'Not timed yet'
              : 'cold ${_seconds(builds.coldMs)} · warm ${_seconds(builds.warmMs)}',
        ),
      if (builds != null)
        (
          'Warm pool',
          '${builds.poolSize} · ${builds.poolIdle} idle · '
              '${builds.poolStarting} starting',
        ),
      ('The gate runs', _gateWords()),
    ];
  }

  /// What the gate can run: the setup command, the policy mode and the
  /// network mode the source declares.
  String _gateWords() {
    final policy = _valueOf('policy', 'mode');
    final network = _valueOf('network', 'mode');
    final setup = _valueOf('setup', 'run');
    final parts = [
      if (setup.isNotEmpty) '`$setup`',
      policy.isEmpty ? 'the environment’s default policy' : policy,
      network.isEmpty ? 'the environment’s network' : 'network $network',
    ];
    return parts.join(' · ');
  }

  /// The value of [key] in table [table], as the source writes it.
  String _valueOf(String table, String key) {
    final lines = _source.split('\n');
    var inTable = false;
    for (final line in lines) {
      final trimmed = line.trim();
      if (trimmed.startsWith('[')) {
        inTable = trimmed.replaceAll(RegExp(r'[\[\]\s]'), '') == table;
        continue;
      }
      if (!inTable) continue;
      final match = RegExp('^${RegExp.escape(key)}\\s*=\\s*(.+)\$')
          .firstMatch(trimmed);
      if (match != null) {
        return match.group(1)!.replaceAll('"', '').trim();
      }
    }
    return '';
  }

  void _adopt(EnvLoaded loaded) {
    _source = loaded.source;
    _saved = loaded.source;
    _diagnostics = [
      for (final diagnostic in loaded.diagnostics)
        LayerDiagnostic(
          line: diagnostic.line,
          message: diagnostic.message,
          severity: diagnostic.severity,
        ),
    ];
    _rescan();
  }

  /// Reads the layers out of the source: every table it declares, with the
  /// lines it owns. One scan, so a keystroke in either view cannot leave the
  /// two disagreeing.
  void _rescan() {
    final lines = _source.split('\n');
    final header = RegExp(r'^\s*\[\[?([A-Za-z0-9_.-]+)\]\]?\s*(#.*)?$');
    final found = <String, int>{}; // table name to its 1-based start line
    for (var i = 0; i < lines.length; i++) {
      final match = header.firstMatch(lines[i]);
      if (match == null) continue;
      found.putIfAbsent(match.group(1)!, () => i + 1);
    }
    final layers = <Layer>[];
    for (final kind in LayerKind.values) {
      for (final key in kind.keys) {
        final start = found[key];
        if (start == null) continue;
        if (layers.any((layer) => layer.kind == kind)) continue;
        final end = _endOf(key, found, start, lines.length);
        layers.add(
          Layer(
            kind: kind,
            summary: _summary(lines, start, end),
            sourceStart: start,
            sourceEnd: end,
          ),
        );
      }
    }
    layers.sort((a, b) => a.sourceStart.compareTo(b.sourceStart));
    _layers = List.unmodifiable(layers);
    if (_selected != null && layerOf(_selected!) == null) _selected = null;
  }

  /// The last line a table owns: the one before the next table, with the blank
  /// lines before that one left out.
  int _endOf(String key, Map<String, int> found, int start, int length) {
    var end = length;
    for (final other in found.values) {
      if (other > start && other - 1 < end) end = other - 1;
    }
    final lines = _source.split('\n');
    while (end > start && lines[end - 1].trim().isEmpty) {
      end--;
    }
    return end;
  }

  /// One line saying what the table holds: its first key, or that it is empty.
  String _summary(List<String> lines, int start, int end) {
    for (var i = start; i < end; i++) {
      final trimmed = lines[i].trim();
      if (trimmed.isEmpty || trimmed.startsWith('#')) continue;
      return trimmed.length <= 60 ? trimmed : '${trimmed.substring(0, 57)}…';
    }
    return 'Empty';
  }

  static String _seconds(int ms) =>
      ms < 1000 ? '${ms}ms' : '${(ms / 1000).round()}s';

  @override
  void dispose() {
    _log?.dispose();
    super.dispose();
  }
}

/// One version, as the builds a panel shows them.
extension EnvironmentVersionOf on EnvVersion {
  EnvironmentVersion get value =>
      EnvironmentVersion(n: n, detail: imageTag ?? sha256, state: buildStatus);
}
