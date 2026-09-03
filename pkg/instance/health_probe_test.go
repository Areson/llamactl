package instance

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeHealthSuccess(t *testing.T) {
	// Create a test server that returns 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// Extract host and port from the test server URL
	host, port := parseHostPort(t, server.URL)

	err := probeHealth(host, port, 5)
	if err != nil {
		t.Errorf("probeHealth() should return nil for healthy server, got: %v", err)
	}
}

func TestProbeHealthServerError(t *testing.T) {
	// Create a test server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	host, port := parseHostPort(t, server.URL)

	err := probeHealth(host, port, 5)
	if err == nil {
		t.Error("probeHealth() should return error for 500 response")
	}
}

func TestProbeHealthConnectionRefused(t *testing.T) {
	// Use a port that's not listening
	err := probeHealth("127.0.0.1", 1, 2)
	if err == nil {
		t.Error("probeHealth() should return error for connection refused")
	}
}

func TestProbeHealthBadRequest(t *testing.T) {
	// Create a test server that returns 400
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	host, port := parseHostPort(t, server.URL)

	err := probeHealth(host, port, 5)
	if err == nil {
		t.Error("probeHealth() should return error for 400 response")
	}
}

func TestProbeHealthInvalidPort(t *testing.T) {
	// Port 0 is invalid
	err := probeHealth("127.0.0.1", 0, 5)
	if err == nil {
		t.Error("probeHealth() should return error for port 0")
	}

	// Negative port is invalid
	err = probeHealth("127.0.0.1", -1, 5)
	if err == nil {
		t.Error("probeHealth() should return error for negative port")
	}
}

// parseHostPort extracts host and port from a URL like "http://127.0.0.1:12345"
func parseHostPort(t *testing.T, url string) (string, int) {
	t.Helper()
	
	// Remove the scheme
	if len(url) >= 7 && url[:7] == "http://" {
		url = url[7:]
	}
	
	// Find the colon
	for i := 0; i < len(url); i++ {
		if url[i] == ':' {
			host := url[:i]
			portStr := url[i+1:]
			
			// Port might have a path after it
			for j := 0; j < len(portStr); j++ {
				if portStr[j] == '/' {
					portStr = portStr[:j]
					break
				}
			}
			
			var port int
			for _, c := range portStr {
				if c < '0' || c > '9' {
					t.Fatalf("Invalid port in URL: %s", url)
				}
				port = port*10 + int(c-'0')
			}
			
			return host, port
		}
	}
	
	t.Fatalf("No port found in URL: %s", url)
	return "", 0
}
