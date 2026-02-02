package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/rs/zerolog/log"
)

// ContainerStatus represents the status of an SRS container
type ContainerStatus string

const (
	StatusStopped  ContainerStatus = "stopped"
	StatusStarting ContainerStatus = "starting"
	StatusRunning  ContainerStatus = "running"
	StatusStopping ContainerStatus = "stopping"
	StatusError    ContainerStatus = "error"
)

// SRS config template
const srsConfigTemplate = `# SRS Configuration for stream: {{.Slug}}
listen              1935;
max_connections     1000;
daemon              off;

http_server {
    enabled         on;
    listen          8080;
    dir             ./objs/nginx/html;
}

http_api {
    enabled         on;
    listen          1985;
}

vhost __defaultVhost__ {
    hls {
        enabled         on;
        hls_fragment    2;
        hls_window      6;
        hls_path        ./objs/nginx/html;
        hls_m3u8_file   live/[stream].m3u8;
        hls_ts_file     live/[stream]-[seq].ts;
        hls_cleanup     on;
    }

    http_hooks {
        enabled         on;
        on_publish      {{.CallbackURL}}/api/srs/on_publish;
        on_unpublish    {{.CallbackURL}}/api/srs/on_unpublish;
    }
}
`

// Manager handles Docker container operations for SRS instances
type Manager struct {
	client        *client.Client
	networkName   string
	srsImage      string
	rtmpPortStart int
	cpuLimit      int64  // CPU limit in nanocpus
	memoryLimit   int64  // Memory limit in MB
	callbackURL   string // Base URL for SRS HTTP callbacks
	configDir     string // Directory to store SRS config files
}

// Config holds configuration for the Docker manager
type Config struct {
	DockerHost    string // Docker socket path (e.g., unix:///var/run/docker.sock)
	NetworkName   string // Docker network to join (e.g., "internal")
	SRSImage      string // SRS image (e.g., ossrs/srs:5)
	RTMPPortStart int    // Starting port for RTMP (e.g., 19350)
	CPULimit      int64  // CPU limit in nanocpus (e.g., 500000000 = 0.5 cores)
	MemoryLimit   int64  // Memory limit in MB (e.g., 512)
	CallbackURL   string // Base URL for SRS HTTP callbacks (e.g., http://paywall:3000)
	ConfigDir     string // Directory to store SRS config files
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

	// Set defaults if not specified
	cpuLimit := cfg.CPULimit
	if cpuLimit <= 0 {
		cpuLimit = 500000000 // Default 0.5 cores
	}
	memoryLimit := cfg.MemoryLimit
	if memoryLimit <= 0 {
		memoryLimit = 512 // Default 512MB
	}

	configDir := cfg.ConfigDir
	if configDir == "" {
		configDir = "/tmp/srs-configs"
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	return &Manager{
		client:        cli,
		networkName:   cfg.NetworkName,
		srsImage:      cfg.SRSImage,
		rtmpPortStart: cfg.RTMPPortStart,
		cpuLimit:      cpuLimit,
		memoryLimit:   memoryLimit,
		callbackURL:   cfg.CallbackURL,
		configDir:     configDir,
	}, nil
}

// Close closes the Docker client
func (m *Manager) Close() error {
	return m.client.Close()
}

// GenerateStreamKey generates a random stream key
func GenerateStreamKey() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// ContainerName generates a container name from a stream slug
func ContainerName(slug string) string {
	return fmt.Sprintf("srs-%s", slug)
}

// VolumeName generates a volume name from a stream slug
func VolumeName(slug string) string {
	return fmt.Sprintf("srs-%s-data", slug)
}

// generateSRSConfig generates the SRS configuration file for a stream
func (m *Manager) generateSRSConfig(slug string) (string, error) {
	tmpl, err := template.New("srs").Parse(srsConfigTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse SRS config template: %w", err)
	}

	data := struct {
		Slug        string
		CallbackURL string
	}{
		Slug:        slug,
		CallbackURL: m.callbackURL,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute SRS config template: %w", err)
	}

	// Write to config file
	configPath := filepath.Join(m.configDir, fmt.Sprintf("srs-%s.conf", slug))
	if err := os.WriteFile(configPath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("failed to write SRS config file: %w", err)
	}

	log.Debug().Str("path", configPath).Str("slug", slug).Msg("Generated SRS config")
	return configPath, nil
}

// CreateAndStartContainer creates and starts an SRS container for a stream
func (m *Manager) CreateAndStartContainer(ctx context.Context, slug, streamKey string, rtmpPort int) error {
	containerName := ContainerName(slug)
	volumeName := VolumeName(slug)

	// Generate SRS config file
	configPath, err := m.generateSRSConfig(slug)
	if err != nil {
		return fmt.Errorf("failed to generate SRS config: %w", err)
	}

	// Pull image if needed
	if err := m.ensureImage(ctx); err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}

	// Check if container already exists
	existing, err := m.getContainer(ctx, containerName)
	if err != nil {
		return err
	}

	if existing != "" {
		// Container exists, just start it
		log.Info().Str("container", containerName).Msg("Container exists, starting...")
		return m.client.ContainerStart(ctx, existing, container.StartOptions{})
	}

	// Create container config
	// SRS uses the config file for stream key validation via HTTP callbacks
	config := &container.Config{
		Image: m.srsImage,
		ExposedPorts: nat.PortSet{
			"8080/tcp": struct{}{},
			"1935/tcp": struct{}{},
			"1985/tcp": struct{}{},
		},
		Labels: map[string]string{
			"managed-by":  "stream-paywall",
			"stream-slug": slug,
		},
	}

	// Host config with port bindings, volume mount, and resource limits
	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			"1935/tcp": []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", rtmpPort),
				},
			},
			// Note: 8080 (HLS) and 1985 (API) are NOT exposed to host, only accessible via internal network
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: volumeName,
				Target: "/usr/local/srs/objs/nginx/html",
			},
			{
				Type:     mount.TypeBind,
				Source:   configPath,
				Target:   "/usr/local/srs/conf/srs.conf",
				ReadOnly: true,
			},
		},
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyUnlessStopped,
		},
		Resources: container.Resources{
			// CPU limit (configurable via SRS_CPU_LIMIT env var, in nanocpus)
			NanoCPUs: m.cpuLimit,
			// Memory limit (configurable via SRS_MEMORY_LIMIT env var, in MB)
			Memory: m.memoryLimit * 1024 * 1024,
			// Memory swap same as memory (no swap)
			MemorySwap: m.memoryLimit * 1024 * 1024,
		},
	}

	// Network config - join internal network
	networkConfig := &network.NetworkingConfig{}
	if m.networkName != "" {
		networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			m.networkName: {},
		}
	}

	// Create container
	resp, err := m.client.ContainerCreate(ctx, config, hostConfig, networkConfig, nil, containerName)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	log.Info().Str("container", containerName).Str("id", resp.ID).Msg("Container created")

	// Start container
	if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	log.Info().Str("container", containerName).Int("rtmp_port", rtmpPort).Msg("Container started")

	return nil
}

// StopContainer stops a running container
func (m *Manager) StopContainer(ctx context.Context, containerName string) error {
	containerID, err := m.getContainer(ctx, containerName)
	if err != nil {
		return err
	}
	if containerID == "" {
		return nil // Container doesn't exist
	}

	timeout := 30
	if err := m.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	log.Info().Str("container", containerName).Msg("Container stopped")
	return nil
}

// RemoveContainer stops and removes a container and its volume
func (m *Manager) RemoveContainer(ctx context.Context, slug string) error {
	containerName := ContainerName(slug)
	volumeName := VolumeName(slug)

	containerID, err := m.getContainer(ctx, containerName)
	if err != nil {
		return err
	}

	if containerID != "" {
		// Stop if running
		timeout := 10
		m.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})

		// Remove container
		if err := m.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
			log.Warn().Err(err).Str("container", containerName).Msg("Failed to remove container")
		} else {
			log.Info().Str("container", containerName).Msg("Container removed")
		}
	}

	// Remove volume
	if err := m.client.VolumeRemove(ctx, volumeName, true); err != nil {
		log.Warn().Err(err).Str("volume", volumeName).Msg("Failed to remove volume")
	} else {
		log.Info().Str("volume", volumeName).Msg("Volume removed")
	}

	// Remove config file
	configPath := filepath.Join(m.configDir, fmt.Sprintf("srs-%s.conf", slug))
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("config", configPath).Msg("Failed to remove config file")
	}

	return nil
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

// getContainer returns the container ID if it exists
func (m *Manager) getContainer(ctx context.Context, containerName string) (string, error) {
	containers, err := m.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}

	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName || name == containerName {
				return c.ID, nil
			}
		}
	}

	return "", nil
}

// ensureImage pulls the SRS image if not present
func (m *Manager) ensureImage(ctx context.Context) error {
	images, err := m.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return err
	}

	// Check if image exists
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == m.srsImage {
				return nil
			}
		}
	}

	// Pull image
	log.Info().Str("image", m.srsImage).Msg("Pulling SRS image...")
	reader, err := m.client.ImagePull(ctx, m.srsImage, image.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()

	// Wait for pull to complete
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return err
	}

	log.Info().Str("image", m.srsImage).Msg("Image pulled successfully")
	return nil
}

// GetInternalURL returns the internal URL for the SRS container (used by paywall proxy)
func GetInternalURL(containerName string) string {
	return fmt.Sprintf("http://%s:8080", containerName)
}

// GetRTMPURL returns the RTMP URL for streaming
// For SRS, the stream name is the slug and the key is passed as a query parameter
func GetRTMPURL(host string, port int, slug string) string {
	return fmt.Sprintf("rtmp://%s:%d/live/%s?key=YOUR_STREAM_KEY", host, port, slug)
}
