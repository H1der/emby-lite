package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("StrongPass#2026")
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if !VerifyPassword("StrongPass#2026", hash) {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword("WrongPass", hash) {
		t.Fatal("expected wrong password to fail")
	}
}

func TestImageTagSigning(t *testing.T) {
	tag := SignImageTag("super-secret", "u1", "item1", "Primary", "abc123")
	if !VerifyImageTag("super-secret", "u1", "item1", "Primary", tag) {
		t.Fatal("expected valid signed tag")
	}
	if VerifyImageTag("super-secret", "u2", "item1", "Primary", tag) {
		t.Fatal("tag should not verify for another user")
	}
}

func TestExtractClientMeta(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Items?api_key=query-tok", nil)
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="VidHub", Device="iPhone", DeviceId="ios-99", Version="1.5", Token="header-tok"`)
	meta := ExtractClientMeta(req)
	if meta.Client != "VidHub" || meta.Device != "iPhone" || meta.DeviceID != "ios-99" || meta.Token != "header-tok" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}
