import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import 'section_model.dart';

/// What one row of Models and routing sets (F7 decision 7).
enum ModelRowKind {
  /// A role and the preset it runs.
  role,

  /// A preset and its limits: read here.
  preset,

  /// A provider's key, which is written and never read back.
  key,

  /// A cap, in dollars.
  capAmount,

  /// What happens when a cap is reached.
  capBehaviour,
}

/// How a row is edited: by choosing a value, by typing an amount, by typing a
/// secret that is never shown again, or not at all.
enum ModelEditor { choices, amount, secret, readOnly }

/// A failure the person made, in words: a cap that is not a number.
@immutable
class ModelValueError implements Exception {
  const ModelValueError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// One row of Models and routing.
@immutable
class ModelRow {
  const ModelRow({
    required this.kind,
    required this.id,
    required this.subject,
    required this.name,
    required this.description,
    required this.value,
    this.editor = ModelEditor.readOnly,
    this.choices = const [],
    this.facts = const [],
    this.note,
  });

  final ModelRowKind kind;

  /// The row's own key.
  final String id;

  /// What a save writes about: the role, the provider, or the cap.
  final String subject;

  final String name;
  final String description;

  /// What it is set to now, or what was typed into it.
  final String value;

  final ModelEditor editor;

  /// For a row edited by choosing: the values to choose from, and what each
  /// means.
  final List<(String, String)> choices;

  /// The detail's named facts, for a row that is read.
  final List<(String, String)> facts;

  final String? note;

  /// What is chosen now, in the design system's words.
  String get shownValue {
    if (editor != ModelEditor.choices) return value;
    for (final (choice, about) in choices) {
      if (choice == value) return about;
    }
    return value;
  }

  /// This row with [value] chosen or typed, which is the edit a save writes.
  ModelRow withValue(String value) => ModelRow(
    kind: kind,
    id: id,
    subject: subject,
    name: name,
    description: description,
    value: value,
    editor: editor,
    choices: choices,
    facts: facts,
    note: note,
  );
}

/// Settings' Models and routing section (F7 decision 7): the role-to-model
/// map, the presets, the caps and what happens at them, and each provider's
/// key.
///
/// Everything a save writes goes to the user's global config: a project never
/// carries providers or presets, and the section says so. A provider's key
/// goes one way — it is written and never read back, so the row reports
/// whether one is stored and nothing else.
class ModelsModel extends SectionModel<ModelRow> {
  ModelsModel(this._client);

  /// A model already holding its rows, for a test or the gallery.
  ModelsModel.fixture(ModelsConfig config) : _client = null {
    _load(config);
    replaceItems(_rows());
  }

  final BridgeClient? _client;

  ModelsConfig? _config;
  final _assignments = <String, String>{};
  Budgets? _budgets;

  /// The providers a key was written for in this window. What a key is cannot
  /// be read back, so this is the only way the row knows one was stored.
  final _keysWritten = <String>{};

  @override
  Future<List<ModelRow>> fetch() async {
    final client = _client;
    if (client == null) return _rows();
    _load(await client.models());
    return _rows();
  }

  @override
  Object keyOf(ModelRow item) => item.id;

  @override
  bool isNew(ModelRow item) => false;

  /// The rows as the model holds them now, after a save: the caller reads the
  /// same row back with what the bridge stored.
  @override
  Future<ModelRow> persist(ModelRow item) async {
    final client = _client;
    switch (item.kind) {
      case ModelRowKind.role:
        if (client == null) {
          _assignments[item.subject] = item.value;
        } else {
          final profile = _config?.defaultProfile ?? 'default';
          await client.setRouting(
            RoutingUpdate(
              profiles: {
                profile: {item.subject: Binding(preset: item.value)},
              },
            ),
          );
          _assignments[item.subject] = item.value;
        }
      case ModelRowKind.key:
        if (client != null && item.value.isNotEmpty) {
          await client.setProviderKey(item.subject, item.value);
        }
        _keysWritten.add(item.subject);
      case ModelRowKind.capAmount:
        final amount = double.tryParse(item.value.trim());
        if (amount == null || amount < 0) {
          throw const ModelValueError('A cap is a number of dollars, or 0');
        }
        final next = switch (item.subject) {
          'daily' => _budgetCopyWith(dailyUSD: amount),
          _ => _budgetCopyWith(perAgentUSD: amount),
        };
        await _writeBudgets(next);
      case ModelRowKind.capBehaviour:
        final next = switch (item.subject) {
          'daily' => _budgetCopyWith(onDailyCap: item.value),
          _ => _budgetCopyWith(onAgentCap: item.value),
        };
        await _writeBudgets(next);
      case ModelRowKind.preset:
        // A preset's limits are read here; changing them is the engine's own
        // config editor, which this build does not carry.
        break;
    }
    replaceItems(_rows());
    // A key row never answers what was typed: the row reports that one is
    // stored, which is all any route says.
    return _rows().firstWhere((row) => row.id == item.id);
  }

  Budgets _budgetCopyWith({
    double? dailyUSD,
    double? perAgentUSD,
    String? onDailyCap,
    String? onAgentCap,
  }) {
    final held =
        _budgets ??
        (_config?.budgets ??
            const Budgets(
              dailyUSD: 0,
              perAgentUSD: 0,
              onDailyCap: 'warn',
              onAgentCap: 'warn',
            ));
    return Budgets(
      dailyUSD: dailyUSD ?? held.dailyUSD,
      perAgentUSD: perAgentUSD ?? held.perAgentUSD,
      onDailyCap: onDailyCap ?? held.onDailyCap,
      onAgentCap: onAgentCap ?? held.onAgentCap,
    );
  }

  /// Writes the caps and adopts the report the engine stored: it may clamp a
  /// value, and what it answered is what holds.
  Future<void> _writeBudgets(Budgets next) async {
    final client = _client;
    if (client == null) {
      _budgets = next;
      return;
    }
    final report = await client.saveBudgets(next);
    _budgets = report.budgets;
  }

  void _load(ModelsConfig config) {
    _config = config;
    _assignments.clear();
    final held = config.profiles[config.defaultProfile];
    for (final role in config.roles) {
      _assignments[role] = held?[role]?.preset ?? '';
    }
    _budgets = config.budgets;
  }

  /// A cap in dollars, as the row says it.
  static String _money(double value) => '\$${value.toStringAsFixed(2)}';

  List<ModelRow> _rows() {
    final config = _config;
    if (config == null) return const [];
    final presets = config.presets.keys.toList()..sort();
    final providers = config.providers.keys.toList()..sort();
    final budgets = _budgets ?? config.budgets;
    final rows = <ModelRow>[
      for (final role in config.roles)
        ModelRow(
          kind: ModelRowKind.role,
          id: 'role/$role',
          subject: role,
          name: role,
          description: 'The preset this role runs',
          value: _assignments[role] ?? '',
          editor: ModelEditor.choices,
          choices: [
            for (final preset in presets)
              (preset, _presetWords(config.presets[preset]!)),
          ],
          note: _assignments[role] == null || _assignments[role]!.isEmpty
              ? 'No preset: this role runs the active preset, '
                    '${config.activePreset.isEmpty ? 'none' : config.activePreset}.'
              : null,
        ),
      for (final name in presets)
        ModelRow(
          kind: ModelRowKind.preset,
          id: 'preset/$name',
          subject: name,
          name: name,
          description: _presetWords(config.presets[name]!),
          value: config.presets[name]!.model,
          facts: [
            ('Provider', config.presets[name]!.provider),
            ('Model', config.presets[name]!.model),
            if (config.presets[name]!.contextWindow != null)
              (
                'Context window',
                '${config.presets[name]!.contextWindow} tokens',
              ),
            if (config.presets[name]!.maxOutputTokens != null)
              ('Max output', '${config.presets[name]!.maxOutputTokens} tokens'),
            if (config.presets[name]!.toolCalling != null)
              ('Tool calling', config.presets[name]!.toolCalling!),
            if (config.presets[name]!.thinking != null)
              ('Thinking', config.presets[name]!.thinking!),
            if (config.presets[name]!.localOnly ?? false)
              ('Runs', 'Locally only'),
            if (config.presets[name]!.verificationGate == false)
              ('Gate', 'Off for this preset'),
          ],
        ),
      for (final name in providers)
        ModelRow(
          kind: ModelRowKind.key,
          id: 'key/$name',
          subject: name,
          name: '$name key',
          description: 'Write-only: a key is stored and never read back',
          value: _keyWords(name, config.providers[name]!),
          editor: ModelEditor.secret,
          note:
              'The bridge stores the key in the user config and nothing on '
              'any route answers it back. Writing a new one replaces it.',
        ),
      ModelRow(
        kind: ModelRowKind.capAmount,
        id: 'cap/daily',
        subject: 'daily',
        name: 'Daily cap',
        description: 'Dollars a day across every project',
        value: _money(budgets.dailyUSD),
        editor: ModelEditor.amount,
      ),
      ModelRow(
        kind: ModelRowKind.capAmount,
        id: 'cap/agent',
        subject: 'agent',
        name: 'Per-agent cap',
        description: 'Dollars one agent may spend',
        value: _money(budgets.perAgentUSD),
        editor: ModelEditor.amount,
      ),
      ModelRow(
        kind: ModelRowKind.capBehaviour,
        id: 'cap/on-daily',
        subject: 'daily',
        name: 'At the daily cap',
        description: 'What happens when the day’s spend reaches its cap',
        value: budgets.onDailyCap,
        editor: ModelEditor.choices,
        choices: _capChoices,
      ),
      ModelRow(
        kind: ModelRowKind.capBehaviour,
        id: 'cap/on-agent',
        subject: 'agent',
        name: 'At an agent’s cap',
        description: 'What happens when an agent reaches its own cap',
        value: budgets.onAgentCap,
        editor: ModelEditor.choices,
        choices: _capChoices,
      ),
    ];
    return rows;
  }

  /// The three answers to a cap, and what each does.
  static const _capChoices = <(String, String)>[
    ('warn', 'Warn and carry on'),
    ('block', 'Block new turns'),
    ('pause', 'Pause the agent'),
  ];

  static String _presetWords(Preset preset) =>
      '${preset.provider}/${preset.model}';

  String _keyWords(String name, ProviderView provider) {
    if (_keysWritten.contains(name)) return 'A key is stored';
    return switch (provider.keySource) {
      'config' => 'A key is stored',
      'env' => 'Read from ${provider.apiKeyEnv ?? 'the environment'}',
      _ => 'No key',
    };
  }
}
