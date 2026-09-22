package claude

import (
	"encoding/json"
	"testing"
)

func TestNextBankedResetGrantID(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{
			name:   "server-designated next grant wins",
			status: `{"next_grant_id":"grant-a","grants":[{"id":"grant-b","resets_left":1,"usable_now":true}]}`,
			want:   "grant-a",
		},
		{
			name:   "falls back to first usable grant with resets left",
			status: `{"grants":[{"id":"spent","resets_left":0,"usable_now":true},{"id":"paused","resets_left":1,"usable_now":true,"paused":true},{"id":"good","resets_left":1,"usable_now":true}]}`,
			want:   "good",
		},
		{
			name:   "nothing to spend",
			status: `{"eligible":true,"grants":[{"id":"later","resets_left":1,"usable_now":false}]}`,
			want:   "",
		},
		{
			name:   "null status",
			status: `null`,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextBankedResetGrantID(json.RawMessage(tt.status)); got != tt.want {
				t.Fatalf("NextBankedResetGrantID() = %q, want %q", got, tt.want)
			}
		})
	}
}
