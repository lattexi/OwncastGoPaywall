package srs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// Manager handles SRS configuration and reload
type Manager struct {
	configPath  string // Path to write srs.conf
	apiURL      string // SRS API URL for reload (e.g., http://paywall-srs:1985)
	webhookBase string // Base URL for webhook callbacks (e.g., http://paywall:3000)
	pgStore     *storage.PostgresStore
	client      *http.Client
}

// NewManager creates a new SRS manager
func NewManager(configPath, apiURL, webhookBase string, pgStore *storage.PostgresStore) *Manager {
	return &Manager{
		configPath:  configPath,
		apiURL:      apiURL,
		webhookBase: webhookBase,
		pgStore:     pgStore,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GenerateConfig generates srs.conf content
func (m *Manager) GenerateConfig() string {
	var b strings.Builder

	b.WriteString("listen 19350;\n")
	b.WriteString("max_connections 1000;\n")
	b.WriteString("daemon off;\n")
	b.WriteString("srs_log_tank console;\n")
	b.WriteString("\n")

	b.WriteString("http_server {\n")
	b.WriteString("    enabled on;\n")
	b.WriteString("    listen 8080;\n")
	b.WriteString("    dir ./objs/nginx/html;\n")
	b.WriteString("}\n\n")

	b.WriteString("http_api {\n")
	b.WriteString("    enabled on;\n")
	b.WriteString("    listen 1985;\n")
	b.WriteString("}\n\n")

	b.WriteString("vhost __defaultVhost__ {\n")
	b.WriteString("    hls {\n")
	b.WriteString("        enabled on;\n")
	b.WriteString("        hls_path ./objs/nginx/html;\n")
	b.WriteString("        hls_fragment 2;\n")
	b.WriteString("        hls_window 10;\n")
	b.WriteString("    }\n\n")

	b.WriteString("    http_hooks {\n")
	b.WriteString("        enabled on;\n")
	b.WriteString(fmt.Sprintf("        on_publish %s/hooks/srs/on_publish;\n", m.webhookBase))
	b.WriteString(fmt.Sprintf("        on_unpublish %s/hooks/srs/on_unpublish;\n", m.webhookBase))
	b.WriteString("    }\n")

	// Generate transcode blocks from DB
	m.writeTranscodeBlocks(&b)

	b.WriteString("}\n")

	return b.String()
}

// writeTranscodeBlocks fetches streams from DB and writes transcode blocks for each
func (m *Manager) writeTranscodeBlocks(b *strings.Builder) {
	if m.pgStore == nil {
		return
	}

	streams, err := m.pgStore.GetAllStreams(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch streams for transcode config")
		return
	}

	for _, stream := range streams {
		if len(stream.TranscodeConfig) == 0 {
			continue
		}

		var variants []models.TranscodeVariant
		if err := json.Unmarshal(stream.TranscodeConfig, &variants); err != nil {
			log.Error().Err(err).Str("stream_key", stream.StreamKey).Msg("Failed to parse transcode config")
			continue
		}

		if len(variants) == 0 {
			continue
		}

		b.WriteString(fmt.Sprintf("\n    transcode live/%s {\n", stream.StreamKey))
		b.WriteString("        enabled on;\n")
		b.WriteString("        ffmpeg /usr/bin/ffmpeg;\n")

		for _, v := range variants {
			b.WriteString(fmt.Sprintf("\n        engine %s {\n", v.Name))
			b.WriteString("            enabled on;\n")
			b.WriteString("            vcodec libx264;\n")
			b.WriteString(fmt.Sprintf("            vbitrate %d;\n", v.VideoBitrate))
			b.WriteString(fmt.Sprintf("            vfps %d;\n", v.Framerate))
			b.WriteString("            vwidth 0;\n")
			b.WriteString(fmt.Sprintf("            vheight %d;\n", v.ScaleHeight))
			b.WriteString("            vthreads 2;\n")
			b.WriteString("            vprofile baseline;\n")
			b.WriteString("            vpreset superfast;\n")
			b.WriteString("            acodec aac;\n")
			b.WriteString(fmt.Sprintf("            abitrate %d;\n", v.AudioBitrate))
			b.WriteString("            asample_rate 44100;\n")
			b.WriteString("            achannels 2;\n")
			b.WriteString(fmt.Sprintf("            output rtmp://127.0.0.1:19350/live/%s_%s;\n", stream.StreamKey, v.Name))
			b.WriteString("        }\n")
		}

		b.WriteString("    }\n")
	}
}

// WriteAndReload writes srs.conf and calls the SRS reload API
func (m *Manager) WriteAndReload() error {
	config := m.GenerateConfig()

	// Ensure directory exists
	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write config file
	if err := os.WriteFile(m.configPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write SRS config: %w", err)
	}

	log.Info().Str("path", m.configPath).Msg("SRS config written")

	// Call SRS reload API
	if err := m.reload(); err != nil {
		log.Warn().Err(err).Msg("Failed to reload SRS config (SRS may not be running yet)")
		return nil // Don't fail if SRS isn't running yet
	}

	return nil
}

// reload calls the SRS reload API
func (m *Manager) reload() error {
	url := m.apiURL + "/api/v1/raw?rpc=reload"
	resp, err := m.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to call SRS reload API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SRS reload API returned status %d", resp.StatusCode)
	}

	log.Info().Msg("SRS config reloaded")
	return nil
}
