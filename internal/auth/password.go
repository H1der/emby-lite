package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLen     = 16
	argonKeyLen      = 32
)

// HashPassword hashes a plaintext password using argon2id in PHC string format.
func HashPassword(password string) (string, error) {
	if len(password) < 4 {
		return "", errors.New("password must be at least 4 characters")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword compares a plaintext password against a stored argon2id PHC hash.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem, iter uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &par); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iter, mem, par, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// GenerateToken generates a 32-byte cryptographically secure hex token and its SHA-256 hash.
func GenerateToken() (plain string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(buf)
	hash = HashToken(plain)
	return plain, hash, nil
}

// HashToken returns the SHA-256 hex digest of a bearer/Emby token or API key.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SignImageTag produces a user-scoped HMAC tag so Emby clients can fetch images with tag verification.
func SignImageTag(secret, userID, itemID, kind, rawTag string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	mac.Write([]byte("|"))
	mac.Write([]byte(itemID))
	mac.Write([]byte("|"))
	mac.Write([]byte(strings.ToLower(kind)))
	mac.Write([]byte("|"))
	mac.Write([]byte(rawTag))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]
	if rawTag == "" {
		rawTag = "0"
	}
	return rawTag + "." + sig
}

// VerifyImageTag verifies a signed tag for a given user/item/kind.
func VerifyImageTag(secret, userID, itemID, kind, signedTag string) bool {
	idx := strings.LastIndex(signedTag, ".")
	if idx <= 0 || idx >= len(signedTag)-1 {
		return false
	}
	rawTag := signedTag[:idx]
	expected := SignImageTag(secret, userID, itemID, kind, rawTag)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signedTag)) == 1
}
