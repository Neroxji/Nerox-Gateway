package security

import (
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const tokenPrefix = "sk-"

// ParseBearerToken extracts a token from a standard Authorization header.
func ParseBearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// GenerateToken creates a gateway token using a cryptographically secure source.
func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := cryptoRand.Read(raw); err != nil {
		return "", err
	}
	return tokenPrefix + hex.EncodeToString(raw), nil
}

// HashToken returns the stable, non-reversible representation used for storage.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
