package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// This file assembles the preview-environment report from the frozen contract
// (preview_environment.go) and the read-only forge adapters
// (preview_environment_forge*.go).
//
// The read is observational on every path. The only requests are the read-only
// forge probes, and only for a repository whose forge is declared; nothing here
// reads a checkout, touches the workspace, or persists a report. Every failure
// is classified by the discovery contract's contextual classifier, so the 404,
// 401, 403 and transport rules are the ones the discovery report already uses.

// The report's own words for the states that are not a failure: a bound that
// was hit, and the terminal case where no scope was resolvable at all. Neither
// says adapter support would be needed: the adapter exists and the instance
// answered; what was not read is a matter of this report's bounds.
const (
	previewPageBoundDetail            = "the forge reported more deployments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest"
	previewStatusesPageBoundDetail    = "the forge reported more statuses for at least one deployment than this report reads; the listed status may not be the newest"
	previewNameCapDetail              = "the forge reported deployments for more environments than this report names; the newest deployment for each of the most recently updated environments is listed"
	previewUnfilteredDetail           = "no revision scope was resolvable; every deployment the forge reports is listed"
	previewStatusesNotExercisedDetail = "not exercised: no deployment was reported for this scope"
	// previewStatusesNoUsableIDDetail is the other not-exercised case: rows
	// exist, but not one carries an id the status URL can use, so saying no
	// deployment was reported would contradict the deployments section.
	previewStatusesNoUsableIDDetail = "not exercised: no deployment carried a usable id"
)

// PreviewEnvironments assembles the read-only preview-environment report for a
// registered repository. It reports what the forge already knows: which
// environments are configured, which deployments exist for the requested scope,
// and each deployment's newest provider-reported status.
//
// The returned error is reserved for a request the bridge cannot assemble at
// all — a registered repository whose id is empty. Everything else is
// classified into the report and returned with a nil error, so a forge that
// cannot answer is a report whose status and error say so.
func (f *Fleet) PreviewEnvironments(ctx context.Context, repo Repo, filter PreviewFilter) (*PreviewReport, error) {
	return f.previewEnvironments(ctx, repo, filter, nil)
}

// previewEnvironments is the testable form of PreviewEnvironments: client
// overrides the shared forge client so a test can observe whether any request
// happens.
func (f *Fleet) previewEnvironments(ctx context.Context, repo Repo, filter PreviewFilter, client *http.Client) (*PreviewReport, error) {
	if repo.ID == "" {
		return nil, fmt.Errorf("bridge: preview environments need a registered repository id")
	}
	a := newPreviewAssembler(repo, filter)
	a.probe(ctx, f, client)
	return a.report(), nil
}

// previewAssembler collects one report. Capabilities are keyed by capability
// key so a key can never be reported twice or vanish from the wire, and every
// one of the frozen three starts visibly unsupported.
type previewAssembler struct {
	repo   Repo
	filter PreviewFilter

	// scope is the resolved revision scope echoed in the report. unfiltered
	// records the terminal case where nothing was resolvable.
	scope      PreviewFilter
	unfiltered bool

	caps map[string]DiscoveryCapability
	gaps map[string]DiscoveryGap

	environments []PreviewEnvironment
	deployments  []PreviewDeployment

	// statusesUnavailable records that the statuses key was degraded with the
	// deployments read, so the status fan-out does not overwrite it.
	statusesUnavailable bool

	outage  string
	unavail bool
}

// newPreviewAssembler seeds all three capability keys.
func newPreviewAssembler(repo Repo, filter PreviewFilter) *previewAssembler {
	a := &previewAssembler{
		repo:   repo,
		filter: filter,
		caps:   make(map[string]DiscoveryCapability, len(previewCapabilityOrder)),
		gaps:   make(map[string]DiscoveryGap, len(previewCapabilityOrder)),
	}
	for _, key := range previewCapabilityOrder {
		a.caps[key] = DiscoveryCapability{
			Key:    key,
			State:  DiscoveryCapabilityUnsupported,
			Detail: "the preview probe did not read this capability",
		}
	}
	return a
}

// set records one capability.
func (a *previewAssembler) set(key string, state DiscoveryCapabilityState, detail string) {
	a.caps[key] = DiscoveryCapability{Key: key, State: state, Detail: detail}
}

// degrade records a capability that is not supported and the gap explaining
// what would fix it.
func (a *previewAssembler) degrade(key, section string, state DiscoveryCapabilityState, detail string) {
	a.set(key, state, detail)
	a.addGap(key, section, state, previewGapDetail(detail, state, a.repo.Forge != ""))
}

// addGap records one gap per capability key: two failures in one capability
// fold into the first instead of being reported twice.
func (a *previewAssembler) addGap(key, section string, state DiscoveryCapabilityState, detail string) {
	if _, ok := a.gaps[key]; ok {
		return
	}
	a.gaps[key] = DiscoveryGap{Key: key, Section: section, State: state, Detail: detail}
}

// degradeAll marks all three capabilities with one shared state and cause, for
// the paths where nothing can be read at all.
func (a *previewAssembler) degradeAll(state DiscoveryCapabilityState, detail string) {
	a.degrade("preview_environments", previewSectionEnvironments, state, detail)
	a.degrade("preview_deployments", previewSectionDeployments, state, detail)
	a.degrade("preview_statuses", previewSectionStatuses, state, detail)
}

// probe runs the reads in the contract's fixed order, which is what makes each
// failure distinguishable.
func (a *previewAssembler) probe(ctx context.Context, f *Fleet, client *http.Client) {
	repo := a.repo
	if repo.Forge == "" {
		// The sanctioned Git-only path: no adapter is engaged and no request is
		// made, which is "unsupported", not a failure to hide.
		a.degradeAll(DiscoveryCapabilityUnsupported, "no forge is declared for this repository")
		return
	}

	preview, previewErr := PreviewForgeFor(repo, client)
	if previewErr != nil {
		// A family with no preview adapter: unsupported, never an error. The
		// cause is written explicitly rather than re-classified, because the
		// classifier's sentinel trim would leave only the bare `forge "x"`,
		// which names neither the reason nor a remedy.
		a.degradeAll(DiscoveryCapabilityUnsupported,
			fmt.Sprintf("forge %q has no read-only preview adapter in this bridge", repo.Forge))
		return
	}
	// The metadata probe is the discovery read: it establishes whether a later
	// 404 means "this instance does not serve the endpoint" or "this credential
	// cannot see the repository", and it supplies the default branch.
	metadata, metadataErr := DiscoveryForgeFor(repo, client)
	if metadataErr != nil {
		state, detail := discoveryProbeOutcome(metadataErr, false)
		a.degradeAll(state, detail)
		return
	}

	cred, credErr := f.discoveryCredential(ctx, repo)
	if credErr != nil {
		// An absent, non-PAT or unusable credential is unconfigured, and no
		// request is attempted with it. It is degraded before any classification
		// call: discoveryCredential returns plain errors, which the shared
		// classifier would report as temporarily_unavailable.
		a.degradeAll(DiscoveryCapabilityUnconfigured,
			"no usable personal access token credential for this repository: "+credErr.Error())
		return
	}

	meta, metaErr := metadata.RepositoryMetadata(ctx, repo, cred)
	metadataReadable := metaErr == nil
	if metaErr != nil {
		state, detail := discoveryProbeOutcome(metaErr, false)
		switch state {
		case DiscoveryCapabilityTemporarilyUnavailable:
			// A declared forge that cannot answer at all is a visible outage,
			// never a silent downgrade to "this repository has no previews".
			a.unavail = true
			a.outage = fmt.Sprintf(
				"the declared %s forge could not be reached while reading repository metadata", repo.Forge)
			a.degradeAll(state, "the forge could not be reached while reading repository metadata")
			return
		case DiscoveryCapabilityUnconfigured:
			// An unusable instance setting or credential is instance-wide, so
			// every preview capability shares it.
			a.degradeAll(state, detail)
			return
		}
		// Any other failure is visible only through the metadataReadable flag
		// that changes the 404 classification: the reads continue.
	}

	a.resolveScope(meta, metadataReadable)
	a.readEnvironments(ctx, preview, cred, metadataReadable)
	a.readDeployments(ctx, preview, cred, metadataReadable)
	a.readStatuses(ctx, preview, cred, metadataReadable)
}

// resolveScope applies the contract's order: both query parameters when given,
// otherwise the registered branch, otherwise the forge's default branch. A sha
// scope never borrows a ref and a ref scope never borrows a sha.
func (a *previewAssembler) resolveScope(meta DiscoveryRepositoryMetadata, metadataReadable bool) {
	a.scope = a.filter
	if a.scope.Ref != "" || a.scope.SHA != "" {
		return
	}
	switch {
	case a.repo.Branch != "":
		a.scope.Ref = a.repo.Branch
	case metadataReadable && meta.DefaultBranch != "":
		a.scope.Ref = meta.DefaultBranch
	default:
		// Terminal case: the read is unfiltered and the capability says so, so
		// "unfiltered" is never confused with "an empty scope was applied".
		a.unfiltered = true
	}
}

// readEnvironments reads the configured deployment environments. A refused read
// degrades this capability alone.
func (a *previewAssembler) readEnvironments(ctx context.Context, probe PreviewForge, cred Credential, metadataReadable bool) {
	environments, err := probe.Environments(ctx, a.repo, cred)
	truncated := errors.Is(err, errDiscoveryTruncated)
	if err != nil && !truncated {
		state, detail := discoveryProbeOutcome(err, metadataReadable)
		a.degrade("preview_environments", previewSectionEnvironments, state, detail)
		return
	}
	a.set("preview_environments", DiscoveryCapabilitySupported, "")
	a.environments = environments
	if truncated {
		a.addGap("preview_environments", previewSectionEnvironments,
			DiscoveryCapabilityTemporarilyUnavailable,
			"the forge reported more environments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest")
	}
}

// readDeployments reads the deployments for the resolved scope, then applies
// the bounds pipeline: group by environment keeping the greatest id, order by
// id descending, keep the first previewNamedDeploymentLimit.
func (a *previewAssembler) readDeployments(ctx context.Context, probe PreviewForge, cred Credential, metadataReadable bool) {
	deployments, err := probe.Deployments(ctx, a.repo, a.scope, cred)
	truncated := errors.Is(err, errDiscoveryTruncated)
	if err != nil && !truncated {
		state, detail := discoveryProbeOutcome(err, metadataReadable)
		a.degrade("preview_deployments", previewSectionDeployments, state, detail)
		// The statuses cannot be exercised while their parent resource is
		// unreadable: an instance that does not serve deployments does not
		// serve their statuses, and a credential refused one is refused the
		// other. The capability carries the same cause in its own gap.
		a.degrade("preview_statuses", previewSectionStatuses, state, detail)
		a.statusesUnavailable = true
		return
	}

	detail := ""
	if a.unfiltered {
		detail = previewUnfilteredDetail
	}
	a.set("preview_deployments", DiscoveryCapabilitySupported, detail)

	if truncated {
		a.addGap("preview_deployments", previewSectionDeployments,
			DiscoveryCapabilityTemporarilyUnavailable, previewPageBoundDetail)
	}
	named, capped := previewNameNewestPerEnvironment(deployments)
	a.deployments = named
	if capped {
		a.addGap("preview_deployments", previewSectionDeployments,
			DiscoveryCapabilityTemporarilyUnavailable, previewNameCapDetail)
	}
}

// readStatuses reads the statuses of the deployments the report names, and
// folds each newest status into its row. A failed read keeps the row with empty
// status fields; only the capability state and its one gap reflect the failure.
func (a *previewAssembler) readStatuses(ctx context.Context, probe PreviewForge, cred Credential, metadataReadable bool) {
	if a.statusesUnavailable {
		return
	}
	if len(a.deployments) == 0 {
		a.set("preview_statuses", DiscoveryCapabilitySupported, previewStatusesNotExercisedDetail)
		return
	}

	read := 0
	truncated := false
	gaps := make([]DiscoveryGap, 0, len(a.deployments))
	for i := range a.deployments {
		id, numeric := previewNumericID(a.deployments[i].ID)
		if !numeric {
			// The status URL needs a usable id, so a deployment with no numeric
			// id keeps its row without a status read.
			continue
		}
		read++
		statuses, err := probe.DeploymentStatuses(ctx, a.repo, id, cred)
		bounded := errors.Is(err, errDiscoveryTruncated)
		if err != nil && !bounded {
			state, detail := discoveryProbeOutcome(err, metadataReadable)
			gaps = append(gaps, DiscoveryGap{
				Key:     "preview_statuses",
				Section: previewSectionStatuses,
				State:   state,
				Detail:  detail,
			})
			continue
		}
		truncated = truncated || bounded
		newest, ok := previewNewestStatus(statuses)
		if !ok {
			// An empty collection is a completed read of nothing, not a failure.
			continue
		}
		a.deployments[i].State = newest.State
		a.deployments[i].EnvironmentURL = newest.EnvironmentURL
		a.deployments[i].TargetURL = newest.TargetURL
		a.deployments[i].LogURL = newest.LogURL
		a.deployments[i].Description = newest.Description
	}

	if len(gaps) == 0 {
		if read == 0 {
			// Rows exist (an empty list returned above), so none of them carried
			// an id the status URL can use: report that rather than claiming no
			// deployment was reported.
			a.set("preview_statuses", DiscoveryCapabilitySupported, previewStatusesNoUsableIDDetail)
			return
		}
		a.set("preview_statuses", DiscoveryCapabilitySupported, "")
	} else {
		// Several status reads can fail differently: the capability and its single
		// gap carry the most actionable state, so the operator sees the failure
		// they can act on first.
		worst := discoveryWorstState(gaps)
		detail := ""
		for _, gap := range gaps {
			if gap.State == worst {
				detail = gap.Detail
				break
			}
		}
		a.degrade("preview_statuses", previewSectionStatuses, worst, detail)
	}

	// The bound is recorded last, so a failure keeps the key: the more
	// actionable state is the one the operator can act on, and addGap leaves
	// the existing gap untouched when a failure already holds it.
	if truncated {
		a.addGap("preview_statuses", previewSectionStatuses,
			DiscoveryCapabilityTemporarilyUnavailable, previewStatusesPageBoundDetail)
	}
}

// report orders every slice deterministically and derives the status from the
// assembled capabilities and gaps.
func (a *previewAssembler) report() *PreviewReport {
	report := &PreviewReport{
		RepoID: a.repo.ID,
		Forge:  a.repo.Forge,
		Ref:    a.scope.Ref,
		SHA:    a.scope.SHA,
	}

	report.Capabilities = make([]DiscoveryCapability, 0, len(previewCapabilityOrder))
	for _, key := range previewCapabilityKeys() {
		capability, ok := a.caps[key]
		if !ok {
			capability = DiscoveryCapability{
				Key:    key,
				State:  DiscoveryCapabilityUnsupported,
				Detail: "the preview probe did not read this capability",
			}
		}
		report.Capabilities = append(report.Capabilities, capability)
	}

	report.Environments = append([]PreviewEnvironment{}, a.environments...)
	report.Deployments = append([]PreviewDeployment{}, a.deployments...)

	report.Gaps = make([]DiscoveryGap, 0, len(a.gaps))
	for _, key := range previewCapabilityKeys() {
		if gap, ok := a.gaps[key]; ok {
			report.Gaps = append(report.Gaps, gap)
		}
	}

	switch {
	case a.unavail:
		report.Status = discoveryStatusUnavailable
		report.Error = a.outage
	case a.degraded(report.Capabilities, report.Gaps):
		report.Status = discoveryStatusDegraded
	default:
		report.Status = discoveryStatusOK
	}
	return report
}

// degraded reports whether any capability is not supported or any gap exists.
func (a *previewAssembler) degraded(caps []DiscoveryCapability, gaps []DiscoveryGap) bool {
	if len(gaps) > 0 {
		return true
	}
	for _, capability := range caps {
		if capability.State != DiscoveryCapabilitySupported {
			return true
		}
	}
	return false
}

// previewNumericID parses a provider id for ordering. Deployment and status ids
// increase monotonically, so the numeric value is the order.
func previewNumericID(id string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// previewNameNewestPerEnvironment applies the bounds pipeline: group by
// environment keeping the deployment with the greatest id, order the survivors
// by id descending, and keep the first previewNamedDeploymentLimit. It reports
// whether the cap was hit.
//
// The cap applies to the newest deployment per environment, not to the newest N
// deployments, so a branch with many environments yields one row per
// environment rather than N rows that might all belong to one.
func previewNameNewestPerEnvironment(deployments []PreviewDeployment) ([]PreviewDeployment, bool) {
	type named struct {
		deployment PreviewDeployment
		id         int64
		numeric    bool
	}
	survivors := make([]named, 0, len(deployments))
	index := make(map[string]int, len(deployments))
	for _, deployment := range deployments {
		id, numeric := previewNumericID(deployment.ID)
		at, seen := index[deployment.Environment]
		if !seen {
			index[deployment.Environment] = len(survivors)
			survivors = append(survivors, named{deployment: deployment, id: id, numeric: numeric})
			continue
		}
		current := survivors[at]
		switch {
		case !numeric:
			// A non-numeric id loses to any numeric id within its environment,
			// so it survives grouping only as the sole member of its group.
			continue
		case !current.numeric || id > current.id:
			survivors[at] = named{deployment: deployment, id: id, numeric: true}
		}
	}

	sort.SliceStable(survivors, func(i, j int) bool {
		left, right := survivors[i], survivors[j]
		if left.numeric != right.numeric {
			// Numeric ids first; a non-numeric survivor comes after every one.
			return left.numeric
		}
		if !left.numeric {
			// No id orders two non-numeric survivors: arrival order stands.
			return false
		}
		return left.id > right.id
	})

	capped := len(survivors) > previewNamedDeploymentLimit
	if capped {
		survivors = survivors[:previewNamedDeploymentLimit]
	}
	out := make([]PreviewDeployment, 0, len(survivors))
	for _, survivor := range survivors {
		out = append(out, survivor.deployment)
	}
	return out, capped
}

// previewNewestStatus returns the status that describes the environment's
// current moment: the greatest id, falling back to arrival order when the
// provider reports no numeric ids.
func previewNewestStatus(statuses []PreviewDeploymentStatus) (PreviewDeploymentStatus, bool) {
	var newest PreviewDeploymentStatus
	found := false
	var newestID int64
	numeric := false
	for _, status := range statuses {
		id, ok := previewNumericID(status.ID)
		switch {
		case !found:
			newest, found = status, true
			newestID, numeric = id, ok
		case ok && (!numeric || id > newestID):
			newest, newestID, numeric = status, id, true
		case !ok && !numeric:
			newest = status
		}
	}
	return newest, found
}
