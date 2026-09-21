package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type DockerClient struct {
	cli *client.Client
}

func NewDockerClient() (*DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &DockerClient{cli: cli}, nil
}

func (d *DockerClient) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx)
	return err
}

func (d *DockerClient) Close() error {
	return d.cli.Close()
}

type ContainerInfo struct {
	ID                   string   `json:"id"`
	ShortID              string   `json:"short_id"`
	Name                 string   `json:"name"`
	Image                string   `json:"image"`
	State                string   `json:"state"`
	Status               string   `json:"status"`
	DetectedDB           string   `json:"detected_db"` // "postgres", "sqlite", "none"
	PostgresUser         string   `json:"postgres_user"`
	PostgresDB           string   `json:"postgres_db"`
	PostgresPassword     string   `json:"postgres_password"`
	PostgresPort         int      `json:"postgres_port"`
	Mounts               []string `json:"mounts"`
	PotentialSQLiteFiles []string `json:"potential_sqlite_files"`
}

func (d *DockerClient) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var results []ContainerInfo
	for _, c := range containers {
		info := ContainerInfo{
			ID:      c.ID,
			ShortID: c.ID,
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
		}
		if len(c.ID) > 12 {
			info.ShortID = c.ID[:12]
		}
		if len(c.Names) > 0 {
			info.Name = strings.TrimPrefix(c.Names[0], "/")
		}

		for _, m := range c.Mounts {
			info.Mounts = append(info.Mounts, m.Destination)
		}

		// Inspect container to get environment variables and detailed metadata
		inspect, err := d.cli.ContainerInspect(ctx, c.ID)
		if err == nil && inspect.Config != nil {
			d.analyzeContainer(&info, inspect.Config.Env, inspect.Config.Image)
		}

		results = append(results, info)
	}

	return results, nil
}

func (d *DockerClient) InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	inspect, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	info := &ContainerInfo{
		ID:      inspect.ID,
		ShortID: inspect.ID,
		Name:    strings.TrimPrefix(inspect.Name, "/"),
		Image:   inspect.Config.Image,
		State:   inspect.State.Status,
		Status:  inspect.State.Status,
	}
	if len(inspect.ID) > 12 {
		info.ShortID = inspect.ID[:12]
	}

	for _, m := range inspect.Mounts {
		info.Mounts = append(info.Mounts, m.Destination)
	}

	if inspect.Config != nil {
		d.analyzeContainer(info, inspect.Config.Env, inspect.Config.Image)
	}

	return info, nil
}

func (d *DockerClient) analyzeContainer(info *ContainerInfo, envs []string, imageName string) {
	info.DetectedDB = "none"
	info.PostgresPort = 5432

	envMap := make(map[string]string)
	for _, e := range envs {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	lowerImage := strings.ToLower(imageName)
	lowerName := strings.ToLower(info.Name)

	// Check for Postgres
	isPostgres := strings.Contains(lowerImage, "postgres") ||
		strings.Contains(lowerImage, "timescale") ||
		strings.Contains(lowerName, "postgres") ||
		strings.Contains(lowerName, "pg") ||
		envMap["POSTGRES_DB"] != "" ||
		envMap["POSTGRES_USER"] != ""

	if isPostgres {
		info.DetectedDB = "postgres"
		info.PostgresUser = envMap["POSTGRES_USER"]
		if info.PostgresUser == "" {
			info.PostgresUser = "postgres"
		}
		info.PostgresDB = envMap["POSTGRES_DB"]
		if info.PostgresDB == "" {
			info.PostgresDB = info.PostgresUser
		}
		info.PostgresPassword = envMap["POSTGRES_PASSWORD"]
		return
	}

	// Heuristics for SQLite
	// Check mounts or image names typical of apps using SQLite (e.g. ghost, vaultwarden, uptime-kuma, pocketbase, strapi, etc.)
	sqliteIndicators := []string{"sqlite", "vaultwarden", "uptime-kuma", "pocketbase", "ghost", "shlink", "plausible", "grafana"}
	for _, ind := range sqliteIndicators {
		if strings.Contains(lowerImage, ind) || strings.Contains(lowerName, ind) {
			info.DetectedDB = "sqlite"
			break
		}
	}

	// Propose potential paths based on mounts
	for _, m := range info.Mounts {
		if strings.Contains(m, "data") || strings.Contains(m, "db") || strings.Contains(m, "config") {
			info.PotentialSQLiteFiles = append(info.PotentialSQLiteFiles,
				m+"/database.sqlite",
				m+"/app.db",
				m+"/data.db",
			)
		}
	}
}

// ExecInContainer executes a command inside a container and returns stdout/stderr
func (d *DockerClient) ExecInContainer(ctx context.Context, containerID string, cmd []string, env []string) (io.Reader, types.IDResponse, error) {
	execConfig := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		Env:          env,
	}

	execID, err := d.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return nil, types.IDResponse{}, fmt.Errorf("failed to create exec: %w", err)
	}

	resp, err := d.cli.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, types.IDResponse{}, fmt.Errorf("failed to attach exec: %w", err)
	}

	return resp.Reader, execID, nil
}

// InspectExec checks the exit code of an executed command
func (d *DockerClient) InspectExec(ctx context.Context, execID string) (container.ExecInspect, error) {
	return d.cli.ContainerExecInspect(ctx, execID)
}

// CopyFromContainer copies a file or directory from a container as a tar archive stream
func (d *DockerClient) CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error) {
	return d.cli.CopyFromContainer(ctx, containerID, srcPath)
}

// CopyToContainer copies a tar archive stream into a container
func (d *DockerClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error {
	return d.cli.CopyToContainer(ctx, containerID, dstPath, content, options)
}
