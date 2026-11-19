import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'environments_model.dart'
    show BuildLog, EnvironmentBuilds, EnvironmentVersion;
import 'fleet_store.dart' show describeFailure;
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

/// One layer: its kind, a one-line summary and the ranges of source it owns.
///
/// A layer is a table, and TOML declares a table more than once — every
/// `[[mounts]]` block is the same table — so a layer owns one range per block,
/// in the document's order. A table declared once, which is most of them, is
/// one range.
@immutable
class Layer {
  const Layer({
    required this.kind,
    required this.summary,
    required this.ranges,
  });

  final LayerKind kind;

  /// What is in it, in one line.
  final String summary;

  /// The 1-based inclusive lines of each of the table's blocks, in order.
  final List<(int, int)> ranges;

  /// The layer's first and last line, for a view that can name only one span.
  int get sourceStart => ranges.first.$1;
  int get sourceEnd => ranges.last.$2;

  /// The lines the layer owns, as a row writes them: `5–6, 10–11`.
  String get rangeLabel => ranges.map((r) => '${r.$1}–${r.$2}').join(', ');
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

  /// The saved draft the newest build was made from, as far as this window
  /// knows: a build reads the draft, so a draft written since has never been
  /// built. Null until the builds have been read once.
  String? _builtDraft;

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

  /// A build may be published once the newest build passed, and only while the
  /// draft is still the one that build read: the gate runs what a passing
  /// build made, so a draft edited or saved since has never been built, and
  /// the bridge publishes it without looking.
  bool get canPublish {
    final builds = _builds;
    if (builds == null || builds.versions.isEmpty) return false;
    final latest = builds.versions.reduce((a, b) => a.n >= b.n ? a : b);
    if (latest.state != 'ok') return false;
    if (hasUnsavedEdits) return false;
    final built = _builtDraft;
    return built == null || built == _saved;
  }

  /// The line of the source [kind] starts on, and the one it ends on.
  Layer? layerOf(LayerKind kind) {
    for (final layer in _layers) {
      if (layer.kind == kind) return layer;
    }
    return null;
  }

  /// The layer's own text: its lines, and no others. A table declared more
  /// than once reads as its blocks in the document's order, which is the order
  /// a person would write them back in.
  String textOf(LayerKind kind) {
    final layer = layerOf(kind);
    if (layer == null) return '';
    final lines = _source.split('\n');
    return [
      for (final (start, end) in layer.ranges)
        ...lines.sublist(
          (start - 1).clamp(0, lines.length),
          end.clamp(0, lines.length),
        ),
    ].join('\n');
  }

  /// The layer's range as character offsets, which is what the source view
  /// selects when a layer is chosen: a selection cannot be disjoint, so a
  /// table declared in several places selects from its first line to its last.
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
  /// it keep their text to the character. A table declared more than once is
  /// written back at its first block — the text the person edited is the text
  /// the document holds afterwards — and its other blocks go with it.
  void editLayer(LayerKind kind, String next) {
    final layer = layerOf(kind);
    if (layer == null) return;
    final lines = _source.split('\n');
    final replacement = next.split('\n');
    // A trailing blank line would grow the document on every save; the layer
    // keeps the blank line that separated it from the next one.
    if (replacement.length > 1 && replacement.last.isEmpty) {
      replacement.removeLast();
    }
    final nextLines = <String>[];
    var cursor = 0;
    for (var i = 0; i < layer.ranges.length; i++) {
      final (start, end) = layer.ranges[i];
      final from = (start - 1).clamp(0, lines.length);
      final to = end.clamp(from, lines.length);
      nextLines.addAll(lines.sublist(cursor, from));
      cursor = to;
      if (i == 0) {
        nextLines.addAll(replacement);
        continue;
      }
      // A block that went leaves one blank line where it stood, not two.
      if (nextLines.isNotEmpty && nextLines.last.trim().isEmpty) {
        while (cursor < lines.length && lines[cursor].trim().isEmpty) {
          cursor++;
        }
      }
    }
    nextLines.addAll(lines.sublist(cursor));
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
      _saveError = describeFailure(error);
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
      // A build reads the saved draft, so that is what it may later publish.
      _builtDraft = _saved;
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
    // The builds that are already there were made from the draft as it stands,
    // unless a build this window started has already said which draft it read.
    _builtDraft ??= _saved;
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
  ///
  /// A table can be declared more than once — `[[mounts]]` twice is still one
  /// table — so the scan keeps every block of a name, not just the first, and
  /// a layer owns all of them.
  void _rescan() {
    final lines = _source.split('\n');
    final header = RegExp(r'^\s*\[\[?([A-Za-z0-9_.-]+)\]\]?\s*(#.*)?$');
    final starts = <int>[]; // every header's 1-based line, ascending
    final blocks = <String, List<int>>{}; // table name to its starts
    for (var i = 0; i < lines.length; i++) {
      final match = header.firstMatch(lines[i]);
      if (match == null) continue;
      starts.add(i + 1);
      blocks.putIfAbsent(match.group(1)!, () => []).add(i + 1);
    }
    final layers = <Layer>[];
    for (final kind in LayerKind.values) {
      for (final key in kind.keys) {
        final own = blocks[key];
        if (own == null) continue;
        if (layers.any((layer) => layer.kind == kind)) continue;
        final ranges = [
          for (final start in own) (start, _endOf(start, starts, lines)),
        ];
        layers.add(
          Layer(
            kind: kind,
            summary: _summary(lines, ranges.first.$1, ranges.first.$2),
            ranges: List.unmodifiable(ranges),
          ),
        );
      }
    }
    layers.sort((a, b) => a.sourceStart.compareTo(b.sourceStart));
    _layers = List.unmodifiable(layers);
    if (_selected != null && layerOf(_selected!) == null) _selected = null;
  }

  /// The last line one block of a table owns: the one before the next table's
  /// header, with the blank lines before that one left out.
  int _endOf(int start, List<int> starts, List<String> lines) {
    var end = lines.length;
    for (final other in starts) {
      if (other <= start) continue;
      end = other - 1;
      break;
    }
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
