package management

import (
	"encoding/base64"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func codexAuthWithClaims(payload string) *coreauth.Auth {
	idToken := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	return &coreauth.Auth{Provider: "codex", Metadata: map[string]any{"id_token": idToken}}
}

func TestExtractCodexIDTokenClaimsOrganizationTitle(t *testing.T) {
	cases := []struct {
		name          string
		accountID     string
		organizations string
		want          string
	}{
		{
			name:          "organization matching the account id wins over default",
			accountID:     "org-1",
			organizations: `[{"id":"org-1","is_default":false,"title":"Active Team"},{"id":"org-2","is_default":true,"title":"Default Org"}]`,
			want:          "Active Team",
		},
		{
			name:          "default organization when no organization matches the account id",
			accountID:     "acct-unknown",
			organizations: `[{"id":"org-1","is_default":false,"title":"Personal"},{"id":"org-2","is_default":true,"title":"Acme Team"}]`,
			want:          "Acme Team",
		},
		{
			name:          "default organization wins over first",
			organizations: `[{"id":"org-1","is_default":false,"title":"Personal"},{"id":"org-2","is_default":true,"title":"Acme Team"}]`,
			want:          "Acme Team",
		},
		{
			name:          "first organization when none is default",
			organizations: `[{"id":"org-1","is_default":false,"title":"First Org"},{"id":"org-2","is_default":false,"title":"Second Org"}]`,
			want:          "First Org",
		},
		{
			name:          "omitted without organizations",
			organizations: `[]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := codexAuthWithClaims(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + tc.accountID + `","chatgpt_plan_type":"team","organizations":` + tc.organizations + `}}`)

			claims := extractCodexIDTokenClaims(auth)

			got, present := claims["organization_title"]
			if tc.want == "" {
				if present {
					t.Fatalf("organization_title = %v, want omitted", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("organization_title = %v, want %q", got, tc.want)
			}
		})
	}
}
