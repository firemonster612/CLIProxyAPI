package handlers

import (
	"net/http"
	"strings"
)

// gatewayHeaderPrefixes lists header name prefixes injected by known AI gateway
// proxies. Claude Code's client-side telemetry detects these and reports the
// gateway type, so we strip them from upstream responses to avoid detection.
var gatewayHeaderPrefixes = []string{
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
}

// hopByHopHeaders lists RFC 7230 Section 6.1 hop-by-hop headers that MUST NOT
// be forwarded by proxies, plus security-sensitive headers that should not leak.
var hopByHopHeaders = map[string]struct{}{
	// RFC 7230 hop-by-hop
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	// Security-sensitive
	"Set-Cookie": {},
	// CPA-managed (set by handlers, not upstream)
	"Content-Length":   {},
	"Content-Encoding": {},
}

var cpaReservedResponseHeaders = map[string]struct{}{
	"Access-Control-Allow-Credentials": {},
	"Access-Control-Allow-Headers":     {},
	"Access-Control-Allow-Methods":     {},
	"Access-Control-Allow-Origin":      {},
	"Access-Control-Expose-Headers":    {},
	"Access-Control-Max-Age":           {},
	"X-Cpa-Trace-Id":                   {},
}

// IsCPAReservedResponseHeader reports whether a downstream response header is managed by CPA.
func IsCPAReservedResponseHeader(name string) bool {
	_, reserved := cpaReservedResponseHeaders[http.CanonicalHeaderKey(name)]
	return reserved
}

// FilterUpstreamHeaders returns a copy of src with hop-by-hop and security-sensitive
// headers removed. Returns nil if src is nil or empty after filtering.
func FilterUpstreamHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	connectionScoped := connectionScopedHeaders(src)
	dst := make(http.Header)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, blocked := hopByHopHeaders[canonicalKey]; blocked {
			continue
		}
		if _, reserved := cpaReservedResponseHeaders[canonicalKey]; reserved {
			continue
		}
		if _, scoped := connectionScoped[canonicalKey]; scoped {
			continue
		}
		// Strip headers injected by known AI gateway proxies to avoid
		// Claude Code client-side gateway detection.
		lowerKey := strings.ToLower(key)
		gatewayMatch := false
		for _, prefix := range gatewayHeaderPrefixes {
			if strings.HasPrefix(lowerKey, prefix) {
				gatewayMatch = true
				break
			}
		}
		if gatewayMatch {
			continue
		}
		dst[key] = values
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

// filterCodexUsageHeaders keeps the account quota snapshot Codex clients use
// to publish account/rateLimits/updated notifications. These values describe
// the credential selected for this response and contain no authentication
// material.
func filterCodexUsageHeaders(src http.Header) http.Header {
	var dst http.Header
	for key, values := range src {
		if !isCodexUsageHeader(key) {
			continue
		}
		if dst == nil {
			dst = make(http.Header)
		}
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func isCodexUsageHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "x-codex-active-limit" || lower == "x-codex-plan-type" ||
		strings.HasPrefix(lower, "x-codex-credits-") {
		return true
	}
	if !strings.HasPrefix(lower, "x-codex-") {
		return false
	}
	for _, marker := range []string{
		"-allowed",
		"-limit-reached",
		"-limit-name",
		"-used-percent",
		"-window-minutes",
		"-reset-after-seconds",
		"-reset-at",
		"-over-secondary-limit-percent",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func connectionScopedHeaders(src http.Header) map[string]struct{} {
	scoped := make(map[string]struct{})
	for _, rawValue := range src.Values("Connection") {
		for _, token := range strings.Split(rawValue, ",") {
			headerName := strings.TrimSpace(token)
			if headerName == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
	}
	return scoped
}

// WriteUpstreamHeaders writes filtered upstream headers to the gin response writer.
// Headers already set by CPA (e.g., Content-Type) are NOT overwritten.
func WriteUpstreamHeaders(dst http.Header, src http.Header) {
	if src == nil {
		return
	}
	for key, values := range src {
		// Don't overwrite headers already set by CPA handlers
		if dst.Get(key) != "" {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}
