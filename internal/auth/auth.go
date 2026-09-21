package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/ZiplEix/db-saver/internal/config"
)

type SessionManager struct {
	cfg      *config.Config
	sessions map[string]time.Time // token -> expiration
	mu       sync.RWMutex
}

func NewSessionManager(cfg *config.Config) *SessionManager {
	sm := &SessionManager{
		cfg:      cfg,
		sessions: make(map[string]time.Time),
	}

	// Periodic cleanup of expired sessions
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		for range ticker.C {
			sm.cleanup()
		}
	}()

	return sm
}

func (sm *SessionManager) Authenticate(user, pass string) bool {
	// If AdminUser is specified, check both, otherwise just check pass
	if sm.cfg.AdminUser != "" && user != sm.cfg.AdminUser {
		return false
	}
	return pass == sm.cfg.AdminPassword
}

func (sm *SessionManager) CreateSession(w http.ResponseWriter) string {
	bytes := make([]byte, 32)
	_, _ = rand.Read(bytes)
	token := hex.EncodeToString(bytes)

	sm.mu.Lock()
	sm.sessions[token] = time.Now().Add(24 * 7 * time.Hour) // 7 days session
	sm.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "db_saver_session",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * 7 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return token
}

func (sm *SessionManager) InvalidateSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("db_saver_session")
	if err == nil {
		sm.mu.Lock()
		delete(sm.sessions, cookie.Value)
		sm.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "db_saver_session",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
	})
}

func (sm *SessionManager) IsAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("db_saver_session")
	if err != nil || cookie.Value == "" {
		return false
	}

	sm.mu.RLock()
	exp, exists := sm.sessions[cookie.Value]
	sm.mu.RUnlock()

	if !exists {
		return false
	}

	if time.Now().After(exp) {
		sm.mu.Lock()
		delete(sm.sessions, cookie.Value)
		sm.mu.Unlock()
		return false
	}

	return true
}

func (sm *SessionManager) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sm.IsAuthenticated(r) {
			// If HTMX request, ask client to redirect via HX-Redirect
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (sm *SessionManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	for token, exp := range sm.sessions {
		if now.After(exp) {
			delete(sm.sessions, token)
		}
	}
}
