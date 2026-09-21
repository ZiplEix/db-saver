package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
)

func BackupSQLite(ctx context.Context, dockerCli *docker.DockerClient, job *database.BackupJob, destFilePath string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "[SQLite] Initialisation de la sauvegarde pour %s (%s)...\n", job.Name, job.ContainerName)

	if job.SQLitePath == "" {
		return fmt.Errorf("sqlite database path is empty for job %s", job.Name)
	}

	tmpBackupInContainer := "/tmp/db_saver_backup.db"

	// Strategy 1: Attempt safe VACUUM INTO or .backup using sqlite3 in the container
	useSafeVacuum := false
	cmd := []string{"sqlite3", job.SQLitePath, fmt.Sprintf("VACUUM INTO '%s';", tmpBackupInContainer)}
	fmt.Fprintf(logWriter, "[SQLite] Tentative d'extraction sécurisée via VACUUM INTO...\n")

	reader, execID, err := dockerCli.ExecInContainer(ctx, job.ContainerID, cmd, nil)
	if err == nil {
		// Discard output
		_, _ = io.Copy(io.Discard, reader)
		inspect, inspectErr := dockerCli.InspectExec(ctx, execID.ID)
		if inspectErr == nil && inspect.ExitCode == 0 {
			useSafeVacuum = true
			fmt.Fprintf(logWriter, "[SQLite] VACUUM INTO réussi avec succès.\n")
		}
	}

	var sourcePathInContainer string
	if useSafeVacuum {
		sourcePathInContainer = tmpBackupInContainer
		defer func() {
			// Clean up temporary file inside container
			_, _, _ = dockerCli.ExecInContainer(ctx, job.ContainerID, []string{"rm", "-f", tmpBackupInContainer}, nil)
		}()
	} else {
		fmt.Fprintf(logWriter, "[SQLite] sqlite3 non disponible ou échec, fallback vers copie directe (%s)...\n", job.SQLitePath)
		sourcePathInContainer = job.SQLitePath
	}

	// Copy the file out of the container via Docker archive
	tarStream, _, err := dockerCli.CopyFromContainer(ctx, job.ContainerID, sourcePathInContainer)
	if err != nil {
		return fmt.Errorf("failed to copy database from container: %w", err)
	}
	defer tarStream.Close()

	// Create local destination file with gzip compression
	outFile, err := os.Create(destFilePath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer outFile.Close()

	gzWriter := gzip.NewWriter(outFile)
	defer gzWriter.Close()

	// Read tar archive and stream the file into gzWriter
	tarReader := tar.NewReader(tarStream)
	found := false
	var writtenBytes int64

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading container tar archive: %w", err)
		}

		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			found = true
			writtenBytes, err = io.Copy(gzWriter, tarReader)
			if err != nil {
				return fmt.Errorf("error compressing sqlite database: %w", err)
			}
			break
		}
	}

	if !found {
		_ = os.Remove(destFilePath)
		return fmt.Errorf("sqlite database file not found in container at %s", sourcePathInContainer)
	}

	if err := gzWriter.Close(); err != nil {
		return fmt.Errorf("error closing gzip writer: %w", err)
	}

	fmt.Fprintf(logWriter, "[SQLite] Sauvegarde SQLite terminée avec succès. Données extraites : %d octets.\n", writtenBytes)
	return nil
}
