package database

import (
	"time"
)

type DBType string

const (
	DBTypePostgres DBType = "postgres"
	DBTypeSQLite   DBType = "sqlite"
)

type BackupJob struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	ContainerID      string     `json:"container_id"`
	ContainerName    string     `json:"container_name"`
	DBType           DBType     `json:"db_type"`
	ScheduleCron     string     `json:"schedule_cron"`
	SchedulePreset   string     `json:"schedule_preset"`
	RcloneRemote     string     `json:"rclone_remote"`
	RclonePath       string     `json:"rclone_path"`
	RetentionCount   int        `json:"retention_count"`
	RetentionDays    int        `json:"retention_days"`
	PostgresDB       string     `json:"postgres_db"`
	PostgresUser     string     `json:"postgres_user"`
	PostgresPassword string     `json:"postgres_password"`
	PostgresPort     int        `json:"postgres_port"`
	SQLitePath       string     `json:"sqlite_path"`
	Enabled          bool       `json:"enabled"`
	LastRunStatus    string     `json:"last_run_status"` // "success", "failed", "running", ""
	LastRunAt        *time.Time `json:"last_run_at"`
	LastRunDuration  int64      `json:"last_run_duration"` // ms
	LastRunSize      int64      `json:"last_run_size"`     // bytes
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type BackupHistory struct {
	ID          int64     `json:"id"`
	JobID       int64     `json:"job_id"`
	JobName     string    `json:"job_name"`
	Status      string    `json:"status"` // "running", "success", "failed"
	SizeBytes   int64     `json:"size_bytes"`
	DurationMs  int64     `json:"duration_ms"`
	Filename    string    `json:"filename"`
	Logs        string    `json:"logs"`
	TriggerType string    `json:"trigger_type"` // "scheduled", "manual"
	CreatedAt   time.Time `json:"created_at"`
}

type AppSettings struct {
	ID                int64  `json:"id"`
	DiscordWebhookURL string `json:"discord_webhook_url"`
	TelegramBotToken  string `json:"telegram_bot_token"`
	TelegramChatID    string `json:"telegram_chat_id"`
	GenericWebhookURL string `json:"generic_webhook_url"`
	NotifyOnSuccess   bool   `json:"notify_on_success"`
	NotifyOnFailure   bool   `json:"notify_on_failure"`
	UpdatedAt         time.Time `json:"updated_at"`
}
