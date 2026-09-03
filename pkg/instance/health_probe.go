package instance

import (
	"fmt"
	"net/http"
	"time"
)

// probeHealth checks the /health endpoint of a model instance.
// Returns nil if the endpoint responds with 200 within the timeout.
func probeHealth(host string, port int, timeoutSec int) error {
	if port <= 0 {
		return fmt.Errorf("invalid port: %d", port)
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
	}

	healthURL := fmt.Sprintf("http://%s:%d/health", host, port)

	resp, err := client.Get(healthURL)
	if err != nil {
		return fmt.Errorf("health probe to %s failed: %w", healthURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health probe to %s returned %d", healthURL, resp.StatusCode)
	}

	return nil
}
