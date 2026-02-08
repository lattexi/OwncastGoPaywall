package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// ContainerStatus represents the status of a container
type ContainerStatus string

const (
	StatusStopped  ContainerStatus = "stopped"
	StatusStarting ContainerStatus = "starting"
	StatusRunning  ContainerStatus = "running"
	StatusStopping ContainerStatus = "stopping"
	StatusError    ContainerStatus = "error"
)

// Manager handles Docker container operations
type Manager struct {
	client           *client.Client
	networkName      string
	rtmpPortStart    int
	srsContainerName string
}

// Config holds configuration for the Docker manager
type Config struct {
	DockerHost       string // Docker socket path (e.g., unix:///var/run/docker.sock)
	NetworkName      string // Docker network to join (e.g., "internal")
	RTMPPortStart    int    // RTMP port (e.g., 19350)
	SRSContainerName string // SRS container name (e.g., "paywall-srs")
}

// NewManager creates a new Docker manager
func NewManager(cfg *Config) (*Manager, error) {
	opts := []client.Opt{
		client.WithAPIVersionNegotiation(),
	}

	if cfg.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.DockerHost))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = cli.Ping(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Docker: %w", err)
	}

	return &Manager{
		client:           cli,
		networkName:      cfg.NetworkName,
		rtmpPortStart:    cfg.RTMPPortStart,
		srsContainerName: cfg.SRSContainerName,
	}, nil
}

// Close closes the Docker client
func (m *Manager) Close() error {
	return m.client.Close()
}

// GetClient returns the underlying Docker client for direct access
func (m *Manager) GetClient() *client.Client {
	return m.client
}

// GenerateStreamKey generates a random stream key
func GenerateStreamKey() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GetContainerStatus returns the current status of a container
func (m *Manager) GetContainerStatus(ctx context.Context, containerName string) (ContainerStatus, error) {
	containers, err := m.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return StatusError, err
	}

	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName || name == containerName {
				switch c.State {
				case "running":
					return StatusRunning, nil
				case "created", "restarting":
					return StatusStarting, nil
				case "paused", "exited", "dead":
					return StatusStopped, nil
				default:
					return StatusStopped, nil
				}
			}
		}
	}

	return StatusStopped, nil
}

// IsContainerRunning checks if a container is running
func (m *Manager) IsContainerRunning(ctx context.Context, containerName string) (bool, error) {
	status, err := m.GetContainerStatus(ctx, containerName)
	if err != nil {
		return false, err
	}
	return status == StatusRunning, nil
}

// IsSRSRunning checks if the SRS container is running
func (m *Manager) IsSRSRunning(ctx context.Context) (bool, error) {
	if m.srsContainerName == "" {
		log.Warn().Msg("SRS container name not configured")
		return false, nil
	}
	return m.IsContainerRunning(ctx, m.srsContainerName)
}

// GetRTMPURL returns the RTMP URL for streaming
func GetRTMPURL(host string, port int) string {
	return fmt.Sprintf("rtmp://%s:%d/live", host, port)
}
