package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func NewDB(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite settings for concurrency
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS backup_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		container_id TEXT NOT NULL,
		container_name TEXT NOT NULL,
		db_type TEXT NOT NULL,
		schedule_cron TEXT NOT NULL,
		schedule_preset TEXT NOT NULL DEFAULT 'daily_3am',
		rclone_remote TEXT NOT NULL,
		rclone_path TEXT NOT NULL,
		retention_count INTEGER NOT NULL DEFAULT 7,
		retention_days INTEGER NOT NULL DEFAULT 30,
		postgres_db TEXT NOT NULL DEFAULT '',
		postgres_user TEXT NOT NULL DEFAULT '',
		postgres_password TEXT NOT NULL DEFAULT '',
		postgres_port INTEGER NOT NULL DEFAULT 5432,
		sqlite_path TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		last_run_status TEXT NOT NULL DEFAULT '',
		last_run_at DATETIME,
		last_run_duration INTEGER NOT NULL DEFAULT 0,
		last_run_size INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS backup_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL,
		job_name TEXT NOT NULL,
		status TEXT NOT NULL,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		filename TEXT NOT NULL DEFAULT '',
		logs TEXT NOT NULL DEFAULT '',
		trigger_type TEXT NOT NULL DEFAULT 'scheduled',
		created_at DATETIME NOT NULL,
		FOREIGN KEY(job_id) REFERENCES backup_jobs(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS app_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		discord_webhook_url TEXT NOT NULL DEFAULT '',
		telegram_bot_token TEXT NOT NULL DEFAULT '',
		telegram_chat_id TEXT NOT NULL DEFAULT '',
		generic_webhook_url TEXT NOT NULL DEFAULT '',
		notify_on_success INTEGER NOT NULL DEFAULT 0,
		notify_on_failure INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL
	);

	INSERT OR IGNORE INTO app_settings (id, discord_webhook_url, telegram_bot_token, telegram_chat_id, generic_webhook_url, notify_on_success, notify_on_failure, updated_at)
	VALUES (1, '', '', '', '', 0, 1, CURRENT_TIMESTAMP);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) GetJobs() ([]BackupJob, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, container_id, container_name, db_type, schedule_cron, schedule_preset,
		       rclone_remote, rclone_path, retention_count, retention_days,
		       postgres_db, postgres_user, postgres_password, postgres_port,
		       sqlite_path, enabled, last_run_status, last_run_at, last_run_duration, last_run_size,
		       created_at, updated_at
		FROM backup_jobs
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []BackupJob
	for rows.Next() {
		var j BackupJob
		var lastRunAt sql.NullTime
		var enabledInt int

		err := rows.Scan(
			&j.ID, &j.Name, &j.ContainerID, &j.ContainerName, &j.DBType, &j.ScheduleCron, &j.SchedulePreset,
			&j.RcloneRemote, &j.RclonePath, &j.RetentionCount, &j.RetentionDays,
			&j.PostgresDB, &j.PostgresUser, &j.PostgresPassword, &j.PostgresPort,
			&j.SQLitePath, &enabledInt, &j.LastRunStatus, &lastRunAt, &j.LastRunDuration, &j.LastRunSize,
			&j.CreatedAt, &j.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		j.Enabled = (enabledInt == 1)
		if lastRunAt.Valid {
			j.LastRunAt = &lastRunAt.Time
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func (db *DB) GetJob(id int64) (*BackupJob, error) {
	row := db.conn.QueryRow(`
		SELECT id, name, container_id, container_name, db_type, schedule_cron, schedule_preset,
		       rclone_remote, rclone_path, retention_count, retention_days,
		       postgres_db, postgres_user, postgres_password, postgres_port,
		       sqlite_path, enabled, last_run_status, last_run_at, last_run_duration, last_run_size,
		       created_at, updated_at
		FROM backup_jobs
		WHERE id = ?
	`, id)

	var j BackupJob
	var lastRunAt sql.NullTime
	var enabledInt int

	err := row.Scan(
		&j.ID, &j.Name, &j.ContainerID, &j.ContainerName, &j.DBType, &j.ScheduleCron, &j.SchedulePreset,
		&j.RcloneRemote, &j.RclonePath, &j.RetentionCount, &j.RetentionDays,
		&j.PostgresDB, &j.PostgresUser, &j.PostgresPassword, &j.PostgresPort,
		&j.SQLitePath, &enabledInt, &j.LastRunStatus, &lastRunAt, &j.LastRunDuration, &j.LastRunSize,
		&j.CreatedAt, &j.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	j.Enabled = (enabledInt == 1)
	if lastRunAt.Valid {
		j.LastRunAt = &lastRunAt.Time
	}
	return &j, nil
}

func (db *DB) CreateJob(j *BackupJob) error {
	now := time.Now()
	j.CreatedAt = now
	j.UpdatedAt = now

	enabledInt := 0
	if j.Enabled {
		enabledInt = 1
	}

	res, err := db.conn.Exec(`
		INSERT INTO backup_jobs (
			name, container_id, container_name, db_type, schedule_cron, schedule_preset,
			rclone_remote, rclone_path, retention_count, retention_days,
			postgres_db, postgres_user, postgres_password, postgres_port,
			sqlite_path, enabled, last_run_status, last_run_at, last_run_duration, last_run_size,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		j.Name, j.ContainerID, j.ContainerName, string(j.DBType), j.ScheduleCron, j.SchedulePreset,
		j.RcloneRemote, j.RclonePath, j.RetentionCount, j.RetentionDays,
		j.PostgresDB, j.PostgresUser, j.PostgresPassword, j.PostgresPort,
		j.SQLitePath, enabledInt, j.LastRunStatus, j.LastRunAt, j.LastRunDuration, j.LastRunSize,
		j.CreatedAt, j.UpdatedAt,
	)
	if err != nil {
		return err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	j.ID = id
	return nil
}

func (db *DB) UpdateJob(j *BackupJob) error {
	j.UpdatedAt = time.Now()
	enabledInt := 0
	if j.Enabled {
		enabledInt = 1
	}

	_, err := db.conn.Exec(`
		UPDATE backup_jobs SET
			name = ?, container_id = ?, container_name = ?, db_type = ?, schedule_cron = ?, schedule_preset = ?,
			rclone_remote = ?, rclone_path = ?, retention_count = ?, retention_days = ?,
			postgres_db = ?, postgres_user = ?, postgres_password = ?, postgres_port = ?,
			sqlite_path = ?, enabled = ?, updated_at = ?
		WHERE id = ?
	`,
		j.Name, j.ContainerID, j.ContainerName, string(j.DBType), j.ScheduleCron, j.SchedulePreset,
		j.RcloneRemote, j.RclonePath, j.RetentionCount, j.RetentionDays,
		j.PostgresDB, j.PostgresUser, j.PostgresPassword, j.PostgresPort,
		j.SQLitePath, enabledInt, j.UpdatedAt, j.ID,
	)
	return err
}

func (db *DB) UpdateJobRunStatus(id int64, status string, durationMs int64, sizeBytes int64) error {
	now := time.Now()
	_, err := db.conn.Exec(`
		UPDATE backup_jobs SET
			last_run_status = ?,
			last_run_at = ?,
			last_run_duration = ?,
			last_run_size = ?,
			updated_at = ?
		WHERE id = ?
	`, status, now, durationMs, sizeBytes, now, id)
	return err
}

func (db *DB) DeleteJob(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM backup_jobs WHERE id = ?`, id)
	return err
}

func (db *DB) CreateHistory(h *BackupHistory) error {
	h.CreatedAt = time.Now()
	res, err := db.conn.Exec(`
		INSERT INTO backup_history (
			job_id, job_name, status, size_bytes, duration_ms, filename, logs, trigger_type, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, h.JobID, h.JobName, h.Status, h.SizeBytes, h.DurationMs, h.Filename, h.Logs, h.TriggerType, h.CreatedAt)
	if err != nil {
		return err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	h.ID = id
	return nil
}

func (db *DB) GetRecentHistory(limit int) ([]BackupHistory, error) {
	rows, err := db.conn.Query(`
		SELECT id, job_id, job_name, status, size_bytes, duration_ms, filename, logs, trigger_type, created_at
		FROM backup_history
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []BackupHistory
	for rows.Next() {
		var h BackupHistory
		err := rows.Scan(
			&h.ID, &h.JobID, &h.JobName, &h.Status, &h.SizeBytes, &h.DurationMs, &h.Filename, &h.Logs, &h.TriggerType, &h.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, nil
}

func (db *DB) GetHistoryByJob(jobID int64, limit int) ([]BackupHistory, error) {
	rows, err := db.conn.Query(`
		SELECT id, job_id, job_name, status, size_bytes, duration_ms, filename, logs, trigger_type, created_at
		FROM backup_history
		WHERE job_id = ?
		ORDER BY id DESC
		LIMIT ?
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []BackupHistory
	for rows.Next() {
		var h BackupHistory
		err := rows.Scan(
			&h.ID, &h.JobID, &h.JobName, &h.Status, &h.SizeBytes, &h.DurationMs, &h.Filename, &h.Logs, &h.TriggerType, &h.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, nil
}

func (db *DB) GetHistory(id int64) (*BackupHistory, error) {
	row := db.conn.QueryRow(`
		SELECT id, job_id, job_name, status, size_bytes, duration_ms, filename, logs, trigger_type, created_at
		FROM backup_history
		WHERE id = ?
	`, id)

	var h BackupHistory
	err := row.Scan(
		&h.ID, &h.JobID, &h.JobName, &h.Status, &h.SizeBytes, &h.DurationMs, &h.Filename, &h.Logs, &h.TriggerType, &h.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (db *DB) GetSettings() (*AppSettings, error) {
	row := db.conn.QueryRow(`
		SELECT id, discord_webhook_url, telegram_bot_token, telegram_chat_id, generic_webhook_url,
		       notify_on_success, notify_on_failure, updated_at
		FROM app_settings
		WHERE id = 1
	`)

	var s AppSettings
	var notifySuccess, notifyFailure int
	err := row.Scan(
		&s.ID, &s.DiscordWebhookURL, &s.TelegramBotToken, &s.TelegramChatID, &s.GenericWebhookURL,
		&notifySuccess, &notifyFailure, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	s.NotifyOnSuccess = (notifySuccess == 1)
	s.NotifyOnFailure = (notifyFailure == 1)
	return &s, nil
}

func (db *DB) SaveSettings(s *AppSettings) error {
	s.UpdatedAt = time.Now()
	notifySuccess := 0
	if s.NotifyOnSuccess {
		notifySuccess = 1
	}
	notifyFailure := 0
	if s.NotifyOnFailure {
		notifyFailure = 1
	}

	_, err := db.conn.Exec(`
		UPDATE app_settings SET
			discord_webhook_url = ?,
			telegram_bot_token = ?,
			telegram_chat_id = ?,
			generic_webhook_url = ?,
			notify_on_success = ?,
			notify_on_failure = ?,
			updated_at = ?
		WHERE id = 1
	`, s.DiscordWebhookURL, s.TelegramBotToken, s.TelegramChatID, s.GenericWebhookURL, notifySuccess, notifyFailure, s.UpdatedAt)
	return err
}
