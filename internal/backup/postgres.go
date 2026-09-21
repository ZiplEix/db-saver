package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ZiplEix/db-saver/internal/database"
	"github.com/ZiplEix/db-saver/internal/docker"
	"github.com/docker/docker/pkg/stdcopy"
)

func BackupPostgres(ctx context.Context, dockerCli *docker.DockerClient, job *database.BackupJob, destFilePath string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "[Postgres] Initialisation de la sauvegarde pour %s (%s)...\n", job.Name, job.ContainerName)

	user := job.PostgresUser
	if user == "" {
		user = "postgres"
	}
	db := job.PostgresDB
	if db == "" {
		db = user
	}

	cmd := []string{
		"pg_dump",
		"-U", user,
		"-d", db,
		"--clean",
		"--if-exists",
	}

	var env []string
	if job.PostgresPassword != "" {
		env = append(env, "PGPASSWORD="+job.PostgresPassword)
	}

	fmt.Fprintf(logWriter, "[Postgres] Exécution de pg_dump dans le conteneur %s...\n", job.ContainerName)
	reader, execID, err := dockerCli.ExecInContainer(ctx, job.ContainerID, cmd, env)
	if err != nil {
		return fmt.Errorf("failed to start pg_dump in container: %w", err)
	}

	// Create local destination file with gzip compression
	outFile, err := os.Create(destFilePath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer outFile.Close()

	gzWriter := gzip.NewWriter(outFile)
	defer gzWriter.Close()

	// Demultiplex docker stdout and stderr
	var stderrBuf bytes.Buffer
	var writtenBytes int64

	// stdcopy.StdCopy splits the docker multiplexed stream into stdout (to gzWriter) and stderr
	writtenBytes, err = stdcopy.StdCopy(gzWriter, &stderrBuf, reader)
	if err != nil {
		return fmt.Errorf("error reading dump stream: %w", err)
	}

	if err := gzWriter.Close(); err != nil {
		return fmt.Errorf("error finishing gzip compression: %w", err)
	}

	// Inspect execution result
	inspect, err := dockerCli.InspectExec(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspect.ExitCode != 0 {
		errStr := strings.TrimSpace(stderrBuf.String())
		if errStr == "" {
			errStr = fmt.Sprintf("exit code %d", inspect.ExitCode)
		}
		_ = os.Remove(destFilePath)
		return fmt.Errorf("pg_dump failed (code %d): %s", inspect.ExitCode, errStr)
	}

	fmt.Fprintf(logWriter, "[Postgres] pg_dump terminé avec succès. Données brutes lues : %d octets.\n", writtenBytes)
	return nil
}
