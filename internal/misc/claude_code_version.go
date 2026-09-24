package misc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	claudeCodeVersionRefreshInterval = time.Hour
	claudeCodeVersionFetchTimeout    = 10 * time.Second
)

var (
	claudeCodeDistTagsURL      = "https://registry.npmjs.org/-/package/@anthropic-ai/claude-code/dist-tags"
	claudeCodeReleasePattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	claudeCodeVersionMu        sync.RWMutex
	claudeCodeLatestRelease    string
	claudeCodeVersionUpdaterOn sync.Once
)

// StartClaudeCodeVersionUpdater tracks the latest Claude Code release published
// on npm so the Claude executor can present it instead of the version compiled
// into this binary. Safe to call multiple times.
func StartClaudeCodeVersionUpdater(ctx context.Context) {
	claudeCodeVersionUpdaterOn.Do(func() {
		go runClaudeCodeVersionUpdater(ctx)
	})
}

func runClaudeCodeVersionUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(claudeCodeVersionRefreshInterval)
	defer ticker.Stop()

	refreshClaudeCodeVersion(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshClaudeCodeVersion(ctx)
		}
	}
}

func refreshClaudeCodeVersion(ctx context.Context) {
	latest, errFetch := fetchClaudeCodeLatestRelease(ctx)
	if errFetch != nil {
		// A stale release is still at least as current as the compiled-in one.
		log.WithError(errFetch).Warn("failed to refresh Claude Code release version, keeping previous value")
		return
	}
	claudeCodeVersionMu.Lock()
	changed := latest != claudeCodeLatestRelease
	claudeCodeLatestRelease = latest
	claudeCodeVersionMu.Unlock()
	if changed {
		log.WithField("version", latest).Info("fetched latest Claude Code release version")
	}
}

func fetchClaudeCodeLatestRelease(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, claudeCodeVersionFetchTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(reqCtx, http.MethodGet, claudeCodeDistTagsURL, nil)
	if errReq != nil {
		return "", errReq
	}
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("claude code dist-tags: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude code dist-tags returned status %d", resp.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if errRead != nil {
		return "", errRead
	}
	return parseClaudeCodeLatestRelease(body)
}

func parseClaudeCodeLatestRelease(body []byte) (string, error) {
	var distTags map[string]string
	if errUnmarshal := json.Unmarshal(body, &distTags); errUnmarshal != nil {
		return "", fmt.Errorf("decode claude code dist-tags: %w", errUnmarshal)
	}
	latest := distTags["latest"]
	if !claudeCodeReleasePattern.MatchString(latest) {
		return "", fmt.Errorf("claude code dist-tags: invalid latest version %q", latest)
	}
	return latest, nil
}

// ClaudeCodeLatestRelease returns the Claude Code version behind the npm
// "latest" dist-tag, which is the channel Claude Code auto-updates from. It is
// empty until the updater has fetched it once.
func ClaudeCodeLatestRelease() string {
	claudeCodeVersionMu.RLock()
	defer claudeCodeVersionMu.RUnlock()
	return claudeCodeLatestRelease
}

// SetClaudeCodeLatestReleaseForTest overrides the tracked release and returns a
// function that restores the previous value. Tests only.
func SetClaudeCodeLatestReleaseForTest(release string) (restore func()) {
	claudeCodeVersionMu.Lock()
	previous := claudeCodeLatestRelease
	claudeCodeLatestRelease = release
	claudeCodeVersionMu.Unlock()
	return func() {
		claudeCodeVersionMu.Lock()
		claudeCodeLatestRelease = previous
		claudeCodeVersionMu.Unlock()
	}
}
