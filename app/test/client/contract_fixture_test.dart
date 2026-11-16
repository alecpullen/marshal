import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';

/// One fixture type's round trip: the value decoded from the fixture's JSON,
/// encoded back to the wire form.
typedef _Codec = Object? Function(Object? json);

/// The shared fixtures, one per contract source file. The Go half of this
/// test is `TestContractFixturesRoundTrip` in web/bridge/contract.
const _fixtures = [
  'agents',
  'answers',
  'budgets',
  'finish',
  'inbox',
  'intake',
  'projects',
  'recipes',
  'schedules',
  'spawn',
  'stream',
];

/// The codec for every type a fixture names. An enum type is held to its wire
/// text, a struct to `fromJson` and `toJson`.
final _codecs = <String, _Codec>{
  // agents.json
  'AccessState': (json) => AccessState.fromJson(_object(json)).toJson(),
  'AgentState': (json) => AgentState.fromWire(json! as String).wire,
  'AgentStatus': (json) => AgentStatus.fromJson(_object(json)).toJson(),
  'AgentWorkspace': (json) => AgentWorkspace.fromJson(_object(json)).toJson(),
  'GateOverride': (json) => GateOverride.fromJson(_object(json)).toJson(),
  'PendingKind': (json) => PendingKind.fromWire(json! as String).wire,
  'PendingRequest': (json) => PendingRequest.fromJson(_object(json)).toJson(),
  'WorkspaceSource': (json) => WorkspaceSource.fromWire(json! as String).wire,
  // answers.json
  'Answer': (json) => Answer.fromJson(_object(json)).toJson(),
  'Answers': (json) => Answers.fromJson(_object(json)).toJson(),
  'Decision': (json) => Decision.fromJson(_object(json)).toJson(),
  'ModeAccessChoice': (json) =>
      ModeAccessChoice.fromJson(_object(json)).toJson(),
  'RecentPrompts': (json) => RecentPrompts.fromJson(_object(json)).toJson(),
  // budgets.json
  'BudgetAgentSpend': (json) =>
      BudgetAgentSpend.fromJson(_object(json)).toJson(),
  'BudgetDaily': (json) => BudgetDaily.fromJson(_object(json)).toJson(),
  'BudgetReport': (json) => BudgetReport.fromJson(_object(json)).toJson(),
  'Budgets': (json) => Budgets.fromJson(_object(json)).toJson(),
  // finish.json
  'CommitDraft': (json) => CommitDraft.fromJson(_object(json)).toJson(),
  'DiffFile': (json) => DiffFile.fromJson(_object(json)).toJson(),
  'DiffResult': (json) => DiffResult.fromJson(_object(json)).toJson(),
  'ExitRequest': (json) => ExitRequest.fromJson(_object(json)).toJson(),
  'ExitResult': (json) => ExitResult.fromJson(_object(json)).toJson(),
  'GateRecord': (json) => GateRecord.fromJson(_object(json)).toJson(),
  'GateResult': (json) => GateResult.fromJson(_object(json)).toJson(),
  'MergeRequest': (json) => MergeRequest.fromJson(_object(json)).toJson(),
  'MergeResult': (json) => MergeResult.fromJson(_object(json)).toJson(),
  // inbox.json
  'Inbox': (json) => Inbox.fromJson(_object(json)).toJson(),
  'InboxDelta': (json) => InboxDelta.fromJson(_object(json)).toJson(),
  'InboxGroups': (json) => InboxGroups.fromJson(_object(json)).toJson(),
  'InboxItem': (json) => InboxItem.fromJson(_object(json)).toJson(),
  'InboxItemKind': (json) => InboxItemKind.fromWire(json! as String).wire,
  // intake.json
  'ActionStatus': (json) => ActionStatus.fromJson(_object(json)).toJson(),
  'ApproveResult': (json) => ApproveResult.fromJson(_object(json)).toJson(),
  'PendingSubmission': (json) =>
      PendingSubmission.fromJson(_object(json)).toJson(),
  // projects.json
  'ProjectStatus': (json) => ProjectStatus.fromJson(_object(json)).toJson(),
  'TrustState': (json) => TrustState.fromWire(json! as String).wire,
  // recipes.json
  'Recipe': (json) => Recipe.fromJson(_object(json)).toJson(),
  'RecipeCopy': (json) => RecipeCopy.fromJson(_object(json)).toJson(),
  'RecipeInput': (json) => RecipeInput.fromJson(_object(json)).toJson(),
  'RecipeLimits': (json) => RecipeLimits.fromJson(_object(json)).toJson(),
  'RecipeRun': (json) => RecipeRun.fromJson(_object(json)).toJson(),
  // schedules.json
  'Schedule': (json) => Schedule.fromJson(_object(json)).toJson(),
  // spawn.json
  'SpawnRequest': (json) => SpawnRequest.fromJson(_object(json)).toJson(),
  'SpawnResult': (json) => SpawnResult.fromJson(_object(json)).toJson(),
  // stream.json
  'BudgetDelta': (json) => BudgetDelta.fromJson(_object(json)).toJson(),
  'BudgetScope': (json) => BudgetScope.fromWire(json! as String).wire,
  'DeltaKind': (json) => DeltaKind.fromWire(json! as String).wire,
  'FleetDelta': (json) => FleetDelta.fromJson(_object(json)).toJson(),
  'ProjectRemovedDelta': (json) =>
      ProjectRemovedDelta.fromJson(_object(json)).toJson(),
};

void main() {
  test('contract fixtures round trip', () {
    final seen = <String>{};
    for (final name in _fixtures) {
      final file = File('../test/contract/$name.json');
      expect(file.existsSync(), isTrue, reason: '${file.path} is missing');
      final values =
          jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
      for (final entry in values.entries) {
        final codec = _codecs[entry.key];
        expect(
          codec,
          isNotNull,
          reason:
              '$name.json names ${entry.key}, which this test has no '
              'codec for',
        );
        seen.add(entry.key);
        expect(
          codec!(entry.value),
          equals(entry.value),
          reason: '$name.json: ${entry.key} does not round trip',
        );
      }
    }
    final missing = _codecs.keys.where((name) => !seen.contains(name));
    expect(
      missing,
      isEmpty,
      reason: 'no fixture value for: ${missing.join(', ')}',
    );
  });

  test('an enum value this build does not know survives the round trip', () {
    // agents.json carries a status no AgentState constant names, the way a
    // newer bridge would.
    final values = jsonDecode(
      File('../test/contract/agents.json').readAsStringSync(),
    ) as Map<String, dynamic>;
    final agent = AgentStatus.fromJson(_object(values['AgentStatus']));
    expect(
      agent.status,
      AgentState.unknown,
      reason: 'the fixture holds a status this build does not know',
    );
    expect(
      agent.toJson()['status'],
      'quarantined',
      reason: 'the wire text is written back, not the word unknown',
    );
  });
}

/// The JSON object [json] names.
Map<String, dynamic> _object(Object? json) => json! as Map<String, dynamic>;
