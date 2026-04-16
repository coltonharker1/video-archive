package mlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client communicates with the Python ML worker over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new ML worker client.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // long timeout for large batches
		},
	}
}

// Health checks if the ML worker is running and models are loaded.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	resp, err := c.get(ctx, "/health")
	if err != nil {
		return nil, fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decoding health response: %w", err)
	}
	return &health, nil
}

// Detect sends frame paths to the ML worker for face detection + embedding.
func (c *Client) Detect(ctx context.Context, framePaths []string) (*DetectResponse, error) {
	req := DetectRequest{FramePaths: framePaths}
	resp, err := c.post(ctx, "/detect", req)
	if err != nil {
		return nil, fmt.Errorf("detect request failed: %w", err)
	}
	defer resp.Body.Close()

	var result DetectResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding detect response: %w", err)
	}
	return &result, nil
}

// Embed sends crop paths to the ML worker for re-embedding.
func (c *Client) Embed(ctx context.Context, cropPaths []string) (*EmbedResponse, error) {
	req := EmbedRequest{CropPaths: cropPaths}
	resp, err := c.post(ctx, "/embed", req)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer resp.Body.Close()

	var result EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding embed response: %w", err)
	}
	return &result, nil
}

// Cluster sends embedding vectors to the ML worker for HDBSCAN clustering.
func (c *Client) Cluster(ctx context.Context, vectors [][]float64, minClusterSize int) (*ClusterResponse, error) {
	req := ClusterRequest{Vectors: vectors, MinClusterSize: minClusterSize}
	resp, err := c.post(ctx, "/cluster", req)
	if err != nil {
		return nil, fmt.Errorf("cluster request failed: %w", err)
	}
	defer resp.Body.Close()

	var result ClusterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding cluster response: %w", err)
	}
	return &result, nil
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}
