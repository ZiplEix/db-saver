package web

import (
	"net/http"
	"strings"
)

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Static assets
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// Auth routes (public)
	mux.HandleFunc("/login", h.HandleLogin)
	mux.HandleFunc("/logout", h.HandleLogout)

	// Protected routes wrapper
	protected := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, h.auth.Middleware(handler))
	}

	// Dashboard
	protected("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		h.HandleDashboard(w, r)
	})

	// Containers
	protected("/containers", h.HandleContainers)

	// Jobs
	protected("/jobs/new", h.HandleJobNewModal)
	protected("/jobs/edit/", h.HandleJobEditModal)
	protected("/jobs/delete/", h.HandleJobDelete)
	protected("/jobs/toggle/", h.HandleJobToggle)
	protected("/jobs/run/", h.HandleJobRun)
	protected("/jobs/logs-modal/", h.HandleJobLogsModal)
	protected("/jobs/backups/", h.HandleJobBackups)
	protected("/jobs/restore", h.HandleJobRestore)
	protected("/jobs/download", h.HandleJobDownload)

	// SSE stream for real-time logs and Job POSTs
	jobsHandler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs/stream") {
			// SSE log streaming
			if !h.auth.IsAuthenticated(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			h.HandleSSELogs(w, r)
			return
		}

		if r.Method == http.MethodPost {
			if !h.auth.IsAuthenticated(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/jobs" || r.URL.Path == "/jobs/" {
				h.HandleJobCreate(w, r)
				return
			}
			h.HandleJobUpdate(w, r)
			return
		}

		http.NotFound(w, r)
	}

	mux.HandleFunc("/jobs", jobsHandler)
	mux.HandleFunc("/jobs/", jobsHandler)

	// Settings
	protected("/settings", h.HandleSettings)
	protected("/settings/save", h.HandleSaveSettings)
	protected("/settings/rclone", h.HandleSaveRcloneConfig)
	protected("/settings/test-notification", h.HandleTestNotification)

	return mux
}
