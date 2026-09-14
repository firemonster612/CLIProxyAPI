package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist Claude OAuth
// credentials. Two enterprise organizations can share one login email, so a
// short hash of the organization UUID (account UUID as fallback) keeps their
// credential files distinct instead of overwriting each other. The legacy
// email-only format remains the fallback for tokens without identity metadata.
func CredentialFileName(email, organizationUUID, accountUUID string) string {
	email = strings.TrimSpace(email)
	seed := strings.TrimSpace(organizationUUID)
	if seed == "" {
		seed = strings.TrimSpace(accountUUID)
	}
	if seed == "" {
		return fmt.Sprintf("claude-%s.json", email)
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("claude-%s-%s.json", hex.EncodeToString(sum[:4]), email)
}
