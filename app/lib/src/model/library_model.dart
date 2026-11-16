import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

// A library's own types reach their screens through the model, so `ui/` reads
// `model/` and never `client/` (test/boundary_test.dart).
export '../client/contract.g.dart'
    show
        MemoryEntry,
        MemorySuggestion,
        PluginContents,
        PluginEntry,
        PluginScan,
        SkillEntry,
        SkillPreview;

/// The Skills tab of Library (F7 decision 5): the installed skills of one
/// scope, and the preview an install goes through before it is confirmed.
///
/// Nothing is installed from this section without a preview: the source is
/// staged, what it would install is shown, and only then is it confirmed or
/// discarded.
class SkillsModel extends SectionModel<SkillEntry> {
  SkillsModel(this._bridge, {required this.scope, this.project})
    : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  SkillsModel.fixture(
    List<SkillEntry> rows, {
    required this.scope,
    this.project,
  }) : _bridge = null,
       _fixture = rows;

  final BridgeClient? _bridge;
  final List<SkillEntry>? _fixture;

  /// `global` or `project`: the scope these skills belong to.
  final String scope;

  /// The project a project-scoped skill belongs to.
  final String? project;

  @override
  Future<List<SkillEntry>> fetch() async =>
      _fixture ?? await _requireBridge().skills(scope: scope, project: project);

  @override
  Object keyOf(SkillEntry skill) => '${skill.scope}|${skill.name}';

  @override
  bool isNew(SkillEntry skill) => false;

  @override
  Future<SkillEntry> persist(SkillEntry skill) =>
      throw StateError('a skill is installed through its preview');

  /// The row a "Add a skill" action opens: an empty one, whose detail is the
  /// install form.
  static SkillEntry blank() =>
      const SkillEntry(name: '', description: '', risk: '', scope: '');

  /// POST /api/library/skills/preview: stages [source] so a person can see
  /// what it would install.
  Future<SkillPreview> preview(String source) => _requireBridge().previewSkill(
    SkillStage(source: source, scope: scope, project: project ?? ''),
  );

  /// POST /api/library/skills/confirm: installs the previewed skill in the
  /// scope it was staged on.
  Future<void> confirm(SkillPreview preview) async {
    await _requireBridge().confirmSkill(
      SkillConfirm(
        stagingToken: preview.stagingToken,
        scope: scope,
        project: project ?? '',
      ),
    );
    store(
      SkillEntry(
        name: preview.name,
        description: preview.description,
        risk: preview.risk,
        scope: scope,
      ),
    );
  }

  /// POST /api/library/skills/discard: drops a previewed skill.
  Future<void> discard(SkillPreview preview) =>
      _requireBridge().discardSkill(preview.stagingToken);

  /// DELETE /api/library/skills/{name}: removes an installed skill.
  Future<void> remove(SkillEntry skill) async {
    await _requireBridge().removeSkill(
      skill.name,
      scope: scope,
      project: project,
    );
    removed(skill);
  }

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}

/// The Plugins tab of Library (F7 decision 5): the installed plugins of one
/// scope, and the scan an install goes through, which says what the plugin
/// ships before it is confirmed.
class PluginsModel extends SectionModel<PluginEntry> {
  PluginsModel(this._bridge, {required this.scope, this.project})
    : _fixture = null;

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  PluginsModel.fixture(
    List<PluginEntry> rows, {
    required this.scope,
    this.project,
  }) : _bridge = null,
       _fixture = rows;

  final BridgeClient? _bridge;
  final List<PluginEntry>? _fixture;

  final String scope;
  final String? project;

  @override
  Future<List<PluginEntry>> fetch() async =>
      _fixture ??
      await _requireBridge().plugins(scope: scope, project: project);

  @override
  Object keyOf(PluginEntry plugin) => '${plugin.scope}|${plugin.name}';

  @override
  bool isNew(PluginEntry plugin) => false;

  @override
  Future<PluginEntry> persist(PluginEntry plugin) =>
      throw StateError('a plugin is installed through its scan');

  /// The row a "Scan a plugin" action opens.
  static PluginEntry blank() => const PluginEntry(
    name: '',
    source: '',
    commit: '',
    contentHash: '',
    installedAt: '',
    scope: '',
  );

  /// POST /api/library/plugins/scan: scans [source] at [ref], if one is
  /// given, and answers what it ships.
  Future<PluginScan> scan(String source, {String ref = ''}) =>
      _requireBridge().scanPlugin(
        PluginStage(
          source: source,
          ref: ref,
          scope: scope,
          project: project ?? '',
        ),
      );

  /// POST /api/library/plugins/confirm: installs the scanned plugin.
  Future<void> confirm(PluginScan scan) async {
    await _requireBridge().confirmPlugin(
      PluginConfirm(
        scanToken: scan.scanToken,
        scope: scope,
        project: project ?? '',
      ),
    );
    store(
      PluginEntry(
        name: scan.name,
        source: scan.source,
        ref: scan.ref,
        commit: scan.commit,
        contentHash: '',
        installedAt: '',
        scope: scope,
      ),
    );
  }

  /// POST /api/library/plugins/discard: drops a scanned plugin.
  Future<void> discard(PluginScan scan) =>
      _requireBridge().discardPlugin(scan.scanToken);

  /// DELETE /api/library/plugins/{name}: removes an installed plugin.
  Future<void> remove(PluginEntry plugin) async {
    await _requireBridge().removePlugin(
      plugin.name,
      scope: scope,
      project: project,
    );
    removed(plugin);
  }

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}

/// The Memory tab of Library (F7 decision 5): the project's memories with
/// their scope, provenance and confidence, and the suggestions to promote one
/// to a wider scope.
///
/// A promotion is an action the person takes, never something the section
/// does on its own.
class MemoryModel extends SectionModel<MemoryEntry> {
  MemoryModel(
    this._bridge, {
    required this.project,
    List<MemorySuggestion> suggestions = const [],
  }) : _fixture = null,
       _suggestions = List.unmodifiable(suggestions);

  /// A section with rows and no bridge, for the gallery and the screens the
  /// enforcement tests pump.
  MemoryModel.fixture(
    List<MemoryEntry> rows, {
    required this.project,
    List<MemorySuggestion> suggestions = const [],
  }) : _bridge = null,
       _fixture = rows,
       _suggestions = List.unmodifiable(suggestions);

  final BridgeClient? _bridge;
  final List<MemoryEntry>? _fixture;

  /// The project whose memory this is.
  final String project;

  List<MemorySuggestion> _suggestions;

  /// The memories another project holds too, which a person may promote.
  List<MemorySuggestion> get suggestions => _suggestions;

  @override
  Future<List<MemoryEntry>> fetch() async {
    final fixed = _fixture;
    if (fixed != null) return fixed;
    final bridge = _requireBridge();
    _suggestions = List.unmodifiable(
      await bridge.memorySuggestions(project: project),
    );
    return bridge.memory(project: project);
  }

  @override
  Object keyOf(MemoryEntry entry) => entry.id;

  @override
  bool isNew(MemoryEntry entry) => false;

  @override
  Future<MemoryEntry> persist(MemoryEntry entry) =>
      throw StateError('a memory is edited through its own actions');

  /// POST /api/library/memory/{id}/promote: widens a memory's scope to the
  /// one the suggestion named.
  Future<void> promote(MemorySuggestion suggestion) async {
    await _requireBridge().promoteMemory(
      suggestion.memoryID,
      MemoryPromote(scope: suggestion.suggestedScope),
      project: project,
    );
    _suggestions = List.unmodifiable([
      for (final held in _suggestions)
        if (held.memoryID != suggestion.memoryID) held,
    ]);
    notifyListeners();
    await load();
  }

  /// POST /api/library/memory/{id}/confidence: records whether the memory
  /// still holds.
  Future<void> setConfidence(MemoryEntry entry, String confidence) async {
    await _requireBridge().setMemoryConfidence(
      entry.id,
      MemoryConfidence(confidence: confidence),
      project: project,
    );
    store(
      MemoryEntry(
        id: entry.id,
        kind: entry.kind,
        content: entry.content,
        confidence: confidence,
        sourceSessionID: entry.sourceSessionID,
        createdAt: entry.createdAt,
        updatedAt: entry.updatedAt,
        scope: entry.scope,
        scopeKey: entry.scopeKey,
        learnedProjectRoot: entry.learnedProjectRoot,
        learnedAgent: entry.learnedAgent,
        learnedStep: entry.learnedStep,
        confirmedBy: entry.confirmedBy,
      ),
    );
  }

  /// DELETE /api/library/memory/{id}.
  Future<void> delete(MemoryEntry entry) async {
    await _requireBridge().deleteMemory(entry.id, project: project);
    removed(entry);
  }

  BridgeClient _requireBridge() {
    final bridge = _bridge;
    if (bridge == null) {
      throw StateError('a fixture section has no bridge');
    }
    return bridge;
  }
}
