package bridge

import (
	"context"
	"fmt"
	"net/http"
)

// PreviewForge is the read-only forge surface the preview-environment probe
// uses. It is a separate, optional interface — neither Forge nor DiscoveryForge
// widens — so that adding a preview read never turns a mutating surface into a
// probe, and so a forge that serves no such resource is discovered by asking
// the instance rather than by a family table. Every method is a read.
type PreviewForge interface {
	// Environments reports the repository's configured deployment environments.
	Environments(ctx context.Context, repo Repo, cred Credential) ([]PreviewEnvironment, error)
	// Deployments reports the deployments the forge records for the scope.
	// An empty filter dimension means "do not filter on it".
	Deployments(ctx context.Context, repo Repo, filter PreviewFilter, cred Credential) ([]PreviewDeployment, error)
	// DeploymentStatuses reports the status records for one deployment.
	DeploymentStatuses(ctx context.Context, repo Repo, deploymentID int64, cred Credential) ([]PreviewDeploymentStatus, error)
}

// PreviewForgeFor builds the read-only preview adapter for a repo's declared
// forge, mirroring ForgeFor and DiscoveryForgeFor. A repo whose family has no
// preview adapter is not an error condition for the report: it is signalled
// with errDiscoveryUnsupported so the classifier reports the capabilities as
// unsupported rather than as a failure.
func PreviewForgeFor(repo Repo, client *http.Client) (PreviewForge, error) {
	if client == nil {
		client = forgeHTTPClient
	}
	switch repo.Forge {
	case "github":
		return &previewGitHubForge{client: client}, nil
	case "gitea":
		// Gitea and Forgejo, which forked from it and keeps API compatibility.
		return &previewGiteaForge{client: client}, nil
	default:
		return nil, fmt.Errorf("forge %q: %w", repo.Forge, errDiscoveryUnsupported)
	}
}

// previewIDString renders a native numeric id as the string the report carries.
// An id the forge did not report stays empty rather than becoming "0".
func previewIDString(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}
