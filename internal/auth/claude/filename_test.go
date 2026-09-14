package claude

import "testing"

func TestCredentialFileNameDistinguishesOrganizations(t *testing.T) {
	first := CredentialFileName("shared@corp.com", "org-alpha", "acct-1")
	second := CredentialFileName("shared@corp.com", "org-beta", "acct-1")
	if first == second {
		t.Fatalf("expected distinct filenames for different organizations, both were %q", first)
	}
}

func TestCredentialFileNameStableForSameIdentity(t *testing.T) {
	first := CredentialFileName("user@example.com", "org-alpha", "")
	second := CredentialFileName("user@example.com", "org-alpha", "")
	if first != second {
		t.Fatalf("expected stable filename, got %q and %q", first, second)
	}
}

func TestCredentialFileNameFallsBackToAccountUUID(t *testing.T) {
	withAccount := CredentialFileName("user@example.com", "", "acct-1")
	legacy := CredentialFileName("user@example.com", "", "")
	if withAccount == legacy {
		t.Fatalf("expected account uuid to contribute to filename, both were %q", withAccount)
	}
	if legacy != "claude-user@example.com.json" {
		t.Fatalf("expected legacy format without identity metadata, got %q", legacy)
	}
}
