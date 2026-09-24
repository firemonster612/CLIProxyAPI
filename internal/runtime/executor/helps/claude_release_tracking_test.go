package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

func TestWithLatestClaudeReleaseAdvancesBaseline(t *testing.T) {
	pinned := pinnedClaudeDeviceProfile(nil)

	got := withLatestClaudeRelease(pinned, "2.3.0")
	if got.UserAgent != "claude-cli/2.3.0 (external, cli)" {
		t.Fatalf("UserAgent = %q, want the latest release", got.UserAgent)
	}
	if got.PackageVersion != pinned.PackageVersion || got.RuntimeVersion != pinned.RuntimeVersion {
		t.Fatalf("software tuple = %s/%s, want the measured %s/%s", got.PackageVersion, got.RuntimeVersion, pinned.PackageVersion, pinned.RuntimeVersion)
	}

	for _, release := range []string{"", "2.1.100", "not-a-version"} {
		if got := withLatestClaudeRelease(pinned, release); got.UserAgent != pinned.UserAgent {
			t.Errorf("release %q: UserAgent = %q, want pinned %q", release, got.UserAgent, pinned.UserAgent)
		}
	}

	legacy := pinnedClaudeDeviceProfile(&config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent: "claude-cli/2.1.220 (external, cli)",
	}})
	if got := withLatestClaudeRelease(legacy, "2.3.0"); got.UserAgent != legacy.UserAgent {
		t.Fatalf("legacy baseline UserAgent = %q, want it kept at 2.1.220", got.UserAgent)
	}
}

func TestPlausibleClaudeCLIVersionAcceptsUpToLatestRelease(t *testing.T) {
	baseline := claudeCLIVersion{2, 1, 258}
	latest := claudeCLIVersion{2, 2, 3}
	cases := []struct {
		candidate claudeCLIVersion
		want      bool
	}{
		{claudeCLIVersion{2, 1, 257}, false},
		{claudeCLIVersion{2, 1, 300}, true},
		{claudeCLIVersion{2, 2, 0}, true},
		{claudeCLIVersion{2, 2, 3}, true},
		{claudeCLIVersion{2, 2, 4}, false},
		{claudeCLIVersion{3, 0, 0}, false},
	}
	for _, tc := range cases {
		if got := plausibleClaudeCLIVersion(tc.candidate, baseline, latest); got != tc.want {
			t.Errorf("plausible(%v) = %v, want %v", tc.candidate, got, tc.want)
		}
	}
}

func TestRecordClaudePackageVersionKeepsFirstObservationOnLatestRelease(t *testing.T) {
	reset := func() {
		claudeObservedPackageMu.Lock()
		claudeObservedPackageRelease, claudeObservedPackageVersion = claudeCLIVersion{}, ""
		claudeObservedPackageMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	restore := misc.SetClaudeCodeLatestReleaseForTest("2.3.0")
	t.Cleanup(restore)
	pinned := pinnedClaudeDeviceProfile(nil)

	RecordClaudePackageVersion("claude-cli/2.2.9 (external, cli)", "0.117.0")
	RecordClaudePackageVersion("claude-cli/2.3.0 (external, cli)", "not-a-version")
	if got := withLatestClaudeRelease(pinned, "2.3.0"); got.PackageVersion != pinned.PackageVersion {
		t.Fatalf("PackageVersion = %q, want pinned until a well-formed latest-release client is seen", got.PackageVersion)
	}

	RecordClaudePackageVersion("claude-cli/2.3.0 (external, cli)", "0.118.0")
	RecordClaudePackageVersion("claude-cli/2.3.0 (external, cli)", "9.9.9")
	got := withLatestClaudeRelease(pinned, "2.3.0")
	if got.PackageVersion != "0.118.0" {
		t.Fatalf("PackageVersion = %q, want the first observation 0.118.0", got.PackageVersion)
	}
	if got.RuntimeVersion != pinned.RuntimeVersion {
		t.Fatalf("RuntimeVersion = %q, want pinned %q", got.RuntimeVersion, pinned.RuntimeVersion)
	}
}
