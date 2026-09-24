package misc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	clientReleaseRefreshInterval = time.Hour
	clientReleaseFetchTimeout    = 10 * time.Second
)

var (
	clientReleasePattern     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	clientReleaseUpdaterOnce sync.Once
)

// ClientRelease tracks the version behind the npm "latest" dist-tag of a coding
// agent CLI. That is the channel the CLI auto-updates from, and providers gate
// new models on recent client versions, so executors present this release
// instead of the version compiled into this binary.
type ClientRelease struct {
	name    string
	npmPkg  string
	mu      sync.RWMutex
	release string
}

var (
	// ClaudeCodeRelease tracks Claude Code.
	ClaudeCodeRelease = &ClientRelease{name: "Claude Code", npmPkg: "@anthropic-ai/claude-code"}
	// CodexRelease tracks the Codex CLI.
	CodexRelease = &ClientRelease{name: "Codex", npmPkg: "@openai/codex"}

	trackedClientReleases = []*ClientRelease{ClaudeCodeRelease, CodexRelease}
)

// Latest returns the latest published release, or "" until it has been
// fetched once.
func (c *ClientRelease) Latest() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.release
}

// SetForTest overrides the tracked release and returns a function that
// restores the previous value. Tests only.
func (c *ClientRelease) SetForTest(release string) (restore func()) {
	c.mu.Lock()
	previous := c.release
	c.release = release
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.release = previous
		c.mu.Unlock()
	}
}

// StartClientReleaseUpdater refreshes every tracked client release now and
// then hourly. Safe to call multiple times.
func StartClientReleaseUpdater(ctx context.Context) {
	clientReleaseUpdaterOnce.Do(func() {
		go runClientReleaseUpdater(ctx)
	})
}

func runClientReleaseUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(clientReleaseRefreshInterval)
	defer ticker.Stop()
	for {
		for _, client := range trackedClientReleases {
			client.refresh(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *ClientRelease) refresh(ctx context.Context) {
	release, errFetch := c.fetch(ctx)
	if errFetch != nil {
		// A stale release is still at least as current as the compiled-in one.
		log.WithError(errFetch).WithField("client", c.name).Warn("failed to refresh client release version, keeping previous value")
		return
	}
	c.mu.Lock()
	changed := release != c.release
	c.release = release
	c.mu.Unlock()
	if changed {
		log.WithFields(log.Fields{"client": c.name, "version": release}).Info("fetched latest client release version")
	}
}

func (c *ClientRelease) fetch(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, clientReleaseFetchTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://registry.npmjs.org/-/package/"+c.npmPkg+"/dist-tags", nil)
	if errReq != nil {
		return "", errReq
	}
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("%s dist-tags: close response body: %v", c.npmPkg, errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s dist-tags returned status %d", c.npmPkg, resp.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if errRead != nil {
		return "", errRead
	}
	return parseLatestRelease(body)
}

func parseLatestRelease(body []byte) (string, error) {
	var distTags map[string]string
	if errUnmarshal := json.Unmarshal(body, &distTags); errUnmarshal != nil {
		return "", fmt.Errorf("decode dist-tags: %w", errUnmarshal)
	}
	latest := distTags["latest"]
	if !clientReleasePattern.MatchString(latest) {
		return "", fmt.Errorf("dist-tags: invalid latest version %q", latest)
	}
	return latest, nil
}

// CompareReleaseVersions compares two MAJOR.MINOR.PATCH versions numerically,
// returning -1, 0 or 1. A malformed version sorts below every valid one.
func CompareReleaseVersions(a, b string) int {
	pa, okA := parseReleaseVersion(a)
	pb, okB := parseReleaseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parseReleaseVersion(version string) ([3]int, bool) {
	var parsed [3]int
	if !clientReleasePattern.MatchString(version) {
		return parsed, false
	}
	for i, part := range strings.Split(version, ".") {
		value, errAtoi := strconv.Atoi(part)
		if errAtoi != nil {
			return parsed, false
		}
		parsed[i] = value
	}
	return parsed, true
}
