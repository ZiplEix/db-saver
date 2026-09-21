package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RcloneService struct {
	configPath string
}

type RemoteItem struct {
	Name    string    `json:"Name"`
	Path    string    `json:"Path"`
	Size    int64     `json:"Size"`
	ModTime time.Time `json:"ModTime"`
	IsDir   bool      `json:"IsDir"`
}

func NewRcloneService(configPath string) *RcloneService {
	return &RcloneService{
		configPath: configPath,
	}
}

// resolveConfigPath returns the actual file path to rclone.conf
// If configPath is a directory (e.g. Docker auto-created it or mounted directory), it appends rclone.conf
func (r *RcloneService) resolveConfigPath() string {
	if fi, err := os.Stat(r.configPath); err == nil && fi.IsDir() {
		return filepath.Join(r.configPath, "rclone.conf")
	}
	return r.configPath
}

// GetConfigPath returns the current path to rclone.conf
func (r *RcloneService) GetConfigPath() string {
	return r.resolveConfigPath()
}

// ReadConfigFile returns the raw content of rclone.conf
func (r *RcloneService) ReadConfigFile() (string, error) {
	target := r.resolveConfigPath()
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return "", nil
	}
	bytes, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// SaveConfigFile writes the content into rclone.conf
func (r *RcloneService) SaveConfigFile(content string) error {
	target := r.resolveConfigPath()
	// Ensure parent directory exists
	if dir := filepath.Dir(target); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	log.Printf("[Rclone] Enregistrement de rclone.conf vers %s (%d octets)", target, len(content))
	return os.WriteFile(target, []byte(content), 0600)
}

// ListRemotes executes `rclone listremotes` and returns remote names (without trailing colon)
func (r *RcloneService) ListRemotes(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "rclone", "listremotes", "--config", r.resolveConfigPath())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	var remotes []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			remotes = append(remotes, strings.TrimSuffix(trimmed, ":"))
		}
	}
	return remotes, nil
}

// TestRemote checks if a remote is reachable
func (r *RcloneService) TestRemote(ctx context.Context, remote string) error {
	target := remote + ":"
	cmd := exec.CommandContext(ctx, "rclone", "lsd", target, "--config", r.resolveConfigPath(), "--max-depth", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(out), err)
	}
	return nil
}

// Upload sends a file to remote:remotePath/filename
func (r *RcloneService) Upload(ctx context.Context, localPath, remote, remotePath, filename string, logWriter io.Writer) error {
	dest := fmt.Sprintf("%s:%s/%s", remote, strings.Trim(remotePath, "/"), filename)
	cmd := exec.CommandContext(ctx, "rclone", "copyto", localPath, dest, "--config", r.resolveConfigPath(), "-v")

	if logWriter != nil {
		cmd.Stdout = logWriter
		cmd.Stderr = logWriter
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rclone upload failed: %w", err)
	}
	return nil
}

// Download fetches a file from remote:remotePath/filename to localPath
func (r *RcloneService) Download(ctx context.Context, remote, remotePath, filename, localPath string, logWriter io.Writer) error {
	src := fmt.Sprintf("%s:%s/%s", remote, strings.Trim(remotePath, "/"), filename)
	cmd := exec.CommandContext(ctx, "rclone", "copyto", src, localPath, "--config", r.resolveConfigPath(), "-v")

	if logWriter != nil {
		cmd.Stdout = logWriter
		cmd.Stderr = logWriter
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rclone download failed: %w", err)
	}
	return nil
}

// ListBackups lists backup files stored in remote:remotePath
func (r *RcloneService) ListBackups(ctx context.Context, remote, remotePath string) ([]RemoteItem, error) {
	target := fmt.Sprintf("%s:%s", remote, strings.Trim(remotePath, "/"))
	cmd := exec.CommandContext(ctx, "rclone", "lsjson", target, "--config", r.resolveConfigPath())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}

	var items []RemoteItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("failed to parse rclone lsjson output: %w", err)
	}

	// Filter out directories and sort newest first
	var files []RemoteItem
	for _, item := range items {
		if !item.IsDir {
			files = append(files, item)
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	return files, nil
}

// ApplyRetention deletes older backups according to retentionCount and retentionDays
func (r *RcloneService) ApplyRetention(ctx context.Context, remote, remotePath string, retentionCount int, retentionDays int, logWriter io.Writer) error {
	if retentionCount <= 0 && retentionDays <= 0 {
		return nil
	}

	items, err := r.ListBackups(ctx, remote, remotePath)
	if err != nil {
		return fmt.Errorf("failed to list backups for retention: %w", err)
	}

	now := time.Now()
	toDelete := make(map[string]bool)

	// Check retention by days
	if retentionDays > 0 {
		maxAge := time.Duration(retentionDays) * 24 * time.Hour
		for _, item := range items {
			if now.Sub(item.ModTime) > maxAge {
				toDelete[item.Name] = true
			}
		}
	}

	// Check retention by count (keep newest N files)
	if retentionCount > 0 && len(items) > retentionCount {
		for i := retentionCount; i < len(items); i++ {
			toDelete[items[i].Name] = true
		}
	}

	// Delete identified files
	for filename := range toDelete {
		targetFile := fmt.Sprintf("%s:%s/%s", remote, strings.Trim(remotePath, "/"), filename)
		if logWriter != nil {
			fmt.Fprintf(logWriter, "[Retention] Suppressing old backup: %s\n", targetFile)
		}
		cmd := exec.CommandContext(ctx, "rclone", "deletefile", targetFile, "--config", r.resolveConfigPath())
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			if logWriter != nil {
				fmt.Fprintf(logWriter, "[Retention Error] Failed to delete %s: %s\n", targetFile, errBuf.String())
			}
		}
	}

	return nil
}
