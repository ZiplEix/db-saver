package auth_test

import (
	"net/http/httptest"
	"testing"

	"github.com/ZiplEix/db-saver/internal/auth"
	"github.com/ZiplEix/db-saver/internal/config"
)

func TestAuth(t *testing.T) {
	cfg := &config.Config{
		AdminUser:     "admin",
		AdminPassword: "supersecretpassword",
	}

	sm := auth.NewSessionManager(cfg)

	// Test invalid password
	if sm.Authenticate("admin", "wrongpassword") {
		t.Fatalf("expected authentication to fail with wrong password")
	}

	// Test valid password
	if !sm.Authenticate("admin", "supersecretpassword") {
		t.Fatalf("expected authentication to succeed with correct password")
	}

	// Test session creation and cookie
	rec := httptest.NewRecorder()
	token := sm.CreateSession(rec)
	if token == "" {
		t.Fatalf("expected non-empty session token")
	}

	resp := rec.Result()
	cookies := resp.Cookies()
	if len(cookies) == 0 || cookies[0].Name != "db_saver_session" {
		t.Fatalf("expected db_saver_session cookie in response")
	}

	// Test IsAuthenticated with cookie
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookies[0])

	if !sm.IsAuthenticated(req) {
		t.Fatalf("expected request to be authenticated")
	}

	// Test InvalidateSession
	rec2 := httptest.NewRecorder()
	sm.InvalidateSession(rec2, req)

	if sm.IsAuthenticated(req) {
		t.Fatalf("expected request to NOT be authenticated after invalidation")
	}
}
