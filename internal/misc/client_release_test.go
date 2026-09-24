package misc

import "testing"

func TestParseLatestRelease(t *testing.T) {
	for body, want := range map[string]string{
		`{"stable":"2.1.273","next":"2.1.282","latest":"2.1.281"}`:                 "2.1.281",
		`{"alpha":"0.158.0-alpha.9","native":"0.1.2505291658","latest":"0.156.1"}`: "0.156.1",
	} {
		if latest, err := parseLatestRelease([]byte(body)); err != nil || latest != want {
			t.Errorf("parse(%s) = %q, %v; want %q", body, latest, err, want)
		}
	}
	for _, body := range []string{`{"latest":"2.1.281-beta.1"}`, `{"next":"2.1.282"}`, `not json`} {
		if _, err := parseLatestRelease([]byte(body)); err == nil {
			t.Errorf("parse(%s) err = nil, want error", body)
		}
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.156.1", "0.154.0", 1},
		{"0.154.0", "0.154.0", 0},
		{"0.99.0", "0.154.0", -1},
		{"bad", "0.154.0", -1},
	}
	for _, tc := range cases {
		if got := CompareReleaseVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareReleaseVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
