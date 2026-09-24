package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

func TestCodexUserAgentFollowsNewerRelease(t *testing.T) {
	restore := misc.CodexRelease.SetForTest("0.160.2")
	defer restore()
	if got := codexUserAgent(); !strings.HasPrefix(got, "codex-tui/0.160.2 ") || !strings.HasSuffix(got, "(codex-tui; 0.160.2)") {
		t.Fatalf("codexUserAgent() = %q, want release 0.160.2", got)
	}

	misc.CodexRelease.SetForTest("0.100.0")
	if got := codexClientVersion(); got != codexPinnedClientVersion {
		t.Fatalf("codexClientVersion() = %q, want the measured %q when npm is older", got, codexPinnedClientVersion)
	}
}
