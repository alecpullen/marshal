import 'dart:async';

import '../../model/age.dart';
import '../../model/project_model.dart';
import '../kit/kit.dart';

/// A project's health (F6 decision 12): whether its verify gate can run, how
/// fresh its mirrors are, its retained workspaces, its trust and its workspace,
/// and the discovery report the person asks for.
///
/// Every check is a fact the bridge reported, in words: the report says what it
/// cannot read rather than drawing it as passing. Discovery never writes
/// anything, so it is a plain action on this tab and nothing else changes when
/// it runs (design-system.md §7).
class HealthTab extends StatelessWidget {
  const HealthTab({super.key, required this.model, this.state, this.now});

  final ProjectModel model;

  /// The tab's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  @override
  Widget build(BuildContext context) {
    final state = this.state;
    if (state != null && state != StateKind.stale) {
      return _stateMessage(state);
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(age: relativeAge(model.lastRead, _now)),
          const SizedBox(height: Space.s16),
        ],
        Label('Health'),
        const SizedBox(height: Space.s8),
        _health(context),
        const SizedBox(height: Space.s20),
        Label('Discovery'),
        const SizedBox(height: Space.s8),
        _discoveryHeader(context),
        if (model.discoveryError != null) ...[
          const SizedBox(height: Space.s8),
          Text(
            model.discoveryError!,
            style: Ember.of(context).small.tint(Ember.of(context).err),
          ),
        ],
        if (model.discovery != null) ...[
          const SizedBox(height: Space.s16),
          _report(context, model.discovery!),
        ],
      ],
    );
  }

  DateTime get _now => now ?? DateTime.now();

  /// The gate, the mirrors, the retained workspaces, the trust and the
  /// workspace, each a row of the kit's settings list.
  Widget _health(BuildContext context) {
    final health = model.health;
    if (health == null) {
      if (model.healthLoading) return const StateMessage.loading(lines: 4);
      final error = model.healthError;
      if (error != null) {
        return StateMessage.error(
          message: error,
          retry: StateAction('Read the health again', onTap: _loadHealth),
        );
      }
      return StateMessage.empty(
        message: 'The health has not been read yet.',
        action: StateAction('Read the health', onTap: _loadHealth),
      );
    }
    final gate = switch (health.gateRunnable) {
      'yes' => 'Runs on this project',
      'no' => 'The last verify was skipped: nothing to run',
      _ => 'No agent has verified yet',
    };
    final commands = [
      if (health.verifyBuild.trim().isNotEmpty)
        'build: ${health.verifyBuild.trim()}',
      if (health.verifyTest.trim().isNotEmpty)
        'test: ${health.verifyTest.trim()}',
    ];
    final workspace = health.workspace;
    return Rows([
      RowItem(
        name: 'Verify gate',
        description: commands.isEmpty
            ? 'What runs before a change is ready'
            : commands.join(' · '),
        value: gate,
      ),
      for (final mirror in health.mirrors)
        RowItem(
          name: 'Mirror ${mirror.repoID}',
          description: mirror.present
              ? 'Fetched ${_age(mirror.ageSeconds)} ago'
              : 'The repository has no mirror yet',
          value: mirror.present
              ? (mirror.head.isEmpty ? 'Present' : mirror.head)
              : 'Missing',
          mono: mirror.head.isNotEmpty,
        ),
      RowItem(
        name: 'Retained workspaces',
        description: health.orphans.isEmpty
            ? 'Every workspace belongs to an agent'
            : health.orphans.join(' · '),
        value: health.orphans.isEmpty ? 'None' : '${health.orphans.length}',
      ),
      RowItem(
        name: 'Folder trust',
        description: 'Whether this project may run its own config',
        value: _trustWord(health.trust),
      ),
      if (workspace != null)
        RowItem(
          name: 'Default workspace',
          description: _workspaceWords(workspace),
          value: workspace.resolves
              ? (workspace.built ? 'Ready' : 'Not built')
              : 'Does not resolve',
        ),
      RowItem(
        name: 'Forge connection',
        description: model.discovery == null
            ? 'Run discovery to check it'
            : 'The forge ${model.discovery!.forge} answered',
        value: model.discovery == null
            ? 'Not checked'
            : _statusWord(model.discovery!.status),
      ),
    ]);
  }

  /// The discovery action and what it needs: a repository to probe.
  Widget _discoveryHeader(BuildContext context) {
    final e = Ember.of(context);
    if (model.repoID.isEmpty) {
      return Text(
        'Discovery needs a repository: wire one in the project\'s intake '
        'before it can check the forge.',
        style: e.small.tint(e.muted),
      );
    }
    if (model.discovering) {
      return Row(
        children: [
          Btn('Running discovery…'),
          const SizedBox(width: Space.s8),
          Flexible(
            child: Text(
              'Reading the checkout and the forge. Nothing is changed.',
              style: e.small.tint(e.muted),
            ),
          ),
        ],
      );
    }
    return Align(
      alignment: Alignment.centerLeft,
      child: Btn(
        model.discovery == null ? 'Run discovery' : 'Run discovery again',
        icon: Glyphs.arrow_clockwise,
        onTap: () => unawaited(model.runDiscovery()),
      ),
    );
  }

  /// The report: what was found, what is recommended and what is missing.
  Widget _report(BuildContext context, DiscoveryView report) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Label('Discovery report', count: report.capabilities.length),
        const SizedBox(height: Space.s8),
        Rows([
          RowItem(
            name: 'Status',
            description: report.error.isEmpty
                ? 'What the probe found'
                : report.error,
            value: _statusWord(report.status),
          ),
          RowItem(
            name: 'Checkout',
            description: report.branch.isEmpty
                ? 'No branch was read'
                : 'On ${report.branch}',
            value: report.revision.isEmpty
                ? 'Unknown'
                : _short(report.revision),
            mono: report.revision.isNotEmpty,
          ),
          if (report.worktreePath.isNotEmpty)
            RowItem(
              name: 'Linked checkout',
              description: report.worktreePath,
              value: report.worktreeDirty ? 'Dirty' : 'Clean',
              mono: true,
            ),
          for (final row in report.capabilities)
            RowItem(
              name: row.name,
              description: row.description,
              value: row.value,
            ),
        ]),
        if (report.recommended.isNotEmpty) ...[
          const SizedBox(height: Space.s16),
          Label('Recommended', count: report.recommended.length),
          const SizedBox(height: Space.s8),
          Rows([
            for (final row in report.recommended)
              RowItem(
                name: row.name,
                description: row.description,
                value: row.value,
                mono: true,
              ),
          ]),
        ],
        if (report.facts.isNotEmpty) ...[
          const SizedBox(height: Space.s16),
          Label('Found', count: report.facts.length),
          const SizedBox(height: Space.s8),
          Rows([
            for (final row in report.facts)
              RowItem(
                name: row.name,
                description: row.description,
                value: row.value,
              ),
          ]),
        ],
        if (report.gaps.isNotEmpty) ...[
          const SizedBox(height: Space.s16),
          Label('Gaps', count: report.gaps.length),
          const SizedBox(height: Space.s8),
          Rows([
            for (final row in report.gaps)
              RowItem(
                name: row.name,
                description: row.description,
                value: row.value,
              ),
          ]),
        ],
      ],
    );
  }

  void _loadHealth() => unawaited(model.loadHealth());

  static String _statusWord(String status) =>
      status.trim().isEmpty ? 'Unknown' : status.trim();

  static String _trustWord(String trust) => switch (trust) {
    'trusted' => 'Trusted',
    'untrusted' => 'Not trusted',
    'na' => 'Not applicable',
    _ => trust,
  };

  static String _workspaceWords(ProjectWorkspaceHealth workspace) {
    if (!workspace.resolves) {
      return workspace.error.isEmpty ? 'It does not resolve' : workspace.error;
    }
    return workspace.built
        ? '${workspace.ref} resolves and is built'
        : '${workspace.ref} resolves, not built yet';
  }

  /// A digest short enough for a row.
  static String _short(String hash) =>
      hash.length <= 8 ? hash : hash.substring(0, 8);

  /// An age from seconds, as the design system writes durations.
  static String _age(int seconds) {
    if (seconds < 90) return '${seconds}s';
    if (seconds < 5400) return '${(seconds / 60).round()}m';
    if (seconds < 172800) return '${(seconds / 3600).round()}h';
    return '${(seconds / 86400).round()}d';
  }

  Widget _stateMessage(StateKind state) {
    final retry = StateAction('Read the health again', onTap: _loadHealth);
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message: 'Nothing has been read about this project yet.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: model.healthError ?? 'The health could not be read.',
        retry: retry,
      ),
      StateKind.unavailable => StateMessage.unavailable(
        host: model.name,
        lastSeen: relativeAge(model.lastRead, _now),
        action: retry,
      ),
      StateKind.stale => StateMessage.stale(
        age: relativeAge(model.lastRead, _now),
      ),
      StateKind.planned => StateMessage.planned(
        name: 'Project health',
        note: 'Not built yet',
      ),
    };
  }
}
