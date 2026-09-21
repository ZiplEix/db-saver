package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) HandleSSELogs(w http.ResponseWriter, r *http.Request) {
	// Parse job ID from URL query or path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var jobID int64
	if len(pathParts) >= 3 && pathParts[0] == "jobs" && pathParts[2] == "logs" {
		id, err := strconv.ParseInt(pathParts[1], 10, 64)
		if err == nil {
			jobID = id
		}
	}

	if jobID == 0 {
		http.Error(w, "Invalid job ID", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub, unsubscribe := h.engine.SubscribeLogs(jobID)
	defer unsubscribe()

	// Send initial connection event
	fmt.Fprintf(w, "event: message\ndata: <div class=\"log-line\">Connexion à la console de logs du job #%d établie...</div>\n\n", jobID)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub:
			if !ok {
				return
			}
			// Format log line nicely with colors depending on content
			lineClass := "log-info"
			if strings.Contains(msg, "ERREUR") || strings.Contains(msg, "failed") || strings.Contains(msg, "Error") {
				lineClass = "log-error"
			} else if strings.Contains(msg, "succès") || strings.Contains(msg, "réussi") {
				lineClass = "log-success"
			} else if strings.Contains(msg, "Retention") {
				lineClass = "log-warning"
			}

			html := fmt.Sprintf("<div class=\"log-line %s\">%s</div>", lineClass, msg)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", html)
			flusher.Flush()
		}
	}
}
