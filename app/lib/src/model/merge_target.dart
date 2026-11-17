import '../client/contract.g.dart';

/// Why a work item's Merge and complete is drawn disabled: the item holds no
/// open pull request at a reviewed identity for the bridge to merge.
const noMergeTargetNote =
    'This work item has no open pull request at a reviewed head to merge yet';

/// The run choice a merge records when the item's own run is still active.
///
/// Completing is the person's decision about the work, recorded separately
/// from what the run is doing, so the app never asks a run to stop on their
/// behalf: stopping has its own confirmed control in the header. The bridge
/// refuses a batch that leaves an active run undecided
/// (web/bridge/factory_completion_batch.go), and `continue` is that decision
/// said without cancelling work nobody asked to cancel.
const mergeRunChoice = 'continue';

/// The reviewed pull request a work item's merge names (F6 decision 5).
///
/// It is the identity the bridge checks the batch against: the node id, the
/// head someone reviewed, and the base it lands on.
class MergeTarget {
  const MergeTarget({
    required this.repoID,
    required this.number,
    required this.nodeID,
    required this.headSHA,
    required this.baseRef,
  });

  final String repoID;
  final int number;
  final String nodeID;
  final String headSHA;
  final String baseRef;

  @override
  String toString() => 'MergeTarget($repoID#$number $headSHA → $baseRef)';
}

/// The pull request [item]'s merge names, or null when it holds none.
///
/// The item's own record is the only place a reviewed identity lives: a pull
/// request's `nodeId`, `headSha` and `baseRef` read together from one linked
/// entry, never assembled from separate observations, so a merge cannot name a
/// head the item never linked. The repo is the item's own configured one, which
/// is what the bridge compares a reviewed step against.
///
/// It is null when no linked pull request is open, non-draft, and complete
/// enough to name — the bridge refuses a blank node id (400) and a review that
/// no longer describes its pull request (409), so a surface with no identity
/// disables the control instead of posting something that must fail.
MergeTarget? mergeTargetOf(FactoryItem item) {
  final repoID = item.repoID.trim();
  if (repoID.isEmpty) return null;
  for (final pr in item.pRs) {
    if (pr.state != 'open' || pr.draft) continue;
    if (pr.nodeID.trim().isEmpty ||
        pr.headSHA.trim().isEmpty ||
        pr.baseRef.trim().isEmpty) {
      continue;
    }
    return MergeTarget(
      repoID: repoID,
      number: pr.number,
      nodeID: pr.nodeID,
      headSHA: pr.headSHA,
      baseRef: pr.baseRef,
    );
  }
  return null;
}

/// Whether [item]'s own run is still active, judged from what the item holds:
/// the bridge's `factoryBatchRunActive` (web/bridge/factory_completion_batch.go),
/// so the app names the run choice the bridge will demand instead of posting a
/// command it answers 409.
bool runActiveOfItem(FactoryItem item) {
  if (item.state == 'draft' ||
      item.state == 'closed' ||
      item.state == 'stopped') {
    return false;
  }
  for (final worker in item.workers) {
    if (!worker.parked) return true;
  }
  for (final effect in item.effects) {
    if (effect.status == 'prepared' || effect.status == 'dispatched') {
      return true;
    }
  }
  return false;
}
