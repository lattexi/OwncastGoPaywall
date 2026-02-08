package metrics

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// HealthStatus represents the health status of a component
type HealthStatus string

const (
	HealthStatusHealthy  HealthStatus = "healthy"
	HealthStatusWarning  HealthStatus = "warning"
	HealthStatusCritical HealthStatus = "critical"
)

// ContainerMetrics represents metrics for a single container
type ContainerMetrics struct {
	Name          string       `json:"name"`
	ID            string       `json:"id"`
	Status        HealthStatus `json:"status"`
	CPUPercent    float64      `json:"cpuPercent"`
	MemoryUsageMB float64      `json:"memoryUsageMB"`
	MemoryLimitMB float64      `json:"memoryLimitMB"`
	MemoryPercent float64      `json:"memoryPercent"`
	NetworkRxMB   float64      `json:"networkRxMB"`
	NetworkTxMB   float64      `json:"networkTxMB"`
	IsSRS         bool         `json:"isSRS"`
}

// RedisMetrics represents Redis server metrics
type RedisMetrics struct {
	UsedMemoryMB     float64      `json:"usedMemoryMB"`
	MaxMemoryMB      float64      `json:"maxMemoryMB"`
	MemoryPercent    float64      `json:"memoryPercent"`
	ConnectedClients int          `json:"connectedClients"`
	HitRate          float64      `json:"hitRate"`
	Status           HealthStatus `json:"status"`
}

// PostgresMetrics represents PostgreSQL metrics
type PostgresMetrics struct {
	ActiveConnections int          `json:"activeConnections"`
	IdleConnections   int          `json:"idleConnections"`
	MaxConnections    int          `json:"maxConnections"`
	ConnectionPercent float64      `json:"connectionPercent"`
	Status            HealthStatus `json:"status"`
}

// GoRuntimeMetrics represents Go runtime metrics
type GoRuntimeMetrics struct {
	Goroutines  int     `json:"goroutines"`
	HeapAllocMB float64 `json:"heapAllocMB"`
	HeapSysMB   float64 `json:"heapSysMB"`
	NumGC       uint32  `json:"numGC"`
}

// Alert represents a system alert
type Alert struct {
	Level     HealthStatus `json:"level"`
	Component string       `json:"component"`
	Message   string       `json:"message"`
}

// SystemMetrics represents all collected system metrics
type SystemMetrics struct {
	Timestamp       time.Time          `json:"timestamp"`
	OverallStatus   HealthStatus       `json:"overallStatus"`
	SRSContainers   []ContainerMetrics `json:"srsContainers"`
	OtherContainers []ContainerMetrics `json:"otherContainers"`
	Redis           RedisMetrics       `json:"redis"`
	Postgres        PostgresMetrics    `json:"postgres"`
	GoRuntime       GoRuntimeMetrics   `json:"goRuntime"`
	Alerts          []Alert            `json:"alerts"`
}

// cpuStatsCache stores previous CPU stats for delta calculation
type cpuStatsCache struct {
	totalUsage  uint64
	systemUsage uint64
	timestamp   time.Time
}

// Collector collects metrics from various sources
type Collector struct {
	dockerClient     *client.Client
	redisClient      *redis.Client
	pgPool           *pgxpool.Pool
	srsContainerName string
	cpuStatsCache    map[string]*cpuStatsCache // container ID -> previous stats
	cacheMu          sync.Mutex
}

// NewCollector creates a new metrics collector
func NewCollector(dockerClient *client.Client, redisClient *redis.Client, pgPool *pgxpool.Pool, srsContainerName string) *Collector {
	return &Collector{
		dockerClient:     dockerClient,
		redisClient:      redisClient,
		pgPool:           pgPool,
		srsContainerName: srsContainerName,
		cpuStatsCache:    make(map[string]*cpuStatsCache),
	}
}

// Collect gathers all metrics
func (c *Collector) Collect(ctx context.Context) (*SystemMetrics, error) {
	metrics := &SystemMetrics{
		Timestamp:     time.Now(),
		OverallStatus: HealthStatusHealthy,
		Alerts:        []Alert{},
	}

	// Collect container metrics
	if c.dockerClient != nil {
		srsContainers, other, containerAlerts := c.collectContainerMetrics(ctx)
		metrics.SRSContainers = srsContainers
		metrics.OtherContainers = other
		metrics.Alerts = append(metrics.Alerts, containerAlerts...)
	}

	// Collect Redis metrics
	if c.redisClient != nil {
		redisMetrics, redisAlerts := c.collectRedisMetrics(ctx)
		metrics.Redis = redisMetrics
		metrics.Alerts = append(metrics.Alerts, redisAlerts...)
	}

	// Collect Postgres metrics
	if c.pgPool != nil {
		pgMetrics, pgAlerts := c.collectPostgresMetrics(ctx)
		metrics.Postgres = pgMetrics
		metrics.Alerts = append(metrics.Alerts, pgAlerts...)
	}

	// Collect Go runtime metrics
	metrics.GoRuntime = c.collectGoRuntimeMetrics()

	// Determine overall status based on alerts
	for _, alert := range metrics.Alerts {
		if alert.Level == HealthStatusCritical {
			metrics.OverallStatus = HealthStatusCritical
			break
		}
		if alert.Level == HealthStatusWarning && metrics.OverallStatus != HealthStatusCritical {
			metrics.OverallStatus = HealthStatusWarning
		}
	}

	return metrics, nil
}

// collectContainerMetrics collects metrics from all Docker containers
func (c *Collector) collectContainerMetrics(ctx context.Context) ([]ContainerMetrics, []ContainerMetrics, []Alert) {
	var srsContainers []ContainerMetrics
	var otherContainers []ContainerMetrics
	var alerts []Alert

	containers, err := c.dockerClient.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list containers")
		return srsContainers, otherContainers, alerts
	}

	for _, ctr := range containers {
		// Get container stats
		statsResp, err := c.dockerClient.ContainerStatsOneShot(ctx, ctr.ID)
		if err != nil {
			log.Warn().Err(err).Str("container", ctr.ID[:12]).Msg("Failed to get container stats")
			continue
		}

		var stats container.StatsResponse
		if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
			statsResp.Body.Close()
			continue
		}
		statsResp.Body.Close()

		// Calculate CPU percentage using cached previous stats
		cpuPercent := c.calculateCPUPercentWithCache(ctr.ID, &stats)

		// Calculate memory
		memoryUsageMB := float64(stats.MemoryStats.Usage) / (1024 * 1024)
		memoryLimitMB := float64(stats.MemoryStats.Limit) / (1024 * 1024)
		memoryPercent := 0.0
		if memoryLimitMB > 0 {
			memoryPercent = (memoryUsageMB / memoryLimitMB) * 100
		}

		// Calculate network I/O
		var networkRx, networkTx uint64
		for _, netStats := range stats.Networks {
			networkRx += netStats.RxBytes
			networkTx += netStats.TxBytes
		}

		// Get container name
		name := ctr.ID[:12]
		if len(ctr.Names) > 0 {
			name = strings.TrimPrefix(ctr.Names[0], "/")
		}

		// Check if it's the SRS container
		isSRS := name == c.srsContainerName

		// Determine health status
		status := HealthStatusHealthy
		if isSRS {
			if cpuPercent > 90 {
				status = HealthStatusCritical
				alerts = append(alerts, Alert{
					Level:     HealthStatusCritical,
					Component: name,
					Message:   "CPU usage above 90%",
				})
			} else if cpuPercent > 75 {
				status = HealthStatusWarning
				alerts = append(alerts, Alert{
					Level:     HealthStatusWarning,
					Component: name,
					Message:   "CPU usage above 75%",
				})
			}
		}

		containerMetric := ContainerMetrics{
			Name:          name,
			ID:            ctr.ID[:12],
			Status:        status,
			CPUPercent:    cpuPercent,
			MemoryUsageMB: memoryUsageMB,
			MemoryLimitMB: memoryLimitMB,
			MemoryPercent: memoryPercent,
			NetworkRxMB:   float64(networkRx) / (1024 * 1024),
			NetworkTxMB:   float64(networkTx) / (1024 * 1024),
			IsSRS:         isSRS,
		}

		if isSRS {
			srsContainers = append(srsContainers, containerMetric)
		} else {
			otherContainers = append(otherContainers, containerMetric)
		}
	}

	return srsContainers, otherContainers, alerts
}

// calculateCPUPercentWithCache calculates CPU percentage using cached previous stats
func (c *Collector) calculateCPUPercentWithCache(containerID string, stats *container.StatsResponse) float64 {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()

	// Get number of CPUs
	numCPUs := stats.CPUStats.OnlineCPUs
	if numCPUs == 0 {
		numCPUs = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if numCPUs == 0 {
		numCPUs = 1
	}

	currentCPU := stats.CPUStats.CPUUsage.TotalUsage
	currentSystem := stats.CPUStats.SystemUsage

	// Get cached previous stats
	prev, hasPrev := c.cpuStatsCache[containerID]

	// Update cache with current stats
	c.cpuStatsCache[containerID] = &cpuStatsCache{
		totalUsage:  currentCPU,
		systemUsage: currentSystem,
		timestamp:   time.Now(),
	}

	// If no previous stats, we can't calculate delta yet
	if !hasPrev || prev.totalUsage == 0 || prev.systemUsage == 0 {
		return 0
	}

	// Calculate deltas
	cpuDelta := float64(currentCPU - prev.totalUsage)
	systemDelta := float64(currentSystem - prev.systemUsage)

	// Avoid division by zero and handle counter wraps
	if systemDelta <= 0 || cpuDelta < 0 {
		return 0
	}

	// Standard Docker CPU calculation
	cpuPercent := (cpuDelta / systemDelta) * float64(numCPUs) * 100.0

	return cpuPercent
}

// collectRedisMetrics collects Redis server metrics
func (c *Collector) collectRedisMetrics(ctx context.Context) (RedisMetrics, []Alert) {
	metrics := RedisMetrics{
		Status: HealthStatusHealthy,
	}
	var alerts []Alert

	// Get Redis INFO
	info, err := c.redisClient.Info(ctx, "memory", "clients", "stats").Result()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get Redis info")
		return metrics, alerts
	}

	// Parse INFO output
	infoMap := parseRedisInfo(info)

	// Memory
	if usedMemory, ok := infoMap["used_memory"]; ok {
		var usedBytes int64
		json.Unmarshal([]byte(usedMemory), &usedBytes)
		metrics.UsedMemoryMB = float64(usedBytes) / (1024 * 1024)
	}

	if maxMemory, ok := infoMap["maxmemory"]; ok {
		var maxBytes int64
		json.Unmarshal([]byte(maxMemory), &maxBytes)
		if maxBytes > 0 {
			metrics.MaxMemoryMB = float64(maxBytes) / (1024 * 1024)
			metrics.MemoryPercent = (metrics.UsedMemoryMB / metrics.MaxMemoryMB) * 100
		}
	}

	// Clients
	if clients, ok := infoMap["connected_clients"]; ok {
		var clientCount int
		json.Unmarshal([]byte(clients), &clientCount)
		metrics.ConnectedClients = clientCount
	}

	// Hit rate
	var hits, misses int64
	if h, ok := infoMap["keyspace_hits"]; ok {
		json.Unmarshal([]byte(h), &hits)
	}
	if m, ok := infoMap["keyspace_misses"]; ok {
		json.Unmarshal([]byte(m), &misses)
	}
	if hits+misses > 0 {
		metrics.HitRate = float64(hits) / float64(hits+misses) * 100
	}

	// Check thresholds
	if metrics.MaxMemoryMB > 0 && metrics.MemoryPercent > 80 {
		metrics.Status = HealthStatusWarning
		alerts = append(alerts, Alert{
			Level:     HealthStatusWarning,
			Component: "Redis",
			Message:   "Memory usage above 80%",
		})
	}

	return metrics, alerts
}

// parseRedisInfo parses Redis INFO output into a map
func parseRedisInfo(info string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// collectPostgresMetrics collects PostgreSQL metrics
func (c *Collector) collectPostgresMetrics(ctx context.Context) (PostgresMetrics, []Alert) {
	metrics := PostgresMetrics{
		Status: HealthStatusHealthy,
	}
	var alerts []Alert

	// Get max connections
	var maxConnStr string
	err := c.pgPool.QueryRow(ctx, "SHOW max_connections").Scan(&maxConnStr)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get max_connections")
	} else {
		var maxConns int
		json.Unmarshal([]byte(maxConnStr), &maxConns)
		metrics.MaxConnections = maxConns
	}

	// Get active connections
	rows, err := c.pgPool.Query(ctx, `
		SELECT state, COUNT(*)
		FROM pg_stat_activity
		WHERE datname = current_database()
		GROUP BY state
	`)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get connection stats")
	} else {
		defer rows.Close()
		for rows.Next() {
			var state *string
			var count int
			if err := rows.Scan(&state, &count); err == nil {
				if state != nil && *state == "active" {
					metrics.ActiveConnections = count
				} else if state != nil && *state == "idle" {
					metrics.IdleConnections = count
				}
			}
		}
	}

	// Calculate connection percentage
	totalConnections := metrics.ActiveConnections + metrics.IdleConnections
	if metrics.MaxConnections > 0 {
		metrics.ConnectionPercent = float64(totalConnections) / float64(metrics.MaxConnections) * 100
	}

	// Check thresholds
	if metrics.ConnectionPercent > 80 {
		metrics.Status = HealthStatusWarning
		alerts = append(alerts, Alert{
			Level:     HealthStatusWarning,
			Component: "PostgreSQL",
			Message:   "Connection usage above 80%",
		})
	}

	return metrics, alerts
}

// collectGoRuntimeMetrics collects Go runtime metrics
func (c *Collector) collectGoRuntimeMetrics() GoRuntimeMetrics {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return GoRuntimeMetrics{
		Goroutines:  runtime.NumGoroutine(),
		HeapAllocMB: float64(memStats.HeapAlloc) / (1024 * 1024),
		HeapSysMB:   float64(memStats.HeapSys) / (1024 * 1024),
		NumGC:       memStats.NumGC,
	}
}
