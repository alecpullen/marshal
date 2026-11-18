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
  'automations',
  'budgets',
  'clients',
  'connections',
  'drafts',
  'environments',
  'factory',
  'finish',
  'inbox',
  'intake',
  'issues',
  'library',
  'models',
  'network',
  'notifications',
  'planner',
  'projectsettings',
  'projects',
  'recipes',
  'relay',
  'runs',
  'schedules',
  'secrets',
  'session',
  'spawn',
  'stack',
  'statuslinks',
  'stream',
  'usage',
  'watches',
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
  // issues.json
  'RepoIssue': (json) => RepoIssue.fromJson(_object(json)).toJson(),
  // recipes.json
  'Recipe': (json) => Recipe.fromJson(_object(json)).toJson(),
  'RecipeCopy': (json) => RecipeCopy.fromJson(_object(json)).toJson(),
  'RecipeInput': (json) => RecipeInput.fromJson(_object(json)).toJson(),
  'RecipeLimits': (json) => RecipeLimits.fromJson(_object(json)).toJson(),
  'RecipeRun': (json) => RecipeRun.fromJson(_object(json)).toJson(),
  // clients.json
  'ApprovalAuthority': (json) =>
      ApprovalAuthority.fromWire(json! as String).wire,
  'MCPClient': (json) => MCPClient.fromJson(_object(json)).toJson(),
  'CreateClientRequest': (json) =>
      CreateClientRequest.fromJson(_object(json)).toJson(),
  'CreateClientResult': (json) =>
      CreateClientResult.fromJson(_object(json)).toJson(),
  'MCPFactoryProjectGrant': (json) =>
      MCPFactoryProjectGrant.fromJson(_object(json)).toJson(),
  'FactoryGrantState': (json) =>
      FactoryGrantState.fromJson(_object(json)).toJson(),
  'PutGrantRequest': (json) => PutGrantRequest.fromJson(_object(json)).toJson(),
  // factory.json — a work item in each of its four resting states, its parts,
  // and the command a client posts back.
  'FactoryItemDraft': (json) => FactoryItem.fromJson(_object(json)).toJson(),
  'FactoryItemNeedsInput': (json) =>
      FactoryItem.fromJson(_object(json)).toJson(),
  'FactoryItemReady': (json) => FactoryItem.fromJson(_object(json)).toJson(),
  'FactoryItemClosed': (json) => FactoryItem.fromJson(_object(json)).toJson(),
  'FactoryConfig': (json) => FactoryConfig.fromJson(_object(json)).toJson(),
  'FactoryCreate': (json) => FactoryCreate.fromJson(_object(json)).toJson(),
  'FactoryApproval': (json) => FactoryApproval.fromJson(_object(json)).toJson(),
  'FactoryWorker': (json) => FactoryWorker.fromJson(_object(json)).toJson(),
  'FactoryPR': (json) => FactoryPR.fromJson(_object(json)).toJson(),
  'FactoryCheckKey': (json) => FactoryCheckKey.fromJson(_object(json)).toJson(),
  'FactoryCheck': (json) => FactoryCheck.fromJson(_object(json)).toJson(),
  'FactoryCI': (json) => FactoryCI.fromJson(_object(json)).toJson(),
  'FactoryRound': (json) => FactoryRound.fromJson(_object(json)).toJson(),
  'FactoryReport': (json) => FactoryReport.fromJson(_object(json)).toJson(),
  'FactoryDecision': (json) => FactoryDecision.fromJson(_object(json)).toJson(),
  'FactoryEvent': (json) => FactoryEvent.fromJson(_object(json)).toJson(),
  'FactoryGrant': (json) => FactoryGrant.fromJson(_object(json)).toJson(),
  'FactoryCompletion': (json) =>
      FactoryCompletion.fromJson(_object(json)).toJson(),
  'FactoryCompletionRecord': (json) =>
      FactoryCompletionRecord.fromJson(_object(json)).toJson(),
  'FactoryAssessment': (json) =>
      FactoryAssessment.fromJson(_object(json)).toJson(),
  'FactorySettingsVersion': (json) =>
      FactorySettingsVersion.fromJson(_object(json)).toJson(),
  'FactoryPRLink': (json) => FactoryPRLink.fromJson(_object(json)).toJson(),
  'FactoryMergeBatch': (json) =>
      FactoryMergeBatch.fromJson(_object(json)).toJson(),
  'FactoryMergeStep': (json) =>
      FactoryMergeStep.fromJson(_object(json)).toJson(),
  'FactorySubagentSettings': (json) =>
      FactorySubagentSettings.fromJson(_object(json)).toJson(),
  'FactorySubagentOverrides': (json) =>
      FactorySubagentOverrides.fromJson(_object(json)).toJson(),
  'EditSnapshot': (json) => EditSnapshot.fromJson(_object(json)).toJson(),
  'SelectedFile': (json) => SelectedFile.fromJson(_object(json)).toJson(),
  'ReviewFinding': (json) => ReviewFinding.fromJson(_object(json)).toJson(),
  'FactoryRevisionAssessment': (json) =>
      FactoryRevisionAssessment.fromJson(_object(json)).toJson(),
  'FactoryRevisionProposal': (json) =>
      FactoryRevisionProposal.fromJson(_object(json)).toJson(),
  'FactoryRevisionCommand': (json) =>
      FactoryRevisionCommand.fromJson(_object(json)).toJson(),
  'FactoryCompletionPR': (json) =>
      FactoryCompletionPR.fromJson(_object(json)).toJson(),
  'FactoryCompletionCommand': (json) =>
      FactoryCompletionCommand.fromJson(_object(json)).toJson(),
  'FactoryCommand': (json) => FactoryCommand.fromJson(_object(json)).toJson(),
  // planner.json — the planning thread, its snapshots and its briefing.
  'PlannerOccupancy': (json) => PlannerOccupancy.fromWire(json! as String).wire,
  'WorkerCapabilities': (json) =>
      WorkerCapabilities.fromJson(_object(json)).toJson(),
  'PlannerArtifact': (json) => PlannerArtifact.fromJson(_object(json)).toJson(),
  'PlannerTurn': (json) => PlannerTurn.fromJson(_object(json)).toJson(),
  'PlannerDecision': (json) => PlannerDecision.fromJson(_object(json)).toJson(),
  'PlannerView': (json) => PlannerView.fromJson(_object(json)).toJson(),
  'PlannerCreateRequest': (json) =>
      PlannerCreateRequest.fromJson(_object(json)).toJson(),
  'PlannerPromptRequest': (json) =>
      PlannerPromptRequest.fromJson(_object(json)).toJson(),
  'PlannerAnswerRequest': (json) =>
      PlannerAnswerRequest.fromJson(_object(json)).toJson(),
  'PlannerTurnReply': (json) =>
      PlannerTurnReply.fromJson(_object(json)).toJson(),
  'PlannerEvent': (json) => PlannerEvent.fromJson(_object(json)).toJson(),
  'PlannerSnapshotArtifact': (json) =>
      PlannerSnapshotArtifact.fromJson(_object(json)).toJson(),
  'PlannerSnapshotRequest': (json) =>
      PlannerSnapshotRequest.fromJson(_object(json)).toJson(),
  'PlannerRejection': (json) =>
      PlannerRejection.fromJson(_object(json)).toJson(),
  'PlannerSnapshotFile': (json) =>
      PlannerSnapshotFile.fromJson(_object(json)).toJson(),
  'PlannerSnapshotView': (json) =>
      PlannerSnapshotView.fromJson(_object(json)).toJson(),
  'PlannerSnapshotReply': (json) =>
      PlannerSnapshotReply.fromJson(_object(json)).toJson(),
  'PlannerContextArtifact': (json) =>
      PlannerContextArtifact.fromJson(_object(json)).toJson(),
  'PlannerContextDecision': (json) =>
      PlannerContextDecision.fromJson(_object(json)).toJson(),
  'PlannerContextQuestion': (json) =>
      PlannerContextQuestion.fromJson(_object(json)).toJson(),
  'PlannerContextView': (json) =>
      PlannerContextView.fromJson(_object(json)).toJson(),
  // drafts.json — an unfinished handoff and its versioned writes.
  'WorkingDraft': (json) => WorkingDraft.fromJson(_object(json)).toJson(),
  'WorkingDraftContent': (json) =>
      WorkingDraftContent.fromJson(_object(json)).toJson(),
  'WorkingDraftCreateRequest': (json) =>
      WorkingDraftCreateRequest.fromJson(_object(json)).toJson(),
  'WorkingDraftSaveRequest': (json) =>
      WorkingDraftSaveRequest.fromJson(_object(json)).toJson(),
  'WorkingDraftDeleteRequest': (json) =>
      WorkingDraftDeleteRequest.fromJson(_object(json)).toJson(),
  // library.json
  'SkillEntry': (json) => SkillEntry.fromJson(_object(json)).toJson(),
  'SkillPreview': (json) => SkillPreview.fromJson(_object(json)).toJson(),
  'SkillList': (json) => SkillList.fromJson(_object(json)).toJson(),
  'SkillStage': (json) => SkillStage.fromJson(_object(json)).toJson(),
  'SkillConfirm': (json) => SkillConfirm.fromJson(_object(json)).toJson(),
  'SkillDiscard': (json) => SkillDiscard.fromJson(_object(json)).toJson(),
  'PluginEntry': (json) => PluginEntry.fromJson(_object(json)).toJson(),
  'PluginContents': (json) => PluginContents.fromJson(_object(json)).toJson(),
  'PluginScan': (json) => PluginScan.fromJson(_object(json)).toJson(),
  'PluginList': (json) => PluginList.fromJson(_object(json)).toJson(),
  'PluginStage': (json) => PluginStage.fromJson(_object(json)).toJson(),
  'PluginConfirm': (json) => PluginConfirm.fromJson(_object(json)).toJson(),
  'PluginDiscard': (json) => PluginDiscard.fromJson(_object(json)).toJson(),
  'MemoryEntry': (json) => MemoryEntry.fromJson(_object(json)).toJson(),
  'MemoryList': (json) => MemoryList.fromJson(_object(json)).toJson(),
  'MemorySuggestion': (json) =>
      MemorySuggestion.fromJson(_object(json)).toJson(),
  'MemorySuggestions': (json) =>
      MemorySuggestions.fromJson(_object(json)).toJson(),
  'MemoryPromote': (json) => MemoryPromote.fromJson(_object(json)).toJson(),
  'MemoryConfidence': (json) =>
      MemoryConfidence.fromJson(_object(json)).toJson(),
  // schedules.json
  'Schedule': (json) => Schedule.fromJson(_object(json)).toJson(),
  // automations.json
  'Automations': (json) => Automations.fromJson(_object(json)).toJson(),
  'CIFixerSettings': (json) => CIFixerSettings.fromJson(_object(json)).toJson(),
  'CIHistory': (json) => CIHistory.fromJson(_object(json)).toJson(),
  'Finding': (json) => Finding.fromJson(_object(json)).toJson(),
  'ProjectAutomations': (json) =>
      ProjectAutomations.fromJson(_object(json)).toJson(),
  'ReviewBotSettings': (json) =>
      ReviewBotSettings.fromJson(_object(json)).toJson(),
  'ReviewDraft': (json) => ReviewDraft.fromJson(_object(json)).toJson(),
  'WebhookSecret': (json) => WebhookSecret.fromJson(_object(json)).toJson(),
  // watches.json
  'LastReroute': (json) => LastReroute.fromJson(_object(json)).toJson(),
  'OnTrip': (json) => OnTrip.fromJson(_object(json)).toJson(),
  'RerouteRule': (json) => RerouteRule.fromJson(_object(json)).toJson(),
  'WatchCreate': (json) => WatchCreate.fromJson(_object(json)).toJson(),
  'WatchInfo': (json) => WatchInfo.fromJson(_object(json)).toJson(),
  'WatchSample': (json) => WatchSample.fromJson(_object(json)).toJson(),
  'WatchSpec': (json) => WatchSpec.fromJson(_object(json)).toJson(),
  'WatchStarted': (json) => WatchStarted.fromJson(_object(json)).toJson(),
  // session.json
  'SessionSummary': (json) => SessionSummary.fromJson(_object(json)).toJson(),
  'SessionList': (json) => SessionList.fromJson(_object(json)).toJson(),
  'SessionIndex': (json) => SessionIndex.fromJson(_object(json)).toJson(),
  'ProjectSessions': (json) => ProjectSessions.fromJson(_object(json)).toJson(),
  'SessionRow': (json) => SessionRow.fromJson(_object(json)).toJson(),
  'PromptRequest': (json) => PromptRequest.fromJson(_object(json)).toJson(),
  'SteerRequest': (json) => SteerRequest.fromJson(_object(json)).toJson(),
  'ModeRequest': (json) => ModeRequest.fromJson(_object(json)).toJson(),
  'Roster': (json) => Roster.fromJson(_object(json)).toJson(),
  'RosterRole': (json) => RosterRole.fromJson(_object(json)).toJson(),
  'RosterBudget': (json) => RosterBudget.fromJson(_object(json)).toJson(),
  // spawn.json
  'SpawnRequest': (json) => SpawnRequest.fromJson(_object(json)).toJson(),
  'SpawnResult': (json) => SpawnResult.fromJson(_object(json)).toJson(),
  // stack.json
  'StackSnapshot': (json) => StackSnapshot.fromJson(_object(json)).toJson(),
  'StackPatch': (json) => StackPatch.fromJson(_object(json)).toJson(),
  'WireNode': (json) => WireNode.fromJson(_object(json)).toJson(),
  'WireStep': (json) => WireStep.fromJson(_object(json)).toJson(),
  'WireTodoLabel': (json) => WireTodoLabel.fromJson(_object(json)).toJson(),
  'WireThought': (json) => WireThought.fromJson(_object(json)).toJson(),
  'WireTool': (json) => WireTool.fromJson(_object(json)).toJson(),
  'WireCall': (json) => WireCall.fromJson(_object(json)).toJson(),
  'WireRunning': (json) => WireRunning.fromJson(_object(json)).toJson(),
  'WireCompletion': (json) => WireCompletion.fromJson(_object(json)).toJson(),
  'WirePlan': (json) => WirePlan.fromJson(_object(json)).toJson(),
  'WireMessage': (json) => WireMessage.fromJson(_object(json)).toJson(),
  'WireThinking': (json) => WireThinking.fromJson(_object(json)).toJson(),
  'WireRunEvent': (json) => WireRunEvent.fromJson(_object(json)).toJson(),
  'WireBand': (json) => WireBand.fromJson(_object(json)).toJson(),
  'WireTask': (json) => WireTask.fromJson(_object(json)).toJson(),
  'WireBandEntity': (json) => WireBandEntity.fromJson(_object(json)).toJson(),
  'WireReceipt': (json) => WireReceipt.fromJson(_object(json)).toJson(),
  // runs.json — a plan run, its tasks and the list entry.
  'SDDRun': (json) => SDDRun.fromJson(_object(json)).toJson(),
  'RunTask': (json) => RunTask.fromJson(_object(json)).toJson(),
  'RunStage': (json) => RunStage.fromJson(_object(json)).toJson(),
  'RunCommit': (json) => RunCommit.fromJson(_object(json)).toJson(),
  'RunGate': (json) => RunGate.fromJson(_object(json)).toJson(),
  'RunListEntry': (json) => RunListEntry.fromJson(_object(json)).toJson(),
  // projectsettings.json — settings, health and discovery.
  'ProjectIntake': (json) => ProjectIntake.fromJson(_object(json)).toJson(),
  'FactoryDefaults': (json) => FactoryDefaults.fromJson(_object(json)).toJson(),
  'ProjectSettings': (json) => ProjectSettings.fromJson(_object(json)).toJson(),
  'MirrorHealth': (json) => MirrorHealth.fromJson(_object(json)).toJson(),
  'WorkspaceHealth': (json) => WorkspaceHealth.fromJson(_object(json)).toJson(),
  'VerifyCommands': (json) => VerifyCommands.fromJson(_object(json)).toJson(),
  'ProjectHealth': (json) => ProjectHealth.fromJson(_object(json)).toJson(),
  'DiscoveryCapabilityState': (json) =>
      DiscoveryCapabilityState.fromWire(json! as String).wire,
  'DiscoveryCapability': (json) =>
      DiscoveryCapability.fromJson(_object(json)).toJson(),
  'DiscoveryFact': (json) => DiscoveryFact.fromJson(_object(json)).toJson(),
  'DiscoveryRecommendation': (json) =>
      DiscoveryRecommendation.fromJson(_object(json)).toJson(),
  'DiscoveryGap': (json) => DiscoveryGap.fromJson(_object(json)).toJson(),
  'DiscoveryWorktree': (json) =>
      DiscoveryWorktree.fromJson(_object(json)).toJson(),
  'DiscoveryReport': (json) => DiscoveryReport.fromJson(_object(json)).toJson(),
  // stream.json
  'BudgetDelta': (json) => BudgetDelta.fromJson(_object(json)).toJson(),
  'BudgetScope': (json) => BudgetScope.fromWire(json! as String).wire,
  'DeltaKind': (json) => DeltaKind.fromWire(json! as String).wire,
  'FleetDelta': (json) => FleetDelta.fromJson(_object(json)).toJson(),
  'ProjectRemovedDelta': (json) =>
      ProjectRemovedDelta.fromJson(_object(json)).toJson(),
  'DeltaSessions': (json) => DeltaKind.fromWire(json! as String).wire,
  'SessionsDelta': (json) => SessionsDelta.fromJson(_object(json)).toJson(),
  // connections.json
  'Credential': (json) => Credential.fromJson(_object(json)).toJson(),
  'CredentialInput': (json) => CredentialInput.fromJson(_object(json)).toJson(),
  'Repo': (json) => Repo.fromJson(_object(json)).toJson(),
  // models.json
  'Binding': (json) => Binding.fromJson(_object(json)).toJson(),
  'BudgetsUpdate': (json) => BudgetsUpdate.fromJson(_object(json)).toJson(),
  'ModelsConfig': (json) => ModelsConfig.fromJson(_object(json)).toJson(),
  'Preset': (json) => Preset.fromJson(_object(json)).toJson(),
  'PresetsUpdate': (json) => PresetsUpdate.fromJson(_object(json)).toJson(),
  'ProbeModel': (json) => ProbeModel.fromJson(_object(json)).toJson(),
  'ProbeRequest': (json) => ProbeRequest.fromJson(_object(json)).toJson(),
  'ProbeResult': (json) => ProbeResult.fromJson(_object(json)).toJson(),
  'ProviderKey': (json) => ProviderKey.fromJson(_object(json)).toJson(),
  'ProviderView': (json) => ProviderView.fromJson(_object(json)).toJson(),
  'ProviderWrite': (json) => ProviderWrite.fromJson(_object(json)).toJson(),
  'ProvidersUpdate': (json) => ProvidersUpdate.fromJson(_object(json)).toJson(),
  'RoutingUpdate': (json) => RoutingUpdate.fromJson(_object(json)).toJson(),
  // network.json
  'NetAgentRow': (json) => NetAgentRow.fromJson(_object(json)).toJson(),
  'NetDecision': (json) => NetDecision.fromJson(_object(json)).toJson(),
  'NetDecisionKind': (json) => NetDecisionKind.fromWire(json! as String).wire,
  'NetDecisionResult': (json) =>
      NetDecisionResult.fromJson(_object(json)).toJson(),
  'NetHostRow': (json) => NetHostRow.fromJson(_object(json)).toJson(),
  'NetHosts': (json) => NetHosts.fromJson(_object(json)).toJson(),
  'NetPending': (json) => NetPending.fromJson(_object(json)).toJson(),
  'NetPendingList': (json) => NetPendingList.fromJson(_object(json)).toJson(),
  'NetRecord': (json) => NetRecord.fromJson(_object(json)).toJson(),
  // secrets.json
  'SecretList': (json) => SecretList.fromJson(_object(json)).toJson(),
  'SecretWrite': (json) => SecretWrite.fromJson(_object(json)).toJson(),
  'SecretsStatus': (json) => SecretsStatus.fromJson(_object(json)).toJson(),
  // environments.json
  'EnvBuildStart': (json) => EnvBuildStart.fromJson(_object(json)).toJson(),
  'EnvBuilds': (json) => EnvBuilds.fromJson(_object(json)).toJson(),
  'EnvCreate': (json) => EnvCreate.fromJson(_object(json)).toJson(),
  'EnvDiagnostic': (json) => EnvDiagnostic.fromJson(_object(json)).toJson(),
  'EnvDoc': (json) => EnvDoc.fromJson(_object(json)).toJson(),
  'EnvDraft': (json) => EnvDraft.fromJson(_object(json)).toJson(),
  'EnvFile': (json) => EnvFile.fromJson(_object(json)).toJson(),
  'EnvInject': (json) => EnvInject.fromJson(_object(json)).toJson(),
  'EnvListItem': (json) => EnvListItem.fromJson(_object(json)).toJson(),
  'EnvLoaded': (json) => EnvLoaded.fromJson(_object(json)).toJson(),
  'EnvMeta': (json) => EnvMeta.fromJson(_object(json)).toJson(),
  'EnvMount': (json) => EnvMount.fromJson(_object(json)).toJson(),
  'EnvNetwork': (json) => EnvNetwork.fromJson(_object(json)).toJson(),
  'EnvPackages': (json) => EnvPackages.fromJson(_object(json)).toJson(),
  'EnvPolicy': (json) => EnvPolicy.fromJson(_object(json)).toJson(),
  'EnvPool': (json) => EnvPool.fromJson(_object(json)).toJson(),
  'EnvPreview': (json) => EnvPreview.fromJson(_object(json)).toJson(),
  'EnvResources': (json) => EnvResources.fromJson(_object(json)).toJson(),
  'EnvSection': (json) => EnvSection.fromJson(_object(json)).toJson(),
  'EnvSetup': (json) => EnvSetup.fromJson(_object(json)).toJson(),
  'EnvStarts': (json) => EnvStarts.fromJson(_object(json)).toJson(),
  'EnvVersion': (json) => EnvVersion.fromJson(_object(json)).toJson(),
  'EnvWorkspace': (json) => EnvWorkspace.fromJson(_object(json)).toJson(),
  // usage.json
  'AuditEvent': (json) => AuditEvent.fromJson(_object(json)).toJson(),
  'DiskStatus': (json) => DiskStatus.fromJson(_object(json)).toJson(),
  'PruneResult': (json) => PruneResult.fromJson(_object(json)).toJson(),
  'UsagePoint': (json) => UsagePoint.fromJson(_object(json)).toJson(),
  'UsageReport': (json) => UsageReport.fromJson(_object(json)).toJson(),
  'UsageTotals': (json) => UsageTotals.fromJson(_object(json)).toJson(),
  // notifications.json
  'NotifyConfig': (json) => NotifyConfig.fromJson(_object(json)).toJson(),
  'NotifyTestResult': (json) =>
      NotifyTestResult.fromJson(_object(json)).toJson(),
  'NotifyWebhook': (json) => NotifyWebhook.fromJson(_object(json)).toJson(),
  // statuslinks.json
  'StatusLink': (json) => StatusLink.fromJson(_object(json)).toJson(),
  'StatusLinkCreate': (json) =>
      StatusLinkCreate.fromJson(_object(json)).toJson(),
  'StatusLinkCreated': (json) =>
      StatusLinkCreated.fromJson(_object(json)).toJson(),
  // relay.json — a relay's hosts and the join a bridge makes into one.
  'RelayHosts': (json) => RelayHosts.fromJson(_object(json)).toJson(),
  'RelayHost': (json) => RelayHost.fromJson(_object(json)).toJson(),
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
