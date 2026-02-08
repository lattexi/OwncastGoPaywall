package srs

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// ConfigGenerator generates and manages SRS configuration
type ConfigGenerator struct {
	srsAPIUrl        string
	configVolumePath string // shared volume path for srs.conf
	callbackURL      string // Go server URL for webhooks
	pgStore          *storage.PostgresStore
	client           *http.Client
}

// NewConfigGenerator creates a new SRS config generator
func NewConfigGenerator(srsAPIUrl, configVolumePath, callbackURL string, pgStore *storage.PostgresStore) *ConfigGenerator {
	return &ConfigGenerator{
		srsAPIUrl:        srsAPIUrl,
		configVolumePath: configVolumePath,
		callbackURL:      callbackURL,
		pgStore:          pgStore,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GenerateAndReload generates the SRS config file and tells SRS to reload
func (g *ConfigGenerator) GenerateAndReload(ctx context.Context) error {
	config, err := g.generateConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to generate SRS config: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(g.configVolumePath, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write config file
	configPath := filepath.Join(g.configVolumePath, "srs.conf")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write SRS config: %w", err)
	}

	log.Info().Str("path", configPath).Msg("SRS config written")

	// Reload SRS
	if err := g.reload(); err != nil {
		log.Warn().Err(err).Msg("Failed to reload SRS (may not be running yet)")
		return nil // Don't fail - SRS may not be running yet on first startup
	}

	return nil
}

// generateConfig creates the SRS configuration content
func (g *ConfigGenerator) generateConfig(ctx context.Context) (string, error) {
	streams, err := g.pgStore.ListStreams(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list streams: %w", err)
	}

	var b strings.Builder

	// Global config
	b.WriteString("listen 1935;\n")
	b.WriteString("max_connections 1000;\n")
	b.WriteString("daemon off;\n")
	b.WriteString("srs_log_tank console;\n")
	b.WriteString("\n")

	// HTTP API
	b.WriteString("http_api {\n")
	b.WriteString("    enabled on;\n")
	b.WriteString("    listen 1985;\n")
	b.WriteString("}\n\n")

	// HTTP server for HLS
	b.WriteString("http_server {\n")
	b.WriteString("    enabled on;\n")
	b.WriteString("    listen 8080;\n")
	b.WriteString("    dir ./objs/nginx/html;\n")
	b.WriteString("}\n\n")

	// Stats
	b.WriteString("stats {\n")
	b.WriteString("    network 0;\n")
	b.WriteString("}\n\n")

	// Vhost with HLS and hooks
	b.WriteString("vhost __defaultVhost__ {\n")

	// HLS
	b.WriteString("    hls {\n")
	b.WriteString("        enabled on;\n")
	b.WriteString("        hls_fragment 6;\n")
	b.WriteString("        hls_window 30;\n")
	b.WriteString("        hls_path ./objs/nginx/html;\n")
	b.WriteString("    }\n\n")

	// HTTP hooks
	b.WriteString("    http_hooks {\n")
	b.WriteString("        enabled on;\n")
	b.WriteString(fmt.Sprintf("        on_publish %s/api/hooks/on_publish;\n", g.callbackURL))
	b.WriteString(fmt.Sprintf("        on_unpublish %s/api/hooks/on_unpublish;\n", g.callbackURL))
	b.WriteString("    }\n\n")

	// Transcode blocks for streams with transcode config
	for _, stream := range streams {
		variants, err := stream.GetTranscodeVariants()
		if err != nil {
			log.Warn().Err(err).Str("stream", stream.Slug).Msg("Failed to parse transcode config")
			continue
		}

		if len(variants) == 0 {
			continue
		}

		// Generate transcode blocks for non-passthrough variants
		for _, v := range variants {
			if v.Passthrough {
				continue
			}

			suffix := strings.ToLower(v.Name)
			if suffix == "" {
				continue
			}

			preset := v.VPreset
			if preset == "" {
				preset = "medium"
			}

			b.WriteString(fmt.Sprintf("    transcode live/%s {\n", stream.StreamKey))
			b.WriteString("        enabled on;\n")
			b.WriteString("        ffmpeg /usr/local/bin/ffmpeg;\n")
			b.WriteString(fmt.Sprintf("        engine %s {\n", suffix))
			b.WriteString("            enabled on;\n")
			b.WriteString("            vcodec libx264;\n")
			b.WriteString("            vprofile main;\n")
			b.WriteString(fmt.Sprintf("            vbitrate %d;\n", v.VBitrate))
			if v.VWidth > 0 && v.VHeight > 0 {
				b.WriteString(fmt.Sprintf("            vwidth %d;\n", v.VWidth))
				b.WriteString(fmt.Sprintf("            vheight %d;\n", v.VHeight))
			}
			if v.VFps > 0 {
				b.WriteString(fmt.Sprintf("            vfps %d;\n", v.VFps))
			}
			b.WriteString(fmt.Sprintf("            vpreset %s;\n", preset))
			b.WriteString("            acodec aac;\n")
			abitrate := v.ABitrate
			if abitrate <= 0 {
				abitrate = 128
			}
			b.WriteString(fmt.Sprintf("            abitrate %d;\n", abitrate))
			b.WriteString(fmt.Sprintf("            output rtmp://127.0.0.1:1935/live/%s_%s;\n", stream.StreamKey, suffix))
			b.WriteString("        }\n")
			b.WriteString("    }\n\n")
		}
	}

	b.WriteString("}\n")

	return b.String(), nil
}

// reload tells SRS to reload its configuration
func (g *ConfigGenerator) reload() error {
	url := fmt.Sprintf("%s/api/v1/raw?rpc=reload", g.srsAPIUrl)
	resp, err := g.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to reload SRS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SRS reload returned status %d", resp.StatusCode)
	}

	log.Info().Msg("SRS config reloaded")
	return nil
}
