package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ZiplEix/db-saver/internal/config"
	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
	"github.com/ZiplEix/db-saver/internal/notification"
	"github.com/ZiplEix/db-saver/internal/rclone"
)

type LogSubscriber chan string

type Engine struct {
	cfg         *config.Config
	db          *database.DB
	dockerCli   *docker.DockerClient
	rcloneSvc   *rclone.RcloneService
	notifier    *notification.NotificationService
	subscribers map[int64][]LogSubscriber // jobID -> list of channels
	subMu       sync.RWMutex
}

func NewEngine(
	cfg *config.Config,
	db *database.DB,
	dockerCli *docker.DockerClient,
	rcloneSvc *rclone.RcloneService,
	notifier *notification.NotificationService,
) *Engine {
	return &Engine{
		cfg:         cfg,
		db:          db,
		dockerCli:   dockerCli,
		rcloneSvc:   rcloneSvc,
		notifier:    notifier,
		subscribers: make(map[int64][]LogSubscriber),
	}
}

func (e *Engine) SubscribeLogs(jobID int64) (LogSubscriber, func()) {
	ch := make(LogSubscriber, 50)
	e.subMu.Lock()
	e.subscribers[jobID] = append(e.subscribers[jobID], ch)
	e.subMu.Unlock()

	unsubscribe := func() {
		e.subMu.Lock()
		defer e.subMu.Unlock()
		subs := e.subscribers[jobID]
		for i, sub := range subs {
			if sub == ch {
				e.subscribers[jobID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}

	return ch, unsubscribe
}

func (e *Engine) broadcastLog(jobID int64, message string) {
	e.subMu.RLock()
	subs := e.subscribers[jobID]
	e.subMu.RUnlock()

	for _, sub := range subs {
		select {
		case sub <- message:
		default:
		}
	}
}

type sseWriter struct {
	jobID  int64
	engine *Engine
	buf    *bytes.Buffer
}

func (w *sseWriter) Write(p []byte) (n int, err error) {
	lines := strings.Split(string(p), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			w.engine.broadcastLog(w.jobID, line)
		}
	}
	return w.buf.Write(p)
}

func (e *Engine) RunBackup(ctx context.Context, jobID int64, triggerType string) (*database.BackupHistory, error) {
	job, err := e.db.GetJob(jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to load job: %w", err)
	}
	if job == nil {
		return nil, fmt.Errorf("job %d not found", jobID)
	}

	startTime := time.Now()
	var logBuf bytes.Buffer
	writer := &sseWriter{
		jobID:  jobID,
		engine: e,
		buf:    &logBuf,
	}

	// Determine file extension
	ext := ".sql.gz"
	if job.DBType == database.DBTypeSQLite {
		ext = ".sqlite.gz"
	}

	safeName := sanitizeFilename(job.Name)
	timestampStr := startTime.Format("2006-01-02_15-04-05")
	filename := fmt.Sprintf("%s_%s_%s%s", safeName, job.DBType, timestampStr, ext)
	localPath := filepath.Join(e.cfg.TmpBackupDir, filename)

	fmt.Fprintf(writer, "[%s] Démarrage du backup '%s' (Déclenchement: %s)\n", time.Now().Format("15:04:05"), job.Name, triggerType)
	_ = e.db.UpdateJobRunStatus(job.ID, "running", 0, 0)

	history := &database.BackupHistory{
		JobID:       job.ID,
		JobName:     job.Name,
		Status:      "running",
		Filename:    filename,
		TriggerType: triggerType,
	}
	_ = e.db.CreateHistory(history)

	var backupErr error
	if job.DBType == database.DBTypePostgres {
		backupErr = BackupPostgres(ctx, e.dockerCli, job, localPath, writer)
	} else if job.DBType == database.DBTypeSQLite {
		backupErr = BackupSQLite(ctx, e.dockerCli, job, localPath, writer)
	} else {
		backupErr = fmt.Errorf("type de base non supporté: %s", job.DBType)
	}

	if backupErr != nil {
		return e.handleFailure(ctx, job, history, backupErr, writer, startTime)
	}

	// Check file size
	fi, err := os.Stat(localPath)
	if err != nil {
		return e.handleFailure(ctx, job, history, fmt.Errorf("impossible de lire le fichier de sauvegarde: %w", err), writer, startTime)
	}
	history.SizeBytes = fi.Size()
	fmt.Fprintf(writer, "[%s] Archive locale générée : %s (%.2f Mo)\n", time.Now().Format("15:04:05"), filename, float64(history.SizeBytes)/(1024*1024))

	// Step 2: Upload to rclone
	fmt.Fprintf(writer, "[%s] Envoi vers rclone (%s:%s)...\n", time.Now().Format("15:04:05"), job.RcloneRemote, job.RclonePath)
	if err := e.rcloneSvc.Upload(ctx, localPath, job.RcloneRemote, job.RclonePath, filename, writer); err != nil {
		return e.handleFailure(ctx, job, history, fmt.Errorf("échec de l'envoi rclone: %w", err), writer, startTime)
	}
	fmt.Fprintf(writer, "[%s] Envoi rclone réussi avec succès !\n", time.Now().Format("15:04:05"))

	// Cleanup local file
	_ = os.Remove(localPath)

	// Step 3: Apply retention
	if job.RetentionCount > 0 || job.RetentionDays > 0 {
		fmt.Fprintf(writer, "[%s] Application des politiques de rétention (%d sauvegardes / %d jours)...\n",
			time.Now().Format("15:04:05"), job.RetentionCount, job.RetentionDays)
		_ = e.rcloneSvc.ApplyRetention(ctx, job.RcloneRemote, job.RclonePath, job.RetentionCount, job.RetentionDays, writer)
	}

	duration := time.Since(startTime)
	history.DurationMs = duration.Milliseconds()
	history.Status = "success"
	history.Logs = writer.buf.String()

	fmt.Fprintf(writer, "[%s] Sauvegarde terminée avec succès en %.2fs !\n", time.Now().Format("15:04:05"), duration.Seconds())

	// Update DB records
	_ = e.db.UpdateJobRunStatus(job.ID, "success", history.DurationMs, history.SizeBytes)
	_ = e.db.CreateHistory(history)

	// Send Notifications
	settings, _ := e.db.GetSettings()
	if settings != nil {
		go e.notifier.NotifySuccess(context.Background(), settings, job, history)
	}

	return history, nil
}

func (e *Engine) handleFailure(ctx context.Context, job *database.BackupJob, history *database.BackupHistory, err error, writer *sseWriter, startTime time.Time) (*database.BackupHistory, error) {
	duration := time.Since(startTime)
	history.DurationMs = duration.Milliseconds()
	history.Status = "failed"
	fmt.Fprintf(writer, "[%s] ERREUR : %v\n", time.Now().Format("15:04:05"), err)
	history.Logs = writer.buf.String()

	_ = e.db.UpdateJobRunStatus(job.ID, "failed", history.DurationMs, 0)
	_ = e.db.CreateHistory(history)

	settings, _ := e.db.GetSettings()
	if settings != nil {
		go e.notifier.NotifyFailure(context.Background(), settings, job, err.Error())
	}

	return history, err
}

func (e *Engine) Restore(ctx context.Context, jobID int64, filename string) error {
	job, err := e.db.GetJob(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %d introuvable", jobID)
	}

	var logBuf bytes.Buffer
	writer := &sseWriter{
		jobID:  jobID,
		engine: e,
		buf:    &logBuf,
	}

	return RestoreBackup(ctx, e.dockerCli, e.rcloneSvc, job, filename, e.cfg.TmpBackupDir, writer)
}

func sanitizeFilename(s string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	safe := reg.ReplaceAllString(s, "_")
	return strings.Trim(safe, "_")
}
