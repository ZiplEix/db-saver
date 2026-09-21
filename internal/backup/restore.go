package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
	"github.com/ZiplEix/db-saver/internal/rclone"
	"github.com/docker/docker/api/types/container"
)

func RestoreBackup(ctx context.Context, dockerCli *docker.DockerClient, rcloneSvc *rclone.RcloneService, job *database.BackupJob, backupFilename, tmpDir string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "[Restauration] Démarrage de la restauration du fichier %s pour %s...\n", backupFilename, job.Name)

	localCompressedFile := filepath.Join(tmpDir, backupFilename)
	defer os.Remove(localCompressedFile)

	// Step 1: Download from rclone
	fmt.Fprintf(logWriter, "[Restauration] Téléchargement depuis %s:%s/%s...\n", job.RcloneRemote, job.RclonePath, backupFilename)
	if err := rcloneSvc.Download(ctx, job.RcloneRemote, job.RclonePath, backupFilename, localCompressedFile, logWriter); err != nil {
		return fmt.Errorf("failed to download backup from rclone: %w", err)
	}

	// Step 2: Restore depending on DB type
	if job.DBType == database.DBTypePostgres {
		return restorePostgres(ctx, dockerCli, job, localCompressedFile, logWriter)
	} else if job.DBType == database.DBTypeSQLite {
		return restoreSQLite(ctx, dockerCli, job, localCompressedFile, logWriter)
	}

	return fmt.Errorf("unsupported db type: %s", job.DBType)
}

func restorePostgres(ctx context.Context, dockerCli *docker.DockerClient, job *database.BackupJob, compressedFile string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "[Restauration Postgres] Décompression de l'archive SQL...\n")

	f, err := os.Open(compressedFile)
	if err != nil {
		return fmt.Errorf("failed to open compressed backup: %w", err)
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to initialize gzip reader: %w", err)
	}
	defer gzReader.Close()

	// Create a tar archive with the SQL file to copy to container
	var tarBuf bytes.Buffer
	tarWriter := tar.NewWriter(&tarBuf)

	sqlData, err := io.ReadAll(gzReader)
	if err != nil {
		return fmt.Errorf("failed to read decompressed SQL: %w", err)
	}

	hdr := &tar.Header{
		Name: "restore.sql",
		Mode: 0644,
		Size: int64(len(sqlData)),
	}
	if err := tarWriter.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tarWriter.Write(sqlData); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}

	fmt.Fprintf(logWriter, "[Restauration Postgres] Copie du script SQL dans le conteneur /tmp/...\n")
	if err := dockerCli.CopyToContainer(ctx, job.ContainerID, "/tmp", &tarBuf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("failed to copy SQL file to container: %w", err)
	}
	defer func() {
		_, _, _ = dockerCli.ExecInContainer(ctx, job.ContainerID, []string{"rm", "-f", "/tmp/restore.sql"}, nil)
	}()

	user := job.PostgresUser
	if user == "" {
		user = "postgres"
	}
	db := job.PostgresDB
	if db == "" {
		db = user
	}

	var env []string
	if job.PostgresPassword != "" {
		env = append(env, "PGPASSWORD="+job.PostgresPassword)
	}

	fmt.Fprintf(logWriter, "[Restauration Postgres] Exécution de psql pour restaurer la base %s...\n", db)
	cmd := []string{"psql", "-U", user, "-d", db, "-f", "/tmp/restore.sql"}
	reader, execID, err := dockerCli.ExecInContainer(ctx, job.ContainerID, cmd, env)
	if err != nil {
		return fmt.Errorf("failed to execute psql in container: %w", err)
	}

	// Capture output
	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, reader)

	inspect, err := dockerCli.InspectExec(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("failed to inspect psql execution: %w", err)
	}

	if inspect.ExitCode != 0 {
		return fmt.Errorf("psql failed (exit code %d): %s", inspect.ExitCode, outBuf.String())
	}

	fmt.Fprintf(logWriter, "[Restauration Postgres] Base de données restaurée avec succès !\n")
	return nil
}

func restoreSQLite(ctx context.Context, dockerCli *docker.DockerClient, job *database.BackupJob, compressedFile string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "[Restauration SQLite] Décompression de l'archive SQLite...\n")

	f, err := os.Open(compressedFile)
	if err != nil {
		return fmt.Errorf("failed to open compressed backup: %w", err)
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to initialize gzip reader: %w", err)
	}
	defer gzReader.Close()

	dbData, err := io.ReadAll(gzReader)
	if err != nil {
		return fmt.Errorf("failed to read decompressed sqlite database: %w", err)
	}

	targetDir := filepath.Dir(job.SQLitePath)
	targetFileName := filepath.Base(job.SQLitePath)

	// Step: Make a safety copy inside the container if the file exists
	fmt.Fprintf(logWriter, "[Restauration SQLite] Création d'une sauvegarde de précaution %s.bak dans le conteneur...\n", job.SQLitePath)
	_, _, _ = dockerCli.ExecInContainer(ctx, job.ContainerID, []string{"cp", job.SQLitePath, job.SQLitePath + ".bak"}, nil)

	// Create tar archive with the restored sqlite file
	var tarBuf bytes.Buffer
	tarWriter := tar.NewWriter(&tarBuf)

	hdr := &tar.Header{
		Name: targetFileName,
		Mode: 0644,
		Size: int64(len(dbData)),
	}
	if err := tarWriter.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tarWriter.Write(dbData); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}

	fmt.Fprintf(logWriter, "[Restauration SQLite] Remplacement du fichier %s dans le conteneur...\n", job.SQLitePath)
	if err := dockerCli.CopyToContainer(ctx, job.ContainerID, targetDir, &tarBuf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("failed to copy restored database to container: %w", err)
	}

	fmt.Fprintf(logWriter, "[Restauration SQLite] Base SQLite restaurée avec succès !\n")
	return nil
}
