package emby

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type cursorPayload struct {
	Offset    int    `json:"o"`
	Limit     int    `json:"l"`
	ScopeHash string `json:"s"`
	Exp       int64  `json:"e"`
}

func cursorScopeHash(userID, parentID, searchTerm, includeTypes, sortBy, sortOrder, filters string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		userID, parentID, strings.ToLower(searchTerm), includeTypes,
		strings.ToLower(sortBy), strings.ToLower(sortOrder), strings.ToLower(filters),
	}, "|")))
	return hex.EncodeToString(sum[:8])
}

func signCursor(secret string, p cursorPayload) string {
	raw, _ := json.Marshal(p)
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b64))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]
	return b64 + "." + sig
}

func verifyCursor(secret, token, expectedScope string) (*cursorPayload, error) {
	idx := strings.LastIndex(token, ".")
	if idx <= 0 || idx >= len(token)-1 {
		return nil, errors.New("malformed cursor")
	}
	b64 := token[:idx]
	sig := token[idx+1:]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b64))
	expectedSig := hex.EncodeToString(mac.Sum(nil))[:16]
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return nil, errors.New("invalid cursor signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("corrupt cursor payload")
	}
	var p cursorPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("invalid cursor json")
	}
	if time.Now().Unix() > p.Exp {
		return nil, errors.New("cursor expired")
	}
	if p.ScopeHash != expectedScope {
		return nil, errors.New("cursor does not match query scope")
	}
	if p.Offset < 0 || p.Limit <= 0 {
		return nil, errors.New("invalid cursor bounds")
	}
	return &p, nil
}
