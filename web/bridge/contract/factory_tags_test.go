package contract

import (
	"reflect"
	"testing"
)

// factoryJSONTags pins the JSON tag of every field of every type
// contract/factory.go declares.
//
// The tags are a wire format and, for a work item, a durable one: the journal
// stores the item re-marshalled, and replayRecord re-marshals each record and
// byte-compares it against the payload it read, so a renamed tag makes the
// journal unreadable and webbridge refuses to start on an upgraded store. Both
// generated clients post and decode these keys too, so a rename breaks them
// against a bridge from the previous release.
//
// The fixture round trip in fixture_test.go does not catch this: a rename that
// also rewrites test/contract/factory.json round trips, and the currency tests
// then regenerate both clients to the new key, so nothing fails until a journal
// or an older client is met. This table is the independent record the
// generators do not hold, so a rename fails here first.
//
// Adding a field is a deliberate edit to this table as well: the test fails on
// a field it does not list, and on an entry the type no longer has.
var factoryJSONTags = map[reflect.Type]map[string]string{
	reflect.TypeOf(EditSnapshot{}):              {"BaseCommit": "baseCommit", "Files": "files", "SHA256": "sha256"},
	reflect.TypeOf(FactoryApproval{}):           {"ApprovedAt": "approvedAt", "Config": "config", "EditSnapshot": "editSnapshot,omitempty", "ImplementerSubagents": "implementerSubagents", "Plan": "plan", "PlanHash": "planHash", "ReviewerSubagents": "reviewerSubagents", "Revision": "revision", "Spec": "spec"},
	reflect.TypeOf(FactoryAssessment{}):         {"Fresh": "fresh", "InputDigest": "inputDigest", "ObservedAt": "observedAt", "Source": "source,omitempty", "Status": "status", "Summary": "summary,omitempty"},
	reflect.TypeOf(FactoryCI{}):                 {"Checks": "checks", "Complete": "complete", "HeadSHA": "headSha", "ObservedAt": "observedAt"},
	reflect.TypeOf(FactoryCheck{}):              {"Conclusion": "conclusion", "ID": "id", "Key": "key", "LogTail": "logTail", "ObservedAt": "observedAt", "SHA": "sha", "Status": "status", "URL": "url"},
	reflect.TypeOf(FactoryCheckKey{}):           {"AppID": "appId", "Kind": "kind", "Name": "name"},
	reflect.TypeOf(FactoryCommand{}):            {"AdditionalRounds": "additionalRounds", "Answer": "answer", "Completion": "completion,omitempty", "ConfirmPreviousWorkerStopped": "confirmPreviousWorkerStopped", "DecisionID": "decisionId", "ExpectedVersion": "expectedVersion", "ImplementerFreeze": "-", "ImplementerRouting": "implementerRouting", "Kind": "kind", "Reason": "reason", "Replacement": "replacement", "RequestID": "requestId", "ReviewerFreeze": "-", "ReviewerRouting": "reviewerRouting", "Revision": "revision,omitempty", "SettingsVersion": "settingsVersion,omitempty"},
	reflect.TypeOf(FactoryCompletion{}):         {"Assessment": "assessment,omitempty", "Batch": "batch,omitempty", "Links": "links,omitempty", "Records": "records,omitempty", "Status": "status", "StopState": "stopState,omitempty"},
	reflect.TypeOf(FactoryCompletionCommand{}):  {"CompleteAfter": "completeAfter,omitempty", "Link": "link,omitempty", "PRs": "prs,omitempty", "RunChoice": "runChoice,omitempty", "Strategy": "strategy,omitempty"},
	reflect.TypeOf(FactoryCompletionPR{}):       {"BaseRef": "baseRef", "HeadSHA": "headSha", "NodeID": "nodeId", "Number": "number", "RepoID": "repoId"},
	reflect.TypeOf(FactoryCompletionRecord{}):   {"Actor": "actor", "Assessment": "assessment,omitempty", "At": "at", "Criteria": "criteria", "Goal": "goal", "ID": "id", "Links": "links", "RunChoice": "runChoice,omitempty"},
	reflect.TypeOf(FactoryConfig{}):             {"CIPollSeconds": "ciPollSeconds", "CIWaitSeconds": "ciWaitSeconds", "ExpectedChecks": "expectedChecks", "HostID": "hostId", "ImplementerAuto": "implementerAuto", "ImplementerRouting": "implementerRouting", "ImplementerSubagents": "implementerSubagents,omitempty", "MaxRounds": "maxRounds", "Native": "native", "NoRequiredCIAcknowledged": "noRequiredCiAcknowledged", "ProjectRoot": "projectRoot", "RepoID": "repoId", "ReviewerInstructions": "reviewerInstructions", "ReviewerRouting": "reviewerRouting", "ReviewerSubagents": "reviewerSubagents,omitempty", "TargetBranch": "targetBranch", "Workspace": "workspace"},
	reflect.TypeOf(FactoryCreate{}):             {"Config": "config", "EditSnapshot": "editSnapshot,omitempty", "Plan": "plan", "RequestID": "requestId", "Spec": "spec", "Title": "title"},
	reflect.TypeOf(FactoryDecision{}):           {"CreatedAt": "createdAt", "EffectID": "effectId", "ID": "id", "Kind": "kind", "Question": "question", "Reason": "reason"},
	reflect.TypeOf(FactoryDelegatedTurn{}):      {"ChildIDs": "childIds", "ContinuationID": "continuationId,omitempty", "EffectID": "effectId", "ParentTurnID": "parentTurnId", "SessionID": "sessionId", "State": "state", "WorkerID": "workerId"},
	reflect.TypeOf(FactoryEffect{}):             {"ApprovalRevision": "approvalRevision", "CreatedAt": "createdAt", "Delegated": "delegated,omitempty", "ExpectedBase": "expectedBase", "ExpectedHead": "expectedHead", "ID": "id", "Kind": "kind", "Outcome": "outcome", "Round": "round", "Status": "status", "Worker": "worker", "WorkerRole": "workerRole"},
	reflect.TypeOf(FactoryEvent{}):              {"At": "at", "EffectID": "effectId", "Kind": "kind", "Message": "message", "Provenance": "provenance,omitempty", "Sequence": "sequence"},
	reflect.TypeOf(FactoryExcerpt{}):            {"Code": "code", "Lang": "lang,omitempty", "Path": "path"},
	reflect.TypeOf(FactoryGrant{}):              {"AdditionalRounds": "additionalRounds", "At": "at", "ID": "id", "Reason": "reason"},
	reflect.TypeOf(FactoryItem{}):               {"Approvals": "approvals", "ApprovedAt": "approvedAt", "BaseSHA": "baseSha", "Branch": "branch", "Completion": "completion,omitempty", "CreatedAt": "createdAt", "Decision": "decision", "Draft": "draft", "Effects": "effects", "Grants": "grants", "History": "history", "HostID": "hostId", "ID": "id", "PRs": "prs", "ProjectRoot": "projectRoot", "Proposals": "proposals,omitempty", "RepoID": "repoId", "Rounds": "rounds", "Stage": "stage", "State": "state", "Title": "title", "UpdatedAt": "updatedAt", "Version": "version", "Workers": "workers"},
	reflect.TypeOf(FactoryMergeBatch{}):         {"CompleteAfter": "completeAfter", "ID": "id", "RequestID": "requestId", "RunChoice": "runChoice,omitempty", "State": "state", "Steps": "steps"},
	reflect.TypeOf(FactoryMergeStep{}):          {"BaseRef": "baseRef", "HeadSHA": "headSha", "NodeID": "nodeId", "Number": "number", "RepoID": "repoId", "Result": "result,omitempty", "State": "state", "Strategy": "strategy"},
	reflect.TypeOf(FactoryNarration{}):          {"Alternatives": "alternatives", "Approach": "approach", "Choice": "choice", "Excerpts": "excerpts,omitempty", "ExistingBehaviour": "existingBehaviour", "Issue": "issue", "Limitations": "limitations", "Tradeoffs": "tradeoffs", "Verification": "verification"},
	reflect.TypeOf(FactoryOutcome{}):            {"BaseSHA": "baseSha", "Confirmed": "confirmed", "Continuation": "-", "Delegated": "delegated,omitempty", "EffectID": "effectId", "Error": "error", "HeadSHA": "headSha", "PR": "pr", "Report": "report", "StopReason": "stopReason", "TreeSHA": "treeSha", "Worker": "worker"},
	reflect.TypeOf(FactoryPR{}):                 {"BaseRef": "baseRef", "BaseSHA": "baseSha", "Draft": "draft", "HeadRef": "headRef", "HeadSHA": "headSha", "NodeID": "nodeId", "Number": "number", "State": "state", "URL": "url"},
	reflect.TypeOf(FactoryPRLink{}):             {"Fresh": "fresh", "ObservedAt": "observedAt", "Origin": "origin", "PR": "pr", "RepoID": "repoId"},
	reflect.TypeOf(FactoryProvenance{}):         {"Action": "action", "Authority": "authority,omitempty", "AuthorityOrigin": "authorityOrigin,omitempty", "ClientID": "clientId", "DecisionID": "decisionId,omitempty", "InstanceID": "instanceId,omitempty", "ProjectID": "projectId", "RequestID": "requestId", "Role": "role,omitempty", "SessionID": "sessionId,omitempty", "WorkerID": "workerId,omitempty"},
	reflect.TypeOf(FactoryReport{}):             {"BaseSHA": "baseSha", "Findings": "findings", "HeadSHA": "headSha", "InputsAccessible": "inputsAccessible", "Narration": "narration,omitempty", "OperationID": "operationId", "Outcome": "outcome", "Question": "question", "Reason": "reason", "Summary": "summary"},
	reflect.TypeOf(FactoryRevisionAssessment{}): {"AffectedEffectID": "affectedEffectId,omitempty", "AffectedWorkerID": "affectedWorkerId,omitempty", "AssessorID": "assessorId", "BaseRevision": "baseRevision", "CandidateHash": "candidateHash", "Classification": "classification", "Reason": "reason", "Recommendation": "recommendation", "Strategy": "strategy"},
	reflect.TypeOf(FactoryRevisionCommand{}):    {"Assessment": "assessment,omitempty", "AttemptID": "attemptId,omitempty", "BaseHash": "baseHash,omitempty", "BaseRevision": "baseRevision,omitempty", "CandidateHash": "candidateHash,omitempty", "FollowUp": "followUp,omitempty", "Plan": "plan,omitempty", "ProposalID": "proposalId,omitempty", "ProposalVersion": "proposalVersion,omitempty", "Routing": "routing,omitempty", "Spec": "spec,omitempty", "Strategy": "strategy,omitempty"},
	reflect.TypeOf(FactoryRevisionDelivery{}):   {"CandidateHash": "candidateHash", "RequestID": "requestId,omitempty", "SessionID": "sessionId,omitempty", "State": "state", "Strategy": "strategy", "TurnID": "turnId,omitempty"},
	reflect.TypeOf(FactoryRevisionProposal{}):   {"Assessment": "assessment,omitempty", "AssessmentAttemptID": "assessmentAttemptId,omitempty", "AssessmentState": "assessmentState", "Authority": "authority,omitempty", "AuthorizedBy": "authorizedBy,omitempty", "AutoAssess": "autoAssess", "BaseHash": "baseHash", "BaseRevision": "baseRevision", "CandidateHash": "candidateHash", "CreatedAt": "createdAt", "CreatedBy": "createdBy", "Delivery": "delivery,omitempty", "FollowUpItemID": "followUpItemId,omitempty", "FollowUpRequestID": "followUpRequestId,omitempty", "ID": "id", "PauseRequestID": "pauseRequestId,omitempty", "PauseState": "pauseState,omitempty", "Plan": "plan", "Routing": "routing,omitempty", "Spec": "spec", "Status": "status", "UpdatedAt": "updatedAt", "Version": "version"},
	reflect.TypeOf(FactoryRound{}):              {"ApprovalRevision": "approvalRevision", "BaseSHA": "baseSha", "CI": "ci", "FinishedAt": "finishedAt", "HeadSHA": "headSha", "ImplementEffectID": "implementEffectId", "Implementation": "implementation", "Number": "number", "Review": "review", "StartedAt": "startedAt", "TreeSHA": "treeSha"},
	reflect.TypeOf(FactorySubagentOverrides{}):  {"Concurrency": "concurrency,omitempty", "Enabled": "enabled,omitempty", "Model": "model,omitempty"},
	reflect.TypeOf(FactorySubagentSettings{}):   {"Concurrency": "concurrency", "Enabled": "enabled", "Model": "model,omitempty"},
	reflect.TypeOf(FactoryWorker{}):             {"AgentID": "agentId", "ApprovalRevision": "approvalRevision,omitempty", "Parked": "parked", "Role": "role", "SessionID": "sessionId", "SpawnEffectID": "spawnEffectId", "Subagents": "subagents"},
	reflect.TypeOf(ReviewFinding{}):             {"Body": "body,omitempty", "Line": "line,omitempty", "Path": "path,omitempty", "Severity": "severity", "StepNode": "stepNode,omitempty", "Title": "title"},
	reflect.TypeOf(SelectedFile{}):              {"BaseSHA256": "baseSha256,omitempty", "ContentSHA256": "contentSha256,omitempty", "Executable": "executable", "Operation": "operation", "Path": "path"},
	reflect.TypeOf(legacyFactoryApproval{}):     {"ApprovedAt": "approvedAt", "Config": "config", "EditSnapshot": "editSnapshot,omitempty", "Plan": "plan", "PlanHash": "planHash", "Revision": "revision", "Spec": "spec"},
	reflect.TypeOf(legacyFactoryWorker{}):       {"AgentID": "agentId", "Parked": "parked", "Role": "role", "SessionID": "sessionId", "SpawnEffectID": "spawnEffectId"},
}

// TestFactoryContractJSONTagsArePinned holds every field of every work item
// type to the JSON tag it had before the types moved into this package.
func TestFactoryContractJSONTagsArePinned(t *testing.T) {
	for typ, want := range factoryJSONTags {
		t.Run(typ.Name(), func(t *testing.T) {
			seen := make(map[string]bool, len(want))
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				tag := field.Tag.Get("json")
				if tag == "" {
					t.Errorf("%s.%s has no json tag", typ.Name(), field.Name)
					continue
				}
				expected, ok := want[field.Name]
				if !ok {
					t.Errorf("%s.%s is not in factoryJSONTags: add %q: %q", typ.Name(), field.Name, field.Name, tag)
					continue
				}
				seen[field.Name] = true
				if tag != expected {
					t.Errorf("%s.%s tag is %q, want %q (a rename changes the journal's bytes and both clients' keys)", typ.Name(), field.Name, tag, expected)
				}
			}
			for name := range want {
				if !seen[name] {
					t.Errorf("%s has no field %s; remove it from factoryJSONTags", typ.Name(), name)
				}
			}
		})
	}
}
