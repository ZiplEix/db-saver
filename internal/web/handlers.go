package web

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ZiplEix/db-saver/internal/auth"
	"github.com/ZiplEix/db-saver/internal/backup"
	"github.com/ZiplEix/db-saver/internal/config"
	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
	"github.com/ZiplEix/db-saver/internal/notification"
	"github.com/ZiplEix/db-saver/internal/rclone"
	"github.com/ZiplEix/db-saver/internal/scheduler"
)

type Handler struct {
	cfg       *config.Config
	db        *database.DB
	auth      *auth.SessionManager
	dockerCli *docker.DockerClient
	rcloneSvc *rclone.RcloneService
	engine    *backup.Engine
	sched     *scheduler.Scheduler
	notifier  *notification.NotificationService
	pages     map[string]*template.Template
	partials  map[string]*template.Template
}

func NewHandler(
	cfg *config.Config,
	db *database.DB,
	authMgr *auth.SessionManager,
	dockerCli *docker.DockerClient,
	rcloneSvc *rclone.RcloneService,
	engine *backup.Engine,
	sched *scheduler.Scheduler,
	notifier *notification.NotificationService,
) (*Handler, error) {
	h := &Handler{
		cfg:       cfg,
		db:        db,
		auth:      authMgr,
		dockerCli: dockerCli,
		rcloneSvc: rcloneSvc,
		engine:    engine,
		sched:     sched,
		notifier:  notifier,
		pages:     make(map[string]*template.Template),
		partials:  make(map[string]*template.Template),
	}

	if err := h.loadTemplates(); err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}

	return h, nil
}

func (h *Handler) loadTemplates() error {
	funcMap := template.FuncMap{
		"formatBytes": func(b int64) string {
			if b <= 0 {
				return "0 O"
			}
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d O", b)
			}
			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %co", float64(b)/float64(div), "KMGTPE"[exp])
		},
		"formatDuration": func(ms int64) string {
			if ms <= 0 {
				return "0s"
			}
			if ms < 1000 {
				return fmt.Sprintf("%dms", ms)
			}
			return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
		},
		"formatTime": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return "Jamais"
			}
			return t.Format("02/01/2006 15:04:05")
		},
		"presetLabel": func(preset string) string {
			switch preset {
			case "hourly":
				return "Toutes les heures"
			case "every_6h":
				return "Toutes les 6 heures"
			case "every_12h":
				return "Toutes les 12 heures"
			case "daily_3am":
				return "Tous les jours à 03h00"
			case "weekly":
				return "Toutes les semaines (Dimanche)"
			case "custom":
				return "Cron personnalisé"
			default:
				return preset
			}
		},
		"trim": strings.TrimSpace,
	}

	// Base layout
	baseTmpl, err := template.New("layout.html").Funcs(funcMap).ParseFiles("web/templates/layout.html")
	if err != nil {
		return fmt.Errorf("failed to parse layout.html: %w", err)
	}

	// Pages that extend layout
	pages := []string{"dashboard.html", "containers.html", "settings.html"}
	for _, page := range pages {
		clone, err := baseTmpl.Clone()
		if err != nil {
			return err
		}
		t, err := clone.ParseFiles("web/templates/" + page)
		if err != nil {
			return fmt.Errorf("failed to parse page %s: %w", page, err)
		}
		h.pages[page] = t
	}

	// Standalone page
	loginTmpl, err := template.New("login.html").Funcs(funcMap).ParseFiles("web/templates/login.html")
	if err != nil {
		return fmt.Errorf("failed to parse login.html: %w", err)
	}
	h.pages["login.html"] = loginTmpl

	// Partials / Modals
	partials := []string{"job_modal.html", "logs_modal.html", "backups_list.html"}
	for _, part := range partials {
		t, err := template.New(part).Funcs(funcMap).ParseFiles("web/templates/" + part)
		if err != nil {
			return fmt.Errorf("failed to parse partial %s: %w", part, err)
		}
		h.partials[part] = t
	}

	return nil
}

// Render renders a full page with layout
func (h *Handler) renderPage(w http.ResponseWriter, name string, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	// Common navigation/system data
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dockerOk := false
	if h.dockerCli != nil && h.dockerCli.Ping(ctx) == nil {
		dockerOk = true
	}
	data["DockerConnected"] = dockerOk

	remotes, _ := h.rcloneSvc.ListRemotes(ctx)
	data["RcloneRemotesCount"] = len(remotes)
	data["AdminUser"] = h.cfg.AdminUser

	tmpl, ok := h.pages[name]
	if !ok {
		http.Error(w, "Page template not found: "+name, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// If it's a page extending layout, execute "layout.html", otherwise execute the template itself
	execName := "layout.html"
	if name == "login.html" {
		execName = "login.html"
	}

	if err := tmpl.ExecuteTemplate(w, execName, data); err != nil {
		log.Printf("Template render error (%s): %v", name, err)
		http.Error(w, "Erreur de rendu du template: "+err.Error(), http.StatusInternalServerError)
	}
}

// RenderPartial renders an HTML snippet/partial for HTMX requests
func (h *Handler) renderPartial(w http.ResponseWriter, name string, data interface{}) {
	tmpl, ok := h.partials[name]
	if !ok {
		http.Error(w, "Partial template not found: "+name, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Partial render error (%s): %v", name, err)
		http.Error(w, "Erreur de rendu: "+err.Error(), http.StatusInternalServerError)
	}
}

// Auth Handlers
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if h.auth.IsAuthenticated(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		h.renderPage(w, "login.html", nil)
		return
	}

	if r.Method == http.MethodPost {
		username := r.FormValue("username")
		password := r.FormValue("password")

		if h.auth.Authenticate(username, password) {
			h.auth.CreateSession(w)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		h.renderPage(w, "login.html", map[string]interface{}{
			"Error":    "Nom d'utilisateur ou mot de passe incorrect.",
			"Username": username,
		})
	}
}

func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	h.auth.InvalidateSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Dashboard Handler
func (h *Handler) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.db.GetJobs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	history, _ := h.db.GetRecentHistory(10)

	totalJobs := len(jobs)
	activeJobs := 0
	successfulBackups := 0
	failedBackups := 0

	for _, j := range jobs {
		if j.Enabled {
			activeJobs++
		}
		if j.LastRunStatus == "success" {
			successfulBackups++
		} else if j.LastRunStatus == "failed" {
			failedBackups++
		}
	}

	h.renderPage(w, "dashboard.html", map[string]interface{}{
		"ActiveTab":         "dashboard",
		"Jobs":              jobs,
		"RecentHistory":     history,
		"TotalJobs":         totalJobs,
		"ActiveJobs":        activeJobs,
		"SuccessfulBackups": successfulBackups,
		"FailedBackups":     failedBackups,
	})
}

// Containers Handler
func (h *Handler) HandleContainers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var containers []docker.ContainerInfo
	var err error
	if h.dockerCli != nil {
		containers, err = h.dockerCli.ListContainers(ctx)
	}

	h.renderPage(w, "containers.html", map[string]interface{}{
		"ActiveTab":  "containers",
		"Containers": containers,
		"Error":      err,
	})
}

// Jobs Handlers
func (h *Handler) HandleJobNewModal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var containers []docker.ContainerInfo
	if h.dockerCli != nil {
		containers, _ = h.dockerCli.ListContainers(ctx)
	}

	remotes, _ := h.rcloneSvc.ListRemotes(ctx)

	prefillContainerID := r.URL.Query().Get("container_id")
	var prefillContainer *docker.ContainerInfo
	if prefillContainerID != "" && h.dockerCli != nil {
		for i := range containers {
			if containers[i].ID == prefillContainerID || containers[i].ShortID == prefillContainerID {
				prefillContainer = &containers[i]
				break
			}
		}
	}

	h.renderPartial(w, "job_modal.html", map[string]interface{}{
		"IsEdit":           false,
		"Containers":       containers,
		"Remotes":          remotes,
		"PrefillContainer": prefillContainer,
		"Job":              &database.BackupJob{RetentionCount: 7, RetentionDays: 30, SchedulePreset: "daily_3am", ScheduleCron: "0 3 * * *", Enabled: true},
	})
}

func (h *Handler) HandleJobEditModal(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/edit/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var containers []docker.ContainerInfo
	if h.dockerCli != nil {
		containers, _ = h.dockerCli.ListContainers(ctx)
	}
	remotes, _ := h.rcloneSvc.ListRemotes(ctx)

	h.renderPartial(w, "job_modal.html", map[string]interface{}{
		"IsEdit":     true,
		"Job":        job,
		"Containers": containers,
		"Remotes":    remotes,
	})
}

func (h *Handler) HandleJobCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cronExpr, preset := parseScheduleForm(r)
	retentionCount, _ := strconv.Atoi(r.FormValue("retention_count"))
	retentionDays, _ := strconv.Atoi(r.FormValue("retention_days"))
	port, _ := strconv.Atoi(r.FormValue("postgres_port"))
	if port == 0 {
		port = 5432
	}

	job := &database.BackupJob{
		Name:             r.FormValue("name"),
		ContainerID:      r.FormValue("container_id"),
		ContainerName:    r.FormValue("container_name"),
		DBType:           database.DBType(r.FormValue("db_type")),
		ScheduleCron:     cronExpr,
		SchedulePreset:   preset,
		RcloneRemote:     r.FormValue("rclone_remote"),
		RclonePath:       r.FormValue("rclone_path"),
		RetentionCount:   retentionCount,
		RetentionDays:    retentionDays,
		PostgresDB:       r.FormValue("postgres_db"),
		PostgresUser:     r.FormValue("postgres_user"),
		PostgresPassword: r.FormValue("postgres_password"),
		PostgresPort:     port,
		SQLitePath:       r.FormValue("sqlite_path"),
		Enabled:          r.FormValue("enabled") == "on" || r.FormValue("enabled") == "true",
	}

	if err := h.db.CreateJob(job); err != nil {
		http.Error(w, "Erreur création job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = h.sched.SyncJob(job)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleJobUpdate(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cronExpr, preset := parseScheduleForm(r)
	retentionCount, _ := strconv.Atoi(r.FormValue("retention_count"))
	retentionDays, _ := strconv.Atoi(r.FormValue("retention_days"))
	port, _ := strconv.Atoi(r.FormValue("postgres_port"))
	if port == 0 {
		port = 5432
	}

	job.Name = r.FormValue("name")
	job.ContainerID = r.FormValue("container_id")
	job.ContainerName = r.FormValue("container_name")
	job.DBType = database.DBType(r.FormValue("db_type"))
	job.ScheduleCron = cronExpr
	job.SchedulePreset = preset
	job.RcloneRemote = r.FormValue("rclone_remote")
	job.RclonePath = r.FormValue("rclone_path")
	job.RetentionCount = retentionCount
	job.RetentionDays = retentionDays
	job.PostgresDB = r.FormValue("postgres_db")
	job.PostgresUser = r.FormValue("postgres_user")
	job.PostgresPassword = r.FormValue("postgres_password")
	job.PostgresPort = port
	job.SQLitePath = r.FormValue("sqlite_path")
	job.Enabled = r.FormValue("enabled") == "on" || r.FormValue("enabled") == "true"

	if err := h.db.UpdateJob(job); err != nil {
		http.Error(w, "Erreur mise à jour: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = h.sched.SyncJob(job)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleJobDelete(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/delete/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	h.sched.RemoveJob(id)
	_ = h.db.DeleteJob(id)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleJobToggle(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/toggle/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	job.Enabled = !job.Enabled
	_ = h.db.UpdateJob(job)
	_ = h.sched.SyncJob(job)

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleJobRun(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/run/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	// Trigger backup in background
	go func() {
		_, _ = h.engine.RunBackup(context.Background(), id, "manual")
	}()

	// Render logs modal to let client connect immediately via SSE
	h.renderPartial(w, "logs_modal.html", map[string]interface{}{
		"Job": job,
	})
}

func (h *Handler) HandleJobLogsModal(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/logs-modal/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	history, _ := h.db.GetHistoryByJob(id, 5)

	h.renderPartial(w, "logs_modal.html", map[string]interface{}{
		"Job":     job,
		"History": history,
	})
}

// Backups on remote and Restore Handlers
func (h *Handler) HandleJobBackups(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/jobs/backups/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	items, rcloneErr := h.rcloneSvc.ListBackups(ctx, job.RcloneRemote, job.RclonePath)

	h.renderPartial(w, "backups_list.html", map[string]interface{}{
		"Job":       job,
		"Backups":   items,
		"RcloneErr": rcloneErr,
	})
}

func (h *Handler) HandleJobRestore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	jobIDStr := r.FormValue("job_id")
	filename := r.FormValue("filename")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil {
		http.Error(w, "ID invalide", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(jobID)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	// Trigger restore asynchronously
	go func() {
		_ = h.engine.Restore(context.Background(), jobID, filename)
	}()

	h.renderPartial(w, "logs_modal.html", map[string]interface{}{
		"Job": job,
	})
}

func (h *Handler) HandleJobDownload(w http.ResponseWriter, r *http.Request) {
	jobIDStr := r.URL.Query().Get("job_id")
	filename := r.URL.Query().Get("file")
	jobID, err := strconv.ParseInt(jobIDStr, 10, 64)
	if err != nil || filename == "" {
		http.Error(w, "Paramètres invalides", http.StatusBadRequest)
		return
	}

	job, err := h.db.GetJob(jobID)
	if err != nil || job == nil {
		http.Error(w, "Job introuvable", http.StatusNotFound)
		return
	}

	tmpLocalFile := filepath.Join(h.cfg.TmpBackupDir, filename)
	defer os.Remove(tmpLocalFile)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	if err := h.rcloneSvc.Download(ctx, job.RcloneRemote, job.RclonePath, filename, tmpLocalFile, nil); err != nil {
		http.Error(w, "Échec du téléchargement rclone: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, tmpLocalFile)
}

// Settings Handlers
func (h *Handler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	settings, _ := h.db.GetSettings()
	rcloneConfig, _ := h.rcloneSvc.ReadConfigFile()
	remotes, _ := h.rcloneSvc.ListRemotes(r.Context())

	saved := r.URL.Query().Get("saved") == "true"
	rcloneSaved := r.URL.Query().Get("rclone_saved") == "true"

	h.renderPage(w, "settings.html", map[string]interface{}{
		"ActiveTab":        "settings",
		"Settings":         settings,
		"RcloneConfig":     rcloneConfig,
		"RcloneConfigPath": h.rcloneSvc.GetConfigPath(),
		"Remotes":          remotes,
		"Saved":            saved,
		"RcloneSaved":      rcloneSaved,
	})
}

func (h *Handler) HandleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	settings, _ := h.db.GetSettings()
	if settings == nil {
		settings = &database.AppSettings{}
	}

	settings.DiscordWebhookURL = r.FormValue("discord_webhook_url")
	settings.TelegramBotToken = r.FormValue("telegram_bot_token")
	settings.TelegramChatID = r.FormValue("telegram_chat_id")
	settings.GenericWebhookURL = r.FormValue("generic_webhook_url")
	settings.NotifyOnSuccess = r.FormValue("notify_on_success") == "on" || r.FormValue("notify_on_success") == "true"
	settings.NotifyOnFailure = r.FormValue("notify_on_failure") == "on" || r.FormValue("notify_on_failure") == "true"

	_ = h.db.SaveSettings(settings)

	http.Redirect(w, r, "/settings?saved=true", http.StatusSeeOther)
}

func (h *Handler) HandleSaveRcloneConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	content := r.FormValue("rclone_conf")
	if err := h.rcloneSvc.SaveConfigFile(content); err != nil {
		log.Printf("[Rclone Error] Erreur enregistrement rclone.conf: %v", err)
		settings, _ := h.db.GetSettings()
		remotes, _ := h.rcloneSvc.ListRemotes(r.Context())
		h.renderPage(w, "settings.html", map[string]interface{}{
			"ActiveTab":        "settings",
			"Settings":         settings,
			"RcloneConfig":     content,
			"RcloneConfigPath": h.rcloneSvc.GetConfigPath(),
			"Remotes":          remotes,
			"Error":            "Erreur lors de l'enregistrement de rclone.conf : " + err.Error(),
		})
		return
	}

	http.Redirect(w, r, "/settings?rclone_saved=true", http.StatusSeeOther)
}

func (h *Handler) HandleTestNotification(w http.ResponseWriter, r *http.Request) {
	settings, _ := h.db.GetSettings()
	if settings == nil {
		http.Error(w, "Paramètres introuvables", http.StatusBadRequest)
		return
	}

	testJob := &database.BackupJob{
		Name:          "Test Notification",
		ContainerName: "test-container",
		DBType:        database.DBTypePostgres,
		RcloneRemote:  "remote",
		RclonePath:    "backups/test",
	}
	testHistory := &database.BackupHistory{
		Filename:    "test_backup_2026-09-21.sql.gz",
		SizeBytes:   1024 * 1024 * 42, // 42 MB
		DurationMs:  1850,
		TriggerType: "test",
	}

	h.notifier.NotifySuccess(r.Context(), settings, testJob, testHistory)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<div class="toast toast-success">Notification de test envoyée !</div>`)
}

func parseScheduleForm(r *http.Request) (string, string) {
	preset := r.FormValue("schedule_preset")
	switch preset {
	case "hourly":
		return "0 * * * *", preset
	case "every_6h":
		return "0 */6 * * *", preset
	case "every_12h":
		return "0 */12 * * *", preset
	case "daily_3am":
		return "0 3 * * *", preset
	case "weekly":
		return "0 3 * * 0", preset
	case "custom":
		cronVal := r.FormValue("schedule_cron")
		if cronVal == "" {
			cronVal = "0 3 * * *"
		}
		return cronVal, "custom"
	default:
		return "0 3 * * *", "daily_3am"
	}
}
