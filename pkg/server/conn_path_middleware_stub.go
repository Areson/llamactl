//go:build !windows

package server

import "net/http"

// ConnPathMiddleware is a no-op on non-Windows platforms because the
// Windows-only hot-swap connection tracker is not present there.
func ConnPathMiddleware(next http.Handler) http.Handler {
	return next
}
