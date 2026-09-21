package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ZiplEix/db-saver/internal/auth"
	"github.com/ZiplEix/db-saver/internal/backup"
	"github.com/ZiplEix/db-saver/internal/config"
	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
	"github.com/ZiplEix/db-saver/internal/notification"
	"github.com/ZiplEix/db-saver/internal/rclone"
	"github.com/ZiplEix/db-saver/internal/scheduler"
	"github.com/ZiplEix/db-saver/internal/web"
)

func main() {
	log.Println("==================================================")
	log.Println("           Démarrage de db-saver                  ")
	log.Println("==================================================")

	cfg := config.LoadConfig()
	log.Printf("[Config] Port: %s | Utilisateur admin: %s", cfg.Port, cfg.AdminUser)
	log.Printf("[Config] Base de données: %s", cfg.DBPath)
	log.Printf("[Config] Fichier rclone: %s", cfg.RcloneConfigPath)

	// 1. Initialize SQLite local storage
	db, err := database.NewDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("[DB Fatal] Impossible d'initialiser la base SQLite: %v", err)
	}
	defer db.Close()
	log.Println("[DB] Base de données SQLite initialisée avec succès.")

	// 2. Initialize Auth
	authMgr := auth.NewSessionManager(cfg)

	// 3. Initialize Docker Client
	dockerCli, err := docker.NewDockerClient()
	if err != nil {
		log.Printf("[Docker Warning] Impossible d'initialiser le client Docker: %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if pingErr := dockerCli.Ping(ctx); pingErr != nil {
			log.Printf("[Docker Warning] Échec du ping Docker (/var/run/docker.sock): %v", pingErr)
		} else {
			log.Println("[Docker] Connecté avec succès au démon Docker.")
		}
		cancel()
	}

	// 4. Initialize Rclone Service
	rcloneSvc := rclone.NewRcloneService(cfg.RcloneConfigPath)

	// 5. Initialize Notification Service
	notifier := notification.NewNotificationService()

	// 6. Initialize Backup Engine
	engine := backup.NewEngine(cfg, db, dockerCli, rcloneSvc, notifier)

	// 7. Initialize & Start Scheduler
	sched := scheduler.NewScheduler(db, engine)
	if err := sched.Start(); err != nil {
		log.Printf("[Scheduler Warning] Erreur au démarrage du planificateur: %v", err)
	}
	defer sched.Stop()

	// 8. Initialize Web Handlers & Router
	handler, err := web.NewHandler(cfg, db, authMgr, dockerCli, rcloneSvc, engine, sched, notifier)
	if err != nil {
		log.Fatalf("[Web Fatal] Impossible d'initialiser le serveur web: %v", err)
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler.Routes(),
		ReadTimeout:  15 * time.Minute, // Support large backup downloads
		WriteTimeout: 15 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[HTTP] Serveur web à l'écoute sur http://0.0.0.0:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[HTTP Fatal] Erreur du serveur HTTP: %v", err)
		}
	}()

	<-stopChan
	log.Println("[System] Arrêt en cours...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[HTTP] Erreur lors de l'arrêt gracieux: %v", err)
	}

	log.Println("[System] db-saver arrêté proprement.")
}
