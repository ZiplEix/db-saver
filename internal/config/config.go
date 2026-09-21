package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	Port             string
	AdminUser        string
	AdminPassword    string
	DataDir          string
	DBPath           string
	RcloneConfigPath string
	TmpBackupDir     string
	SessionSecret    string
}

func LoadConfig() *Config {
	port := getEnv("PORT", "8080")
	adminUser := getEnv("ADMIN_USER", "admin")
	adminPass := getEnv("ADMIN_PASSWORD", "admin123")
	dataDir := getEnv("DATA_DIR", "./data")
	rcloneConfig := getEnv("RCLONE_CONFIG_PATH", "./config/rclone.conf")
	tmpDir := getEnv("TMP_BACKUP_DIR", filepath.Join(dataDir, "tmp"))
	sessionSecret := getEnv("SESSION_SECRET", "db-saver-super-secret-key-change-me")

	// Ensure data and tmp directories exist
	_ = os.MkdirAll(dataDir, 0755)
	_ = os.MkdirAll(tmpDir, 0755)

	// Also make sure parent directory of rclone config exists if specified
	if rcloneDir := filepath.Dir(rcloneConfig); rcloneDir != "" && rcloneDir != "." {
		_ = os.MkdirAll(rcloneDir, 0755)
	}

	return &Config{
		Port:             port,
		AdminUser:        adminUser,
		AdminPassword:    adminPass,
		DataDir:          dataDir,
		DBPath:           filepath.Join(dataDir, "db-saver.db"),
		RcloneConfigPath: rcloneConfig,
		TmpBackupDir:     tmpDir,
		SessionSecret:    sessionSecret,
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
