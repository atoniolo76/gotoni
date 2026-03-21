package spicedb

import (
	"context"
	"fmt"
	"os"
	"sync"
)

var (
	authzOnce   sync.Once
	authzClient *Client
	authzUserID string
	authzOrgID  string
)

func initAuthz() {
	authzOnce.Do(func() {
		authzUserID = os.Getenv("GOTONI_USER_ID")
		authzOrgID = os.Getenv("GOTONI_ORG_ID")

		client, err := NewClient()
		if err != nil {
			return
		}
		authzClient = client
	})
}

// Enabled returns true if SpiceDB is configured and user identity is available.
func Enabled() bool {
	initAuthz()
	return authzClient != nil && authzUserID != ""
}

// GetProjectID returns the optional project scope from the environment.
func GetProjectID() string {
	return os.Getenv("GOTONI_PROJECT_ID")
}

// Check verifies the current user has the given permission on the specified
// resource. Returns nil if authz is disabled or permission is granted.
func Check(ctx context.Context, resourceType, resourceID, permission string) error {
	if !Enabled() {
		return nil
	}
	allowed, err := authzClient.CheckPermission(ctx, resourceType, resourceID, permission, "user", authzUserID)
	if err != nil {
		return fmt.Errorf("permission check: %w", err)
	}
	if !allowed {
		return fmt.Errorf("permission denied: user %s does not have %s on %s:%s", authzUserID, permission, resourceType, resourceID)
	}
	return nil
}

// CheckCreate verifies the user can create resources in their org or project.
// Used for operations where the target resource doesn't exist yet.
func CheckCreate(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	if projectID := GetProjectID(); projectID != "" {
		return Check(ctx, "project", projectID, "edit")
	}
	if authzOrgID != "" {
		return Check(ctx, "organization", authzOrgID, "write_access")
	}
	return fmt.Errorf("permission denied: GOTONI_ORG_ID (or GOTONI_PROJECT_ID) is required when SpiceDB is enabled")
}

// WriteResourceOwnership records that the current user owns a newly created
// resource and links it to the org/project.
func WriteResourceOwnership(ctx context.Context, resourceID string) {
	if !Enabled() {
		return
	}
	_ = authzClient.WriteRelationship(ctx, "resource", resourceID, "owner", "user", authzUserID)

	if projectID := GetProjectID(); projectID != "" {
		_ = authzClient.WriteRelationship(ctx, "resource", resourceID, "project", "project", projectID)
	} else if authzOrgID != "" {
		_ = authzClient.WriteRelationship(ctx, "resource", resourceID, "org", "organization", authzOrgID)
	}
}

// DeleteResource removes all relationships for a terminated resource.
func DeleteResource(ctx context.Context, resourceID string) {
	if !Enabled() {
		return
	}
	_ = authzClient.DeleteRelationship(ctx, "resource", resourceID, "owner", "user", authzUserID)

	if projectID := GetProjectID(); projectID != "" {
		_ = authzClient.DeleteRelationship(ctx, "resource", resourceID, "project", "project", projectID)
	} else if authzOrgID != "" {
		_ = authzClient.DeleteRelationship(ctx, "resource", resourceID, "org", "organization", authzOrgID)
	}
}

// ViewableResourceIDs returns the set of resource IDs the current user can
// view. Returns nil if authz is disabled.
func ViewableResourceIDs(ctx context.Context) map[string]bool {
	if !Enabled() {
		return nil
	}
	ids, err := authzClient.LookupResources(ctx, "resource", "view", "user", authzUserID)
	if err != nil {
		return nil
	}
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return allowed
}
