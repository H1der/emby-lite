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

	// Emby 4.10 / Yamby style headers with spaces around '=' and dedicated X-Emby-* headers.
	req2 := httptest.NewRequest(http.MethodGet, "/emby/Items?x-emby-token=ci-tok", nil)
	req2.Header.Set("Authorization", `Emby Client = "Yamby", Device = "Pixel 9", DeviceId = "yamby-123", Version = "1.0.9"`)
	meta2 := ExtractClientMeta(req2)
	if meta2.Client != "Yamby" || meta2.Device != "Pixel 9" || meta2.DeviceID != "yamby-123" || meta2.Version != "1.0.9" || meta2.Token != "ci-tok" {
		t.Fatalf("unexpected yamby meta: %+v", meta2)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/emby/Items?X-Emby-Client=Yamby&X-Emby-Client-Version=1.2.0&X-Emby-Device-Name=AndroidTV&X-Emby-Device-Id=tv-1&ApiKey=tok3", nil)
	meta3 := ExtractClientMeta(req3)
	if meta3.Client != "Yamby" || meta3.Version != "1.2.0" || meta3.Device != "AndroidTV" || meta3.DeviceID != "tv-1" || meta3.Token != "tok3" {
		t.Fatalf("unexpected query-header meta: %+v", meta3)
	}
}
