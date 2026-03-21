package spicedb

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
)

//go:embed schema.zed
var schema string

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	mu    sync.Mutex
	zedToken string // latest consistency token from writes
}

// NewClient connects to a SpiceDB HTTP API. Requires SPICEDB_ENDPOINT and
// SPICEDB_TOKEN to be set as environment variables.
func NewClient() (*Client, error) {
	endpoint := os.Getenv("SPICEDB_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("SPICEDB_ENDPOINT is not set")
	}
	token := os.Getenv("SPICEDB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("SPICEDB_TOKEN is not set")
	}
	return &Client{
		baseURL:    endpoint,
		token:      token,
		httpClient: &http.Client{},
	}, nil
}

// --- Schema -----------------------------------------------------------------

// ApplySchema writes the embedded schema.zed to SpiceDB.
func (c *Client) ApplySchema(ctx context.Context) error {
	req := map[string]string{"schema": schema}
	_, err := c.post(ctx, "/v1/schema/write", req)
	if err != nil {
		return fmt.Errorf("schema write: %w", err)
	}
	return nil
}

// ReadSchema returns the current schema from SpiceDB.
func (c *Client) ReadSchema(ctx context.Context) (string, error) {
	body, err := c.post(ctx, "/v1/schema/read", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("schema read: %w", err)
	}
	var resp struct {
		SchemaText string `json:"schemaText"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("json.Unmarshal: %w", err)
	}
	return resp.SchemaText, nil
}

// --- Relationships ----------------------------------------------------------

type objectRef struct {
	ObjectType string `json:"objectType"`
	ObjectID   string `json:"objectId"`
}

type subjectRef struct {
	Object objectRef `json:"object"`
}

type relationship struct {
	Resource objectRef  `json:"resource"`
	Relation string     `json:"relation"`
	Subject  subjectRef `json:"subject"`
}

type relUpdate struct {
	Operation    string       `json:"operation"`
	Relationship relationship `json:"relationship"`
}

func makeRel(resourceType, resourceID, relation, subjectType, subjectID string) relationship {
	return relationship{
		Resource: objectRef{ObjectType: resourceType, ObjectID: resourceID},
		Relation: relation,
		Subject:  subjectRef{Object: objectRef{ObjectType: subjectType, ObjectID: subjectID}},
	}
}

// WriteRelationship creates or touches a single relationship tuple.
// Example: WriteRelationship(ctx, "organization", "acme", "admin", "user", "alice")
func (c *Client) WriteRelationship(ctx context.Context, resourceType, resourceID, relation, subjectType, subjectID string) error {
	return c.writeRel(ctx, "OPERATION_TOUCH", resourceType, resourceID, relation, subjectType, subjectID)
}

// DeleteRelationship removes a single relationship tuple via the write API.
func (c *Client) DeleteRelationship(ctx context.Context, resourceType, resourceID, relation, subjectType, subjectID string) error {
	return c.writeRel(ctx, "OPERATION_DELETE", resourceType, resourceID, relation, subjectType, subjectID)
}

func (c *Client) writeRel(ctx context.Context, op, resourceType, resourceID, relation, subjectType, subjectID string) error {
	req := map[string]any{
		"updates": []relUpdate{{
			Operation:    op,
			Relationship: makeRel(resourceType, resourceID, relation, subjectType, subjectID),
		}},
	}
	body, err := c.post(ctx, "/v1/relationships/write", req)
	if err != nil {
		return fmt.Errorf("relationships write (%s): %w", op, err)
	}
	c.saveToken(body)
	return nil
}

// ReadRelationships returns all matching relationship tuples.
func (c *Client) ReadRelationships(ctx context.Context, resourceType string, resourceID, relation *string) ([]relationship, error) {
	filter := map[string]string{"resourceType": resourceType}
	if resourceID != nil {
		filter["optionalResourceId"] = *resourceID
	}
	if relation != nil {
		filter["optionalRelation"] = *relation
	}
	req := map[string]any{
		"consistency":        c.consistency(),
		"relationshipFilter": filter,
	}
	body, err := c.post(ctx, "/v1/relationships/read", req)
	if err != nil {
		return nil, fmt.Errorf("relationships read: %w", err)
	}
	var resp struct {
		Result struct {
			Relationship relationship `json:"relationship"`
		} `json:"result"`
	}
	// The HTTP API returns newline-delimited JSON objects for streaming.
	var rels []relationship
	for _, line := range splitJSONLines(body) {
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("json.Unmarshal: %w", err)
		}
		rels = append(rels, resp.Result.Relationship)
	}
	return rels, nil
}

// --- Permissions ------------------------------------------------------------

// CheckPermission returns true if subjectType:subjectID has the given
// permission on resourceType:resourceID.
func (c *Client) CheckPermission(ctx context.Context, resourceType, resourceID, permission, subjectType, subjectID string) (bool, error) {
	req := map[string]any{
		"consistency": c.consistency(),
		"resource":    objectRef{ObjectType: resourceType, ObjectID: resourceID},
		"permission":  permission,
		"subject":     subjectRef{Object: objectRef{ObjectType: subjectType, ObjectID: subjectID}},
	}
	body, err := c.post(ctx, "/v1/permissions/check", req)
	if err != nil {
		return false, fmt.Errorf("permissions check: %w", err)
	}
	var resp struct {
		Permissionship string `json:"permissionship"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("json.Unmarshal: %w", err)
	}
	return resp.Permissionship == "PERMISSIONSHIP_HAS_PERMISSION", nil
}

// LookupResources returns all resource IDs of resourceType that subjectID
// can reach via permission.
func (c *Client) LookupResources(ctx context.Context, resourceType, permission, subjectType, subjectID string) ([]string, error) {
	req := map[string]any{
		"consistency":        c.consistency(),
		"resourceObjectType": resourceType,
		"permission":         permission,
		"subject":            subjectRef{Object: objectRef{ObjectType: subjectType, ObjectID: subjectID}},
	}
	body, err := c.post(ctx, "/v1/permissions/resources", req)
	if err != nil {
		return nil, fmt.Errorf("lookup resources: %w", err)
	}
	var ids []string
	for _, line := range splitJSONLines(body) {
		var resp struct {
			Result struct {
				ResourceObjectID string `json:"resourceObjectId"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("json.Unmarshal: %w", err)
		}
		if resp.Result.ResourceObjectID != "" {
			ids = append(ids, resp.Result.ResourceObjectID)
		}
	}
	return ids, nil
}

// LookupSubjects returns all subject IDs of subjectType that have the given
// permission on resourceType:resourceID.
func (c *Client) LookupSubjects(ctx context.Context, resourceType, resourceID, permission, subjectType string) ([]string, error) {
	req := map[string]any{
		"consistency":       c.consistency(),
		"resource":          objectRef{ObjectType: resourceType, ObjectID: resourceID},
		"permission":        permission,
		"subjectObjectType": subjectType,
	}
	body, err := c.post(ctx, "/v1/permissions/subjects", req)
	if err != nil {
		return nil, fmt.Errorf("lookup subjects: %w", err)
	}
	var ids []string
	for _, line := range splitJSONLines(body) {
		var resp struct {
			Result struct {
				SubjectObjectID string `json:"subjectObjectId"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("json.Unmarshal: %w", err)
		}
		if resp.Result.SubjectObjectID != "" {
			ids = append(ids, resp.Result.SubjectObjectID)
		}
	}
	return ids, nil
}

// --- Internals --------------------------------------------------------------

func (c *Client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("http.NewRequestWithContext: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http.Do: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return buf.Bytes(), nil
}

// saveToken extracts and caches the ZedToken from a write response so
// subsequent reads are at least as fresh.
func (c *Client) saveToken(body []byte) {
	var resp struct {
		WrittenAt struct {
			Token string `json:"token"`
		} `json:"writtenAt"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.WrittenAt.Token != "" {
		c.mu.Lock()
		c.zedToken = resp.WrittenAt.Token
		c.mu.Unlock()
	}
}

func (c *Client) consistency() map[string]any {
	c.mu.Lock()
	token := c.zedToken
	c.mu.Unlock()

	if token != "" {
		return map[string]any{"atLeastAsFresh": map[string]string{"token": token}}
	}
	return map[string]any{"fullyConsistent": true}
}

// splitJSONLines splits newline-delimited JSON (SpiceDB streaming responses).
func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
