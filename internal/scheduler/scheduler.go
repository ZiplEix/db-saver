package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/ZiplEix/db-saver/internal/backup"
	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron    *cron.Cron
	db      *database.DB
	engine  *backup.Engine
	entries map[int64]cron.EntryID
	mu      sync.Mutex
}

func NewScheduler(db *database.DB, engine *backup.Engine) *Scheduler {
	// Standard cron parser with seconds optional (standard 5 fields or with seconds)
	c := cron.New(cron.WithParser(cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)))

	return &Scheduler{
		cron:    c,
		db:      db,
		engine:  engine,
		entries: make(map[int64]cron.EntryID),
	}
}

func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.db.GetJobs()
	if err != nil {
		return fmt.Errorf("failed to fetch jobs for scheduler: %w", err)
	}

	for _, j := range jobs {
		if j.Enabled && j.ScheduleCron != "" {
			s.scheduleJobUnlocked(j)
		}
	}

	s.cron.Start()
	log.Printf("[Scheduler] Planificateur démarré avec %d tâche(s) active(s)", len(s.entries))
	return nil
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) SyncJob(job *database.BackupJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if any
	if entryID, exists := s.entries[job.ID]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, job.ID)
	}

	// Re-schedule if enabled
	if job.Enabled && job.ScheduleCron != "" {
		return s.scheduleJobUnlocked(*job)
	}

	return nil
}

func (s *Scheduler) RemoveJob(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, exists := s.entries[jobID]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, jobID)
	}
}

func (s *Scheduler) scheduleJobUnlocked(j database.BackupJob) error {
	jobID := j.ID
	jobName := j.Name

	entryID, err := s.cron.AddFunc(j.ScheduleCron, func() {
		log.Printf("[Scheduler] Déclenchement automatique du job #%d (%s)...", jobID, jobName)
		_, err := s.engine.RunBackup(context.Background(), jobID, "scheduled")
		if err != nil {
			log.Printf("[Scheduler] Erreur d'exécution du job #%d: %v", jobID, err)
		}
	})
	if err != nil {
		log.Printf("[Scheduler] Erreur de planification pour le job #%d avec expression '%s': %v", j.ID, j.ScheduleCron, err)
		return err
	}

	s.entries[j.ID] = entryID
	log.Printf("[Scheduler] Job #%d (%s) planifié avec l'expression: %s", j.ID, j.Name, j.ScheduleCron)
	return nil
}
