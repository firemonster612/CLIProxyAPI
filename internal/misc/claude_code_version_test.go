package misc

import "testing"

func TestParseClaudeCodeLatestRelease(t *testing.T) {
	latest, err := parseClaudeCodeLatestRelease([]byte(`{"stable":"2.1.273","next":"2.1.282","latest":"2.1.281"}`))
	if err != nil || latest != "2.1.281" {
		t.Fatalf("latest = %q, err = %v; want 2.1.281", latest, err)
	}
	for _, body := range []string{`{"latest":"2.1.281-beta.1"}`, `{"next":"2.1.282"}`, `not json`} {
		if _, err := parseClaudeCodeLatestRelease([]byte(body)); err == nil {
			t.Errorf("parse(%s) err = nil, want error", body)
		}
	}
}
