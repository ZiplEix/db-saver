package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ZiplEix/db-saver/internal/database"
)

type NotificationService struct {
	client *http.Client
}

func NewNotificationService() *NotificationService {
	return &NotificationService{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (n *NotificationService) NotifySuccess(ctx context.Context, settings *database.AppSettings, job *database.BackupJob, history *database.BackupHistory) {
	if !settings.NotifyOnSuccess {
		return
	}

	sizeMB := float64(history.SizeBytes) / (1024 * 1024)
	durationSec := float64(history.DurationMs) / 1000.0

	// Discord
	if settings.DiscordWebhookURL != "" {
		_ = n.sendDiscord(ctx, settings.DiscordWebhookURL, discordPayload{
			Embeds: []discordEmbed{
				{
					Title:       fmt.Sprintf("✅ Sauvegarde réussie : %s", job.Name),
					Description: fmt.Sprintf("La sauvegarde de la base de données **%s** (%s) a été effectuée avec succès.", job.Name, job.DBType),
					Color:       0x10B981, // Green
					Fields: []discordField{
						{Name: "Conteneur", Value: job.ContainerName, Inline: true},
						{Name: "Type", Value: string(job.DBType), Inline: true},
						{Name: "Destination", Value: fmt.Sprintf("%s:%s", job.RcloneRemote, job.RclonePath), Inline: true},
						{Name: "Fichier", Value: history.Filename, Inline: false},
						{Name: "Taille", Value: fmt.Sprintf("%.2f Mo", sizeMB), Inline: true},
						{Name: "Durée", Value: fmt.Sprintf("%.2fs", durationSec), Inline: true},
					},
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		})
	}

	// Telegram
	if settings.TelegramBotToken != "" && settings.TelegramChatID != "" {
		text := fmt.Sprintf(
			"✅ *db-saver: Sauvegarde réussie*\n\n"+
				"*Job:* %s\n"+
				"*Conteneur:* `%s`\n"+
				"*Type:* %s\n"+
				"*Fichier:* `%s`\n"+
				"*Taille:* %.2f Mo\n"+
				"*Durée:* %.2fs\n"+
				"*Destination:* `%s:%s`",
			job.Name, job.ContainerName, job.DBType, history.Filename, sizeMB, durationSec, job.RcloneRemote, job.RclonePath,
		)
		_ = n.sendTelegram(ctx, settings.TelegramBotToken, settings.TelegramChatID, text)
	}

	// Generic Webhook
	if settings.GenericWebhookURL != "" {
		_ = n.sendGeneric(ctx, settings.GenericWebhookURL, map[string]interface{}{
			"event":        "backup_success",
			"job_id":       job.ID,
			"job_name":     job.Name,
			"container":    job.ContainerName,
			"db_type":      job.DBType,
			"remote":       job.RcloneRemote,
			"path":         job.RclonePath,
			"filename":     history.Filename,
			"size_bytes":   history.SizeBytes,
			"duration_ms":  history.DurationMs,
			"trigger_type": history.TriggerType,
			"timestamp":    time.Now().Format(time.RFC3339),
		})
	}
}

func (n *NotificationService) NotifyFailure(ctx context.Context, settings *database.AppSettings, job *database.BackupJob, errMsg string) {
	if !settings.NotifyOnFailure {
		return
	}

	// Discord
	if settings.DiscordWebhookURL != "" {
		_ = n.sendDiscord(ctx, settings.DiscordWebhookURL, discordPayload{
			Embeds: []discordEmbed{
				{
					Title:       fmt.Sprintf("❌ Échec de sauvegarde : %s", job.Name),
					Description: fmt.Sprintf("Une erreur est survenue lors de la sauvegarde du conteneur **%s** : \n```\n%s\n```", job.ContainerName, errMsg),
					Color:       0xEF4444, // Red
					Fields: []discordField{
						{Name: "Conteneur", Value: job.ContainerName, Inline: true},
						{Name: "Type", Value: string(job.DBType), Inline: true},
						{Name: "Destination", Value: fmt.Sprintf("%s:%s", job.RcloneRemote, job.RclonePath), Inline: true},
					},
					Timestamp: time.Now().Format(time.RFC3339),
				},
			},
		})
	}

	// Telegram
	if settings.TelegramBotToken != "" && settings.TelegramChatID != "" {
		text := fmt.Sprintf(
			"❌ *db-saver: Échec de sauvegarde*\n\n"+
				"*Job:* %s\n"+
				"*Conteneur:* `%s`\n"+
				"*Type:* %s\n"+
				"*Erreur:*\n```\n%s\n```",
			job.Name, job.ContainerName, job.DBType, errMsg,
		)
		_ = n.sendTelegram(ctx, settings.TelegramBotToken, settings.TelegramChatID, text)
	}

	// Generic Webhook
	if settings.GenericWebhookURL != "" {
		_ = n.sendGeneric(ctx, settings.GenericWebhookURL, map[string]interface{}{
			"event":       "backup_failed",
			"job_id":      job.ID,
			"job_name":    job.Name,
			"container":   job.ContainerName,
			"db_type":     job.DBType,
			"error":       errMsg,
			"timestamp":   time.Now().Format(time.RFC3339),
		})
	}
}

// Discord structs
type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields"`
	Timestamp   string         `json:"timestamp"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

func (n *NotificationService) sendDiscord(ctx context.Context, url string, payload discordPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned status %d", resp.StatusCode)
	}
	return nil
}

func (n *NotificationService) sendTelegram(ctx context.Context, token, chatID, text string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned status %d", resp.StatusCode)
	}
	return nil
}

func (n *NotificationService) sendGeneric(ctx context.Context, url string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
