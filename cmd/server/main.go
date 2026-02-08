package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/docker"
	"github.com/laurikarhu/stream-paywall/internal/handlers"
	"github.com/laurikarhu/stream-paywall/internal/metrics"
	"github.com/laurikarhu/stream-paywall/internal/middleware"
	"github.com/laurikarhu/stream-paywall/internal/srs"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Set up logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	if os.Getenv("ENV") != "production" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load config, using defaults for development")
		cfg = config.LoadWithDefaults()
	}

	log.Info().
		Str("port", cfg.Port).
		Str("base_url", cfg.BaseURL).
		Str("rtmp_public_host", cfg.RTMPPublicHost).
		Str("srs_container", cfg.SRSContainerName).
		Msg("Starting stream paywall server")

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize PostgreSQL
	pgStore, err := storage.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to PostgreSQL")
	}
	defer pgStore.Close()
	log.Info().Msg("Connected to PostgreSQL")

	// Initialize Redis
	redisStore, err := storage.NewRedisStore(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to Redis")
	}
	defer redisStore.Close()
	log.Info().Msg("Connected to Redis")

	// Create initial admin user if configured and no admins exist
	createInitialAdminUser(ctx, cfg, pgStore)

	// Initialize Docker manager (optional - may not be available in all environments)
	var dockerMgr *docker.Manager
	dockerMgr, err = docker.NewManager(&docker.Config{
		DockerHost:       cfg.DockerHost,
		NetworkName:      cfg.DockerNetwork,
		RTMPPortStart:    cfg.RTMPPortStart,
		SRSContainerName: cfg.SRSContainerName,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Docker manager not available - container metrics disabled")
		dockerMgr = nil
	} else {
		defer dockerMgr.Close()
		log.Info().Msg("Docker manager initialized")
	}

	// Initialize SRS config generator
	// callbackURL is how SRS reaches the Go server (internal Docker network)
	callbackURL := "http://paywall:3000"
	if os.Getenv("ENV") != "production" {
		callbackURL = cfg.BaseURL
	}

	srsConfig := srs.NewConfigGenerator(cfg.SRSAPIUrl, cfg.SRSConfigVolumePath, callbackURL, pgStore)

	// Generate initial SRS config
	if err := srsConfig.GenerateAndReload(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to generate initial SRS config (SRS may not be running)")
	}

	// Initialize handlers
	paymentHandler := handlers.NewPaymentHandler(cfg, pgStore, redisStore)
	recoveryHandler := handlers.NewRecoveryHandler(cfg, pgStore, redisStore)
	streamHandler := handlers.NewStreamHandler(cfg, pgStore, redisStore)
	adminHandler := handlers.NewAdminHandler(cfg, pgStore, redisStore)

	// Find template directory
	templateDir := findTemplateDir()
	pageHandler, err := handlers.NewPageHandler(cfg, pgStore, redisStore, templateDir)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize page handler")
	}

	// Initialize middleware
	adminAPIMiddleware := middleware.NewAdminMiddleware(cfg)
	adminSessionMiddleware := middleware.NewAdminSessionMiddleware(pgStore, redisStore)

	// Initialize admin page handler (with SRS config instead of Docker manager)
	adminPageHandler, err := handlers.NewAdminPageHandler(cfg, pgStore, redisStore, templateDir, adminSessionMiddleware, srsConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize admin page handler")
	}

	// Initialize SRS handlers
	srsHooksHandler := handlers.NewSRSHooksHandler(pgStore)
	srsSettingsHandler := handlers.NewSRSSettingsHandler(cfg, pgStore, srsConfig, adminSessionMiddleware)

	// Initialize metrics collector and handler
	var metricsCollector *metrics.Collector
	if dockerMgr != nil {
		metricsCollector = metrics.NewCollector(dockerMgr.GetClient(), redisStore.GetClient(), pgStore.GetPool(), cfg.SRSContainerName)
	} else {
		metricsCollector = metrics.NewCollector(nil, redisStore.GetClient(), pgStore.GetPool(), cfg.SRSContainerName)
	}
	metricsHandler := handlers.NewMetricsHandler(metricsCollector)

	// Create router
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Static files with cache headers
	staticDir := findStaticDir()
	fs := http.FileServer(http.Dir(staticDir))
	cachedFS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		http.StripPrefix("/static/", fs).ServeHTTP(w, r)
	})
	mux.Handle("GET /static/", cachedFS)

	// Public API endpoints
	mux.HandleFunc("GET /api/streams", streamHandler.ListStreams)
	mux.HandleFunc("GET /api/streams/{slug}", streamHandler.GetStreamInfo)
	mux.HandleFunc("POST /api/payment/create", paymentHandler.CreatePayment)
	mux.HandleFunc("POST /api/payment/recover", recoveryHandler.RecoverToken)
	mux.HandleFunc("GET /api/callback/success", paymentHandler.HandleSuccessCallback)
	mux.HandleFunc("GET /api/callback/cancel", paymentHandler.HandleCancelCallback)
	mux.HandleFunc("POST /api/stream/{id}/heartbeat", streamHandler.Heartbeat)
	mux.HandleFunc("GET /api/stream/{slug}/playlist", streamHandler.GetPlaylistURL)

	// SRS webhook endpoints (called by SRS, no auth needed - internal network only)
	mux.HandleFunc("POST /api/hooks/on_publish", srsHooksHandler.OnPublish)
	mux.HandleFunc("POST /api/hooks/on_unpublish", srsHooksHandler.OnUnpublish)

	// HLS proxy (protected by signed URLs)
	mux.HandleFunc("GET /stream/{id}/hls/{path...}", streamHandler.ServeHLS)

	// Admin API endpoints (protected by API key) - for programmatic access
	mux.Handle("GET /api/admin/streams", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.ListStreams)))
	mux.Handle("POST /api/admin/streams", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.CreateStream)))
	mux.Handle("GET /api/admin/streams/{id}", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.GetStream)))
	mux.Handle("PUT /api/admin/streams/{id}", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.UpdateStream)))
	mux.Handle("PATCH /api/admin/streams/{id}/status", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.UpdateStreamStatus)))
	mux.Handle("DELETE /api/admin/streams/{id}", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.DeleteStream)))
	mux.Handle("GET /api/admin/streams/{id}/viewers", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.GetViewerCount)))
	mux.Handle("GET /api/admin/streams/{id}/payments", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.ListPayments)))
	mux.Handle("GET /api/admin/streams/{id}/whitelist", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.ListWhitelist)))
	mux.Handle("POST /api/admin/streams/{id}/whitelist", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.AddToWhitelist)))
	mux.Handle("DELETE /api/admin/streams/{id}/whitelist/{email}", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.RemoveFromWhitelist)))
	mux.Handle("GET /api/admin/stats", adminAPIMiddleware.RequireAdmin(http.HandlerFunc(adminHandler.GetStats)))

	// Admin Web UI routes (protected by session)
	mux.HandleFunc("GET /admin/login", adminPageHandler.ShowLogin)
	mux.HandleFunc("POST /admin/login", adminPageHandler.ProcessLogin)
	mux.HandleFunc("GET /admin/logout", adminPageHandler.Logout)

	// Protected admin pages
	mux.Handle("GET /admin", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.Dashboard)))
	mux.Handle("GET /admin/streams", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.ListStreams)))
	mux.Handle("GET /admin/streams/new", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.NewStreamForm)))
	mux.Handle("POST /admin/streams", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.CreateStream)))
	mux.Handle("GET /admin/streams/{id}/edit", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.EditStreamForm)))
	mux.Handle("POST /admin/streams/{id}", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.UpdateStream)))
	mux.Handle("POST /admin/streams/{id}/status", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.UpdateStreamStatus)))
	mux.Handle("POST /admin/streams/{id}/delete", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.DeleteStream)))
	mux.Handle("GET /admin/streams/{id}/payments", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.StreamPayments)))

	// SRS settings routes (replaces Owncast settings)
	mux.Handle("GET /admin/api/streams/{id}/srs/settings", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(srsSettingsHandler.GetVideoSettings)))
	mux.Handle("POST /admin/api/streams/{id}/srs/settings", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(srsSettingsHandler.UpdateVideoSettings)))

	// Admin API for AJAX requests (protected by session)
	mux.Handle("GET /admin/api/streams/{id}/viewers", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.GetViewerCountAPI)))

	// Metrics routes
	mux.Handle("GET /admin/metrics", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(adminPageHandler.MetricsPage)))
	mux.Handle("GET /admin/api/metrics", adminSessionMiddleware.RequireAdminSession(http.HandlerFunc(metricsHandler.GetMetrics)))

	// Page routes
	mux.HandleFunc("GET /{$}", pageHandler.Home) // Exact match for root
	mux.HandleFunc("GET /stream/{slug}", pageHandler.Stream)
	mux.HandleFunc("GET /watch/{slug}", pageHandler.Watch)
	mux.HandleFunc("GET /recover/{slug}", pageHandler.Recover)

	// Apply global middleware
	handler := middleware.Recovery(middleware.Logging(mux))

	// Create server with timeouts
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Info().Str("addr", server.Addr).Msg("Server listening")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server failed")
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("Shutting down server...")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Server forced to shutdown")
	}

	log.Info().Msg("Server exited")
}

// createInitialAdminUser creates the initial admin user if configured and no admins exist
func createInitialAdminUser(ctx context.Context, cfg *config.Config, pgStore *storage.PostgresStore) {
	if cfg.AdminInitialUser == "" || cfg.AdminInitialPassword == "" {
		return
	}

	// Check if any admin users exist
	count, err := pgStore.CountAdminUsers(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to check admin user count (table may not exist yet)")
		return
	}

	if count > 0 {
		log.Debug().Int("count", count).Msg("Admin users already exist, skipping initial user creation")
		return
	}

	// Create initial admin user
	user, err := pgStore.CreateAdminUser(ctx, cfg.AdminInitialUser, cfg.AdminInitialPassword)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create initial admin user")
		return
	}

	log.Info().
		Str("username", user.Username).
		Msg("Initial admin user created - please change the password after first login!")
}

// findTemplateDir finds the templates directory
func findTemplateDir() string {
	paths := []string{
		"web/templates",
		"../../web/templates",
		"../../../web/templates",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	return "web/templates"
}

// findStaticDir finds the static files directory
func findStaticDir() string {
	paths := []string{
		"web/static",
		"../../web/static",
		"../../../web/static",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	return "web/static"
}
