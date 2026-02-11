package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/docker/docker/client"
)

// Manager handles Docker client for metrics collection
type Manager struct {
	client *client.Client
}

// Config holds configuration for the Docker manager
type Config struct {
	DockerHost string // Docker socket path (e.g., unix:///var/run/docker.sock)
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

	return &Manager{client: cli}, nil
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

// GetRTMPURL returns the RTMP URL for streaming
func GetRTMPURL(host string, port int) string {
	return fmt.Sprintf("rtmp://%s:%d/live", host, port)
}
