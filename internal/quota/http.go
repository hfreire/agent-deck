package quota

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpTimeout bounds a single usage request. The poller runs off the render
// path, but a hung request would still pin a goroutine per tick.
const httpTimeout = 10 * time.Second

// maxResponseBytes caps what we read from an endpoint we do not control.
const maxResponseBytes = 1 << 20

func defaultHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: httpTimeout}
}

func requestJSON(ctx context.Context, client *http.Client, method, url string, body io.Reader, headers map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := defaultHTTPClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}
	return data, nil
}
