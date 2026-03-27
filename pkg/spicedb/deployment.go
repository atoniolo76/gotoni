package spicedb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modal-labs/libmodal/modal-go"
)

// DeploySpicedb starts a short-lived Modal sandbox (demo HTTP server) and verifies connect-token access.
// For production SpiceDB, switch the image and Command to spicedb serve (see commented block below).
func DeploySpicedb(presharedKey string) (*modal.Sandbox, error) {
	_ = presharedKey // used when deploying SpiceDB image instead of the demo server

	ctx := context.Background()

	client, err := modal.NewClient()
	if err != nil {
		return nil, fmt.Errorf("modal.NewClient: %w", err)
	}

	app, err := client.Apps.FromName(ctx, "gotoni-spicedb", &modal.AppFromNameParams{
		CreateIfMissing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("Apps.FromName: %w", err)
	}

	image := client.Images.FromRegistry("python:3.11-slim", nil)

	// One argv slice = one process: bash -c runs a single shell string (see libmodal sandbox-connect-token example).
	sb, err := client.Sandboxes.Create(ctx, app, image, &modal.SandboxCreateParams{
		Command:          []string{"bash", "-c", "python3 -m http.server 8080"},
		Timeout:          24 * time.Hour,
		UnencryptedPorts: []int{8080},
	})
	if err != nil {
		return nil, fmt.Errorf("Sandboxes.Create: %w", err)
	}

	creds, err := sb.CreateConnectToken(ctx, &modal.SandboxCreateConnectTokenParams{})
	if err != nil {
		return nil, fmt.Errorf("CreateConnectToken: %w", err)
	}

	// Only the hostname part is valid for DNS — do not paste fmt %v of structs or print(*Body) into the browser.
	fmt.Printf("connect URL: %s\n", creds.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, creds.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", creds.Token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to connect URL: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	fmt.Printf("connect probe: status=%d body_len=%d\n", resp.StatusCode, len(body))

	return sb, nil
}
