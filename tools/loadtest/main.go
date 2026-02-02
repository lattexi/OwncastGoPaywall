package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// Config holds test configuration
type Config struct {
	BaseURL  string
	StreamID string
	Token    string
}

// Metrics holds test metrics
type Metrics struct {
	mu sync.Mutex

	PlaylistRequests  int64
	PlaylistSuccesses int64
	PlaylistErrors    int64
	PlaylistLatencies []time.Duration

	SegmentRequests  int64
	SegmentSuccesses int64
	SegmentErrors    int64
	SegmentLatencies []time.Duration

	HeartbeatRequests  int64
	HeartbeatSuccesses int64
	HeartbeatErrors    int64
	HeartbeatLatencies []time.Duration

	ErrorMessages map[string]int
}

func NewMetrics() *Metrics {
	return &Metrics{
		PlaylistLatencies:  make([]time.Duration, 0),
		SegmentLatencies:   make([]time.Duration, 0),
		HeartbeatLatencies: make([]time.Duration, 0),
		ErrorMessages:      make(map[string]int),
	}
}

func (m *Metrics) RecordPlaylist(latency time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PlaylistRequests++
	if err != nil {
		m.PlaylistErrors++
		m.ErrorMessages[err.Error()]++
	} else {
		m.PlaylistSuccesses++
		m.PlaylistLatencies = append(m.PlaylistLatencies, latency)
	}
}

func (m *Metrics) RecordSegment(latency time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SegmentRequests++
	if err != nil {
		m.SegmentErrors++
		m.ErrorMessages[err.Error()]++
	} else {
		m.SegmentSuccesses++
		m.SegmentLatencies = append(m.SegmentLatencies, latency)
	}
}

func (m *Metrics) RecordHeartbeat(latency time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HeartbeatRequests++
	if err != nil {
		m.HeartbeatErrors++
		m.ErrorMessages[err.Error()]++
	} else {
		m.HeartbeatSuccesses++
		m.HeartbeatLatencies = append(m.HeartbeatLatencies, latency)
	}
}

// Stats calculates statistics for a slice of durations
type Stats struct {
	Count int
	Min   time.Duration
	Max   time.Duration
	Avg   time.Duration
	P50   time.Duration
	P95   time.Duration
	P99   time.Duration
}

func calculateStats(latencies []time.Duration) Stats {
	if len(latencies) == 0 {
		return Stats{}
	}

	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, l := range sorted {
		total += l
	}

	return Stats{
		Count: len(sorted),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		Avg:   total / time.Duration(len(sorted)),
		P50:   sorted[len(sorted)*50/100],
		P95:   sorted[len(sorted)*95/100],
		P99:   sorted[len(sorted)*99/100],
	}
}

// Viewer simulates a single HLS viewer
type Viewer struct {
	id       int
	config   Config
	client   *http.Client
	metrics  *Metrics
	deviceID string
}

func NewViewer(id int, config Config, metrics *Metrics) *Viewer {
	return &Viewer{
		id:      id,
		config:  config,
		metrics: metrics,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		// Use same device ID for all viewers to avoid "Another device watching" errors
		// We're testing server performance, not device enforcement
		deviceID: "loadtest-shared-device",
	}
}

var segmentURLRegex = regexp.MustCompile(`^[^#].*\.(ts|m4s)(\?.*)?`)
var variantPlaylistRegex = regexp.MustCompile(`^[^#].*\.m3u8(\?.*)?`)

func (v *Viewer) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	playlistTicker := time.NewTicker(2 * time.Second)
	defer playlistTicker.Stop()

	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	// Send initial heartbeat to register device
	v.sendHeartbeat(ctx)

	// Initial playlist fetch
	v.fetchPlaylistAndSegments(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-playlistTicker.C:
			v.fetchPlaylistAndSegments(ctx)
		case <-heartbeatTicker.C:
			v.sendHeartbeat(ctx)
		}
	}
}

func (v *Viewer) fetchPlaylistAndSegments(ctx context.Context) {
	// Generate playlist URL with token (no signing needed - validated via Redis)
	playlistURL := fmt.Sprintf("%s/stream/%s/hls/stream.m3u8?token=%s", v.config.BaseURL, v.config.StreamID, v.config.Token)

	start := time.Now()
	resp, err := v.client.Get(playlistURL)
	latency := time.Since(start)

	if err != nil {
		v.metrics.RecordPlaylist(latency, fmt.Errorf("network: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		v.metrics.RecordPlaylist(latency, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 50)))
		return
	}

	v.metrics.RecordPlaylist(latency, nil)

	// Parse master playlist for variant playlists or segments
	body, _ := io.ReadAll(resp.Body)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	var variantURL string
	var segmentURL string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Check for variant playlist (e.g., "0/stream.m3u8?...")
		if variantPlaylistRegex.MatchString(line) {
			variantURL = line
		}
		// Check for segment directly
		if segmentURLRegex.MatchString(line) {
			segmentURL = line
		}
	}

	// If we found a variant playlist, fetch it to get segments
	if variantURL != "" && segmentURL == "" {
		segmentURL = v.fetchVariantPlaylist(ctx, variantURL)
	}

	if segmentURL != "" {
		v.fetchSegment(ctx, segmentURL)
	}
}

func (v *Viewer) fetchVariantPlaylist(ctx context.Context, variantPath string) string {
	variantURL := v.config.BaseURL + variantPath

	resp, err := v.client.Get(variantURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, _ := io.ReadAll(resp.Body)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	var segmentURL string

	for scanner.Scan() {
		line := scanner.Text()
		if segmentURLRegex.MatchString(line) {
			segmentURL = line
		}
	}

	return segmentURL
}

func (v *Viewer) fetchSegment(ctx context.Context, segmentPath string) {
	// The segment URL from playlist already has signature
	segmentURL := v.config.BaseURL + segmentPath

	start := time.Now()
	resp, err := v.client.Get(segmentURL)
	latency := time.Since(start)

	if err != nil {
		v.metrics.RecordSegment(latency, fmt.Errorf("network: %v", err))
		return
	}
	defer resp.Body.Close()

	// Drain body
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		v.metrics.RecordSegment(latency, fmt.Errorf("status %d", resp.StatusCode))
		return
	}

	v.metrics.RecordSegment(latency, nil)
}

func (v *Viewer) sendHeartbeat(ctx context.Context) {
	heartbeatURL := fmt.Sprintf("%s/api/stream/%s/heartbeat", v.config.BaseURL, v.config.StreamID)

	body := fmt.Sprintf(`{"device_id":"%s"}`, v.deviceID)
	req, err := http.NewRequestWithContext(ctx, "POST", heartbeatURL, strings.NewReader(body))
	if err != nil {
		v.metrics.RecordHeartbeat(0, err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", fmt.Sprintf("access_token=%s", v.config.Token))

	start := time.Now()
	resp, err := v.client.Do(req)
	latency := time.Since(start)

	if err != nil {
		v.metrics.RecordHeartbeat(latency, fmt.Errorf("network: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		v.metrics.RecordHeartbeat(latency, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(respBody), 50)))
		return
	}

	v.metrics.RecordHeartbeat(latency, nil)
}

// TestResult holds results for a single test run
type TestResult struct {
	Viewers  int
	Duration time.Duration
	Metrics  *Metrics

	PlaylistStats  Stats
	SegmentStats   Stats
	HeartbeatStats Stats

	PlaylistRPS  float64
	SegmentRPS   float64
	HeartbeatRPS float64

	SuccessRate float64
}

func runTest(config Config, numViewers int, duration time.Duration) *TestResult {
	fmt.Printf("\n🚀 Starting test with %d viewers for %v...\n", numViewers, duration)

	metrics := NewMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup

	// Start viewers with slight stagger to avoid thundering herd at start
	for i := 0; i < numViewers; i++ {
		wg.Add(1)
		viewer := NewViewer(i, config, metrics)
		go viewer.Run(ctx, &wg)

		// Stagger startup: 10ms between each viewer
		if i < numViewers-1 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	fmt.Printf("   ✓ Started %d viewers\n", numViewers)

	// Progress indicator
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Printf("   ... Playlist: %d, Segments: %d, Heartbeats: %d\n",
					atomic.LoadInt64(&metrics.PlaylistRequests),
					atomic.LoadInt64(&metrics.SegmentRequests),
					atomic.LoadInt64(&metrics.HeartbeatRequests))
			}
		}
	}()

	wg.Wait()

	// Calculate results
	result := &TestResult{
		Viewers:  numViewers,
		Duration: duration,
		Metrics:  metrics,
	}

	result.PlaylistStats = calculateStats(metrics.PlaylistLatencies)
	result.SegmentStats = calculateStats(metrics.SegmentLatencies)
	result.HeartbeatStats = calculateStats(metrics.HeartbeatLatencies)

	seconds := duration.Seconds()
	result.PlaylistRPS = float64(metrics.PlaylistRequests) / seconds
	result.SegmentRPS = float64(metrics.SegmentRequests) / seconds
	result.HeartbeatRPS = float64(metrics.HeartbeatRequests) / seconds

	totalRequests := metrics.PlaylistRequests + metrics.SegmentRequests + metrics.HeartbeatRequests
	totalSuccesses := metrics.PlaylistSuccesses + metrics.SegmentSuccesses + metrics.HeartbeatSuccesses
	if totalRequests > 0 {
		result.SuccessRate = float64(totalSuccesses) / float64(totalRequests) * 100
	}

	return result
}

func printResult(result *TestResult) {
	fmt.Print("\n" + strings.Repeat("=", 70) + "\n")
	fmt.Printf("📊 RESULTS: %d Viewers, %v Duration\n", result.Viewers, result.Duration)
	fmt.Print(strings.Repeat("=", 70) + "\n")

	fmt.Printf("\n📈 Request Summary:\n")
	fmt.Printf("   %-15s %10s %10s %10s %10s\n", "Type", "Total", "Success", "Errors", "RPS")
	fmt.Printf("   %-15s %10d %10d %10d %10.1f\n", "Playlist",
		result.Metrics.PlaylistRequests, result.Metrics.PlaylistSuccesses,
		result.Metrics.PlaylistErrors, result.PlaylistRPS)
	fmt.Printf("   %-15s %10d %10d %10d %10.1f\n", "Segment",
		result.Metrics.SegmentRequests, result.Metrics.SegmentSuccesses,
		result.Metrics.SegmentErrors, result.SegmentRPS)
	fmt.Printf("   %-15s %10d %10d %10d %10.1f\n", "Heartbeat",
		result.Metrics.HeartbeatRequests, result.Metrics.HeartbeatSuccesses,
		result.Metrics.HeartbeatErrors, result.HeartbeatRPS)

	fmt.Printf("\n⏱️  Latency Statistics (ms):\n")
	fmt.Printf("   %-15s %8s %8s %8s %8s %8s %8s\n", "Type", "Min", "Avg", "P50", "P95", "P99", "Max")
	printLatencyRow("Playlist", result.PlaylistStats)
	printLatencyRow("Segment", result.SegmentStats)
	printLatencyRow("Heartbeat", result.HeartbeatStats)

	fmt.Printf("\n✅ Overall Success Rate: %.2f%%\n", result.SuccessRate)

	if len(result.Metrics.ErrorMessages) > 0 {
		fmt.Printf("\n❌ Errors:\n")
		for msg, count := range result.Metrics.ErrorMessages {
			fmt.Printf("   [%d] %s\n", count, truncate(msg, 60))
		}
	}
}

func printLatencyRow(name string, stats Stats) {
	if stats.Count == 0 {
		fmt.Printf("   %-15s %8s %8s %8s %8s %8s %8s\n", name, "-", "-", "-", "-", "-", "-")
		return
	}
	fmt.Printf("   %-15s %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f\n", name,
		float64(stats.Min.Microseconds())/1000,
		float64(stats.Avg.Microseconds())/1000,
		float64(stats.P50.Microseconds())/1000,
		float64(stats.P95.Microseconds())/1000,
		float64(stats.P99.Microseconds())/1000,
		float64(stats.Max.Microseconds())/1000)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func printSummary(results []*TestResult) {
	fmt.Print("\n" + strings.Repeat("=", 70) + "\n")
	fmt.Printf("📋 COMPARISON SUMMARY\n")
	fmt.Print(strings.Repeat("=", 70) + "\n")

	fmt.Printf("\n%-10s %12s %12s %12s %12s\n", "Viewers", "Success%", "Playlist P95", "Segment P95", "Total RPS")
	for _, r := range results {
		fmt.Printf("%-10d %11.2f%% %11.1fms %11.1fms %12.1f\n",
			r.Viewers,
			r.SuccessRate,
			float64(r.PlaylistStats.P95.Microseconds())/1000,
			float64(r.SegmentStats.P95.Microseconds())/1000,
			r.PlaylistRPS+r.SegmentRPS+r.HeartbeatRPS)
	}

	// Check for degradation
	fmt.Printf("\n🔍 Analysis:\n")
	if len(results) >= 2 {
		baseline := results[0]
		for i := 1; i < len(results); i++ {
			r := results[i]

			// Check latency degradation
			if baseline.PlaylistStats.P95 > 0 {
				latencyIncrease := float64(r.PlaylistStats.P95) / float64(baseline.PlaylistStats.P95)
				if latencyIncrease > 2 {
					fmt.Printf("   ⚠️  %d viewers: Playlist P95 latency %.1fx higher than %d viewers\n",
						r.Viewers, latencyIncrease, baseline.Viewers)
				}
			}

			// Check success rate degradation
			if r.SuccessRate < 99 {
				fmt.Printf("   ⚠️  %d viewers: Success rate dropped to %.2f%%\n",
					r.Viewers, r.SuccessRate)
			}
		}

		lastResult := results[len(results)-1]
		if lastResult.SuccessRate >= 99 && lastResult.PlaylistStats.P95 < 500*time.Millisecond {
			fmt.Printf("   ✅ System handled %d viewers well!\n", lastResult.Viewers)
		}
	}
}

func exportResults(results []*TestResult) {
	type ExportData struct {
		Timestamp string        `json:"timestamp"`
		Results   []interface{} `json:"results"`
	}

	var exportResults []interface{}
	for _, r := range results {
		exportResults = append(exportResults, map[string]interface{}{
			"viewers":          r.Viewers,
			"duration_seconds": r.Duration.Seconds(),
			"success_rate":     r.SuccessRate,
			"playlist": map[string]interface{}{
				"total":   r.Metrics.PlaylistRequests,
				"success": r.Metrics.PlaylistSuccesses,
				"errors":  r.Metrics.PlaylistErrors,
				"rps":     r.PlaylistRPS,
				"p50_ms":  float64(r.PlaylistStats.P50.Microseconds()) / 1000,
				"p95_ms":  float64(r.PlaylistStats.P95.Microseconds()) / 1000,
				"p99_ms":  float64(r.PlaylistStats.P99.Microseconds()) / 1000,
			},
			"segment": map[string]interface{}{
				"total":   r.Metrics.SegmentRequests,
				"success": r.Metrics.SegmentSuccesses,
				"errors":  r.Metrics.SegmentErrors,
				"rps":     r.SegmentRPS,
				"p50_ms":  float64(r.SegmentStats.P50.Microseconds()) / 1000,
				"p95_ms":  float64(r.SegmentStats.P95.Microseconds()) / 1000,
				"p99_ms":  float64(r.SegmentStats.P99.Microseconds()) / 1000,
			},
			"heartbeat": map[string]interface{}{
				"total":   r.Metrics.HeartbeatRequests,
				"success": r.Metrics.HeartbeatSuccesses,
				"errors":  r.Metrics.HeartbeatErrors,
				"rps":     r.HeartbeatRPS,
				"p50_ms":  float64(r.HeartbeatStats.P50.Microseconds()) / 1000,
				"p95_ms":  float64(r.HeartbeatStats.P95.Microseconds()) / 1000,
				"p99_ms":  float64(r.HeartbeatStats.P99.Microseconds()) / 1000,
			},
		})
	}

	data := ExportData{
		Timestamp: time.Now().Format(time.RFC3339),
		Results:   exportResults,
	}

	jsonData, _ := json.MarshalIndent(data, "", "  ")
	filename := fmt.Sprintf("loadtest_results_%s.json", time.Now().Format("20060102_150405"))
	os.WriteFile(filename, jsonData, 0644)
	fmt.Printf("\n📁 Results exported to: %s\n", filename)
}

// generateToken creates a random access token
func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// generateUUID creates a random UUID v4
func generateUUID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	// Set version (4) and variant bits
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// setupTestData creates test payment and session in the database
func setupTestData(ctx context.Context, dbURL, redisURL, streamID string) (string, error) {
	// Connect to PostgreSQL
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return "", fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Generate test token
	token := generateToken()

	// Create test payment
	paymentID := generateUUID()
	_, err = db.ExecContext(ctx, `
		INSERT INTO payments (id, stream_id, email, amount_cents, status, access_token, token_expiry, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (access_token) DO UPDATE SET token_expiry = $7
	`, paymentID, streamID, "loadtest@test.com", 0, "completed", token, time.Now().Add(24*time.Hour), time.Now())
	if err != nil {
		return "", fmt.Errorf("failed to create test payment: %w", err)
	}

	// Connect to Redis
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	// Create session in Redis
	sessionData := map[string]interface{}{
		"token":      token,
		"stream_id":  streamID,
		"email":      "loadtest@test.com",
		"payment_id": paymentID,
		"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}
	sessionJSON, _ := json.Marshal(sessionData)
	err = rdb.Set(ctx, "session:"+token, sessionJSON, 24*time.Hour).Err()
	if err != nil {
		return "", fmt.Errorf("failed to create Redis session: %w", err)
	}

	return token, nil
}

// cleanupTestData removes test data
func cleanupTestData(ctx context.Context, dbURL, redisURL, token string) {
	// Clean PostgreSQL
	db, err := sql.Open("postgres", dbURL)
	if err == nil {
		db.ExecContext(ctx, "DELETE FROM payments WHERE access_token = $1", token)
		db.Close()
	}

	// Clean Redis
	opts, _ := redis.ParseURL(redisURL)
	rdb := redis.NewClient(opts)
	rdb.Del(ctx, "session:"+token)
	rdb.Del(ctx, "device:"+token)
	rdb.Close()
}

func main() {
	// Parse command line flags
	baseURL := flag.String("url", "http://lauri.duckdns.org:3000", "Base URL of the paywall server")
	dbURL := flag.String("db", "", "PostgreSQL connection string (default: from DATABASE_URL env)")
	redisURL := flag.String("redis", "", "Redis connection string (default: from REDIS_URL env)")
	streamSlug := flag.String("stream", "", "Stream slug to test (will find first live stream if not specified)")
	duration := flag.Duration("duration", 30*time.Second, "Test duration per viewer count")
	quick := flag.Bool("quick", false, "Quick test (10s per level)")
	viewersFlag := flag.String("viewers", "10,100,1000", "Comma-separated viewer counts to test")

	flag.Parse()

	// Get config from flags or environment
	if *dbURL == "" {
		*dbURL = os.Getenv("DATABASE_URL")
		if *dbURL == "" {
			*dbURL = "postgres://paywall:paywall@localhost:5432/paywall?sslmode=disable"
		}
	}
	if *redisURL == "" {
		*redisURL = os.Getenv("REDIS_URL")
		if *redisURL == "" {
			*redisURL = "redis://localhost:6379"
		}
	}

	if *quick {
		*duration = 10 * time.Second
	}

	// Parse viewer counts
	var viewerCounts []int
	for _, v := range strings.Split(*viewersFlag, ",") {
		var count int
		fmt.Sscanf(strings.TrimSpace(v), "%d", &count)
		if count > 0 {
			viewerCounts = append(viewerCounts, count)
		}
	}

	ctx := context.Background()

	fmt.Println("🎬 Stream Paywall Load Tester")
	fmt.Println(strings.Repeat("=", 70))

	// Find a live stream
	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to connect to database: %v\n", err)
		os.Exit(1)
	}

	var streamID string
	var streamTitle string
	query := "SELECT id, title FROM streams WHERE status = 'live'"
	if *streamSlug != "" {
		query += " AND slug = $1"
		err = db.QueryRowContext(ctx, query, *streamSlug).Scan(&streamID, &streamTitle)
	} else {
		query += " LIMIT 1"
		err = db.QueryRowContext(ctx, query).Scan(&streamID, &streamTitle)
	}
	db.Close()

	if err != nil {
		fmt.Printf("❌ No live stream found. Make sure a stream is set to 'live' status.\n")
		os.Exit(1)
	}

	fmt.Printf("Target: %s\n", *baseURL)
	fmt.Printf("Stream: %s (%s)\n", streamTitle, streamID)
	fmt.Printf("Duration per test: %v\n", *duration)
	fmt.Printf("Viewer counts: %v\n", viewerCounts)

	// Setup test data
	fmt.Println("\n⚙️  Setting up test data...")
	token, err := setupTestData(ctx, *dbURL, *redisURL, streamID)
	if err != nil {
		fmt.Printf("❌ Failed to setup test data: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   ✓ Created test token: %s...\n", token[:16])

	// Cleanup on exit
	defer func() {
		fmt.Println("\n🧹 Cleaning up test data...")
		cleanupTestData(ctx, *dbURL, *redisURL, token)
	}()

	config := Config{
		BaseURL:  *baseURL,
		StreamID: streamID,
		Token:    token,
	}

	var results []*TestResult

	for _, count := range viewerCounts {
		result := runTest(config, count, *duration)
		results = append(results, result)
		printResult(result)

		// Brief pause between tests
		if count < viewerCounts[len(viewerCounts)-1] {
			fmt.Printf("\n⏸️  Pausing 5 seconds before next test...\n")
			time.Sleep(5 * time.Second)
		}
	}

	printSummary(results)
	exportResults(results)
}
