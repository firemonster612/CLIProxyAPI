package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ParseCodexUsageWindows extracts the ChatGPT Codex primary (short, typically
// 5h) and secondary (long, typically weekly) rate-limit windows from response
// headers. The current backend reports the reset as an absolute Unix timestamp
// in X-Codex-*-Reset-At; the older relative X-Codex-*-Reset-After-Seconds
// spelling is accepted as a fallback. Window lengths arrive in minutes. A
// window whose reset headers are absent or unparseable is returned zeroed.
func ParseCodexUsageWindows(headers http.Header, now time.Time) (primary, secondary cliproxyauth.UsageWindow) {
	primary = parseCodexUsageWindow(headers, "X-Codex-Primary", now)
	secondary = parseCodexUsageWindow(headers, "X-Codex-Secondary", now)
	return primary, secondary
}

func parseCodexUsageWindow(headers http.Header, prefix string, now time.Time) cliproxyauth.UsageWindow {
	if headers == nil {
		return cliproxyauth.UsageWindow{}
	}
	var reset time.Time
	if at := codexHeaderFloat(headers, prefix+"-Reset-At"); at > 0 {
		reset = time.Unix(int64(at), 0)
	} else if seconds := codexHeaderFloat(headers, prefix+"-Reset-After-Seconds"); seconds > 0 {
		reset = now.Add(time.Duration(seconds * float64(time.Second)))
	} else {
		return cliproxyauth.UsageWindow{}
	}
	window := cliproxyauth.UsageWindow{Reset: reset}
	if minutes := codexHeaderFloat(headers, prefix+"-Window-Minutes"); minutes > 0 {
		window.Span = time.Duration(minutes * float64(time.Minute))
	}
	return window
}

// ParseCodexUsageWindowsEvent extracts the same windows from a
// codex.rate_limits websocket event payload. Live payloads spell the absolute
// Unix reset "resets_at"; "reset_at" (codex-rs event struct) and the older
// relative "resets_in_seconds" are accepted as fallbacks.
func ParseCodexUsageWindowsEvent(payload []byte, now time.Time) (primary, secondary cliproxyauth.UsageWindow) {
	if gjson.GetBytes(payload, "type").String() != "codex.rate_limits" {
		return cliproxyauth.UsageWindow{}, cliproxyauth.UsageWindow{}
	}
	primary = parseCodexEventWindow(gjson.GetBytes(payload, "rate_limits.primary"), now)
	secondary = parseCodexEventWindow(gjson.GetBytes(payload, "rate_limits.secondary"), now)
	return primary, secondary
}

func parseCodexEventWindow(window gjson.Result, now time.Time) cliproxyauth.UsageWindow {
	if !window.Exists() {
		return cliproxyauth.UsageWindow{}
	}
	var reset time.Time
	if resetsAt := window.Get("resets_at").Int(); resetsAt > 0 {
		reset = time.Unix(resetsAt, 0)
	} else if resetAt := window.Get("reset_at").Int(); resetAt > 0 {
		reset = time.Unix(resetAt, 0)
	} else if seconds := window.Get("resets_in_seconds").Int(); seconds > 0 {
		reset = now.Add(time.Duration(seconds) * time.Second)
	} else {
		return cliproxyauth.UsageWindow{}
	}
	parsed := cliproxyauth.UsageWindow{Reset: reset}
	if minutes := window.Get("window_minutes").Int(); minutes > 0 {
		parsed.Span = time.Duration(minutes) * time.Minute
	}
	return parsed
}

func codexHeaderFloat(headers http.Header, name string) float64 {
	raw := strings.TrimSpace(getHeaderCaseInsensitive(headers, name))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
