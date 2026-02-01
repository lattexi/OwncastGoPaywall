package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration for the application
type Config struct {
	// Server
	BaseURL string
	Port    string

	// Paytrail
	PaytrailMerchantID string
	PaytrailSecretKey  string

	// Security
	SigningSecret     string
	SessionDuration   time.Duration
	HeartbeatTimeout  time.Duration
	SignatureValidity time.Duration

	// Storage
	DatabaseURL string
	RedisURL    string

	// Admin
	AdminAPIKey string

	// Initial Admin User (for first-time setup)
	AdminInitialUser     string
	AdminInitialPassword string

	// Rate Limiting
	RecoveryRateLimitPerEmail int
	RecoveryRateLimitPerIP    int

	// Docker / Owncast Container Management
	DockerHost          string // Docker socket path (e.g., unix:///var/run/docker.sock)
	DockerNetwork       string // Docker network for containers (e.g., "internal")
	OwncastImage        string // Owncast Docker image
	RTMPPortStart       int    // Starting port for RTMP (e.g., 19350)
	RTMPPublicHost      string // Public hostname for RTMP URLs (shown in admin)
	OwncastAdminPassword string // Owncast admin password (default: "abc123")
}

// Load reads configuration from environment variables
func Load() (*Config, error) {
	cfg := &Config{
		// Server defaults
		BaseURL: getEnv("BASE_URL", "http://localhost:3000"),
		Port:    getEnv("PORT", "3000"),

		// Paytrail (test credentials as default for development)
		PaytrailMerchantID: getEnv("PAYTRAIL_MERCHANT_ID", "375917"),
		PaytrailSecretKey:  getEnv("PAYTRAIL_SECRET_KEY", "SAIPPUAKAUPPIAS"),

		// Security
		SigningSecret: getEnv("SIGNING_SECRET", ""),

		// Storage
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/paywall?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),

		// Admin
		AdminAPIKey: getEnv("ADMIN_API_KEY", ""),

		// Initial Admin User
		AdminInitialUser:     getEnv("ADMIN_INITIAL_USER", ""),
		AdminInitialPassword: getEnv("ADMIN_INITIAL_PASSWORD", ""),

		// Rate Limiting defaults
		RecoveryRateLimitPerEmail: 5,
		RecoveryRateLimitPerIP:    20,

		// Docker defaults
		DockerHost:           getEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
		DockerNetwork:        getEnv("DOCKER_NETWORK", "owncastgopaywall_internal"),
		OwncastImage:         getEnv("OWNCAST_IMAGE", "owncast/owncast:latest"),
		RTMPPortStart:        getEnvInt("RTMP_PORT_START", 19350),
		RTMPPublicHost:       getEnv("RTMP_PUBLIC_HOST", "localhost"),
		OwncastAdminPassword: getEnv("OWNCAST_ADMIN_PASSWORD", "abc123"),
	}

	// Parse durations
	var err error
	cfg.SessionDuration, err = time.ParseDuration(getEnv("SESSION_DURATION", "24h"))
	if err != nil {
		return nil, fmt.Errorf("invalid SESSION_DURATION: %w", err)
	}

	cfg.HeartbeatTimeout, err = time.ParseDuration(getEnv("HEARTBEAT_TIMEOUT", "45s"))
	if err != nil {
		return nil, fmt.Errorf("invalid HEARTBEAT_TIMEOUT: %w", err)
	}

	cfg.SignatureValidity, err = time.ParseDuration(getEnv("SIGNATURE_VALIDITY", "24h"))
	if err != nil {
		return nil, fmt.Errorf("invalid SIGNATURE_VALIDITY: %w", err)
	}

	// Validate required fields
	if cfg.SigningSecret == "" {
		return nil, fmt.Errorf("SIGNING_SECRET is required")
	}

	if cfg.AdminAPIKey == "" {
		return nil, fmt.Errorf("ADMIN_API_KEY is required")
	}

	// Warn about localhost in production
	if os.Getenv("ENV") == "production" {
		if strings.Contains(cfg.BaseURL, "localhost") {
			return nil, fmt.Errorf("BASE_URL contains 'localhost' but ENV=production. Set BASE_URL to your public domain")
		}
		if cfg.RTMPPublicHost == "localhost" {
			return nil, fmt.Errorf("RTMP_PUBLIC_HOST is 'localhost' but ENV=production. Set RTMP_PUBLIC_HOST to your public hostname")
		}
	}

	return cfg, nil
}

// LoadWithDefaults loads config with sensible defaults for development
// Use this only for local development
func LoadWithDefaults() *Config {
	cfg, err := Load()
	if err != nil {
		// For development, use defaults
		return &Config{
			BaseURL:                   getEnv("BASE_URL", "http://localhost:3000"),
			Port:                      getEnv("PORT", "3000"),
			PaytrailMerchantID:        "375917",
			PaytrailSecretKey:         "SAIPPUAKAUPPIAS",
			SigningSecret:             "dev-signing-secret-change-in-production",
			SessionDuration:           24 * time.Hour,
			HeartbeatTimeout:          45 * time.Second,
			SignatureValidity:         24 * time.Hour,
			DatabaseURL:               getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/paywall?sslmode=disable"),
			RedisURL:                  getEnv("REDIS_URL", "redis://localhost:6379"),
			AdminAPIKey:               "dev-admin-key",
			AdminInitialUser:          getEnv("ADMIN_INITIAL_USER", "admin"),
			AdminInitialPassword:      getEnv("ADMIN_INITIAL_PASSWORD", "admin"),
			RecoveryRateLimitPerEmail: 5,
			RecoveryRateLimitPerIP:    20,
			DockerHost:           getEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
			DockerNetwork:        getEnv("DOCKER_NETWORK", "owncastgopaywall_internal"),
			OwncastImage:         getEnv("OWNCAST_IMAGE", "owncast/owncast:latest"),
			RTMPPortStart:        getEnvInt("RTMP_PORT_START", 19350),
			RTMPPublicHost:       getEnv("RTMP_PUBLIC_HOST", "localhost"),
			OwncastAdminPassword: getEnv("OWNCAST_ADMIN_PASSWORD", "abc123"),
		}
	}
	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}
