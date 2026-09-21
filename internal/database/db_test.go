package database_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZiplEix/db-saver/internal/database"
)

func TestDBOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "db-saver-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDB(dbPath)
	if err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	defer db.Close()

	// 1. Test Create Job
	job := &database.BackupJob{
		Name:             "Postgres Production",
		ContainerID:      "cont-12345",
		ContainerName:    "prod-postgres",
		DBType:           database.DBTypePostgres,
		ScheduleCron:     "0 3 * * *",
		SchedulePreset:   "daily_3am",
		RcloneRemote:     "s3-backup",
		RclonePath:       "backups/postgres-prod",
		RetentionCount:   7,
		RetentionDays:    30,
		PostgresDB:       "app_db",
		PostgresUser:     "postgres",
		PostgresPassword: "secretpassword",
		PostgresPort:     5432,
		Enabled:          true,
	}

	if err := db.CreateJob(job); err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	if job.ID == 0 {
		t.Fatalf("expected non-zero job ID")
	}

	// 2. Test Get Job
	fetched, err := db.GetJob(job.ID)
	if err != nil {
		t.Fatalf("failed to fetch job: %v", err)
	}
	if fetched == nil || fetched.Name != "Postgres Production" {
		t.Fatalf("job name mismatch: got %v", fetched)
	}
	if !fetched.Enabled {
		t.Fatalf("expected job to be enabled")
	}

	// 3. Test Update Job Status
	now := time.Now()
	if err := db.UpdateJobRunStatus(job.ID, "success", 1250, 1048576); err != nil {
		t.Fatalf("failed to update job run status: %v", err)
	}

	updated, _ := db.GetJob(job.ID)
	if updated.LastRunStatus != "success" || updated.LastRunDuration != 1250 || updated.LastRunSize != 1048576 {
		t.Fatalf("unexpected updated status: %+v", updated)
	}

	// 4. Test History
	history := &database.BackupHistory{
		JobID:       job.ID,
		JobName:     job.Name,
		Status:      "success",
		SizeBytes:   1048576,
		DurationMs:  1250,
		Filename:    "backup_test.sql.gz",
		Logs:        "Backup succeeded\nDone",
		TriggerType: "manual",
		CreatedAt:   now,
	}

	if err := db.CreateHistory(history); err != nil {
		t.Fatalf("failed to create history: %v", err)
	}

	histList, err := db.GetHistoryByJob(job.ID, 10)
	if err != nil || len(histList) == 0 {
		t.Fatalf("failed to get history: %v", err)
	}
	if histList[0].Filename != "backup_test.sql.gz" {
		t.Fatalf("unexpected history item: %+v", histList[0])
	}

	// 5. Test Settings
	settings, err := db.GetSettings()
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}

	settings.DiscordWebhookURL = "https://discord.com/api/webhooks/test"
	settings.NotifyOnSuccess = true
	if err := db.SaveSettings(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	updatedSettings, _ := db.GetSettings()
	if updatedSettings.DiscordWebhookURL != "https://discord.com/api/webhooks/test" || !updatedSettings.NotifyOnSuccess {
		t.Fatalf("settings not saved properly: %+v", updatedSettings)
	}

	// 6. Test Delete Job
	if err := db.DeleteJob(job.ID); err != nil {
		t.Fatalf("failed to delete job: %v", err)
	}

	deleted, _ := db.GetJob(job.ID)
	if deleted != nil {
		t.Fatalf("expected job to be nil after deletion")
	}
}
