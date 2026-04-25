package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

// systemMetricsResponse is the snapshot returned by GET /api/system-metrics.
//
// Three sections, each cheap enough that the whole thing computes in <100ms.
// Frontend polls this endpoint on the Metrics page (no streaming yet).
type systemMetricsResponse struct {
	Process processMetrics `json:"process"`
	Host    hostMetrics    `json:"host"`
	Storage storageMetrics `json:"storage"`
}

// processMetrics describes the running daimon process itself.
// Sourced from runtime.MemStats (cheap, in-process) plus gopsutil/process for
// the CPU% reading (which gopsutil computes from /proc/<pid>/stat deltas).
type processMetrics struct {
	HeapAllocBytes uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64  `json:"heap_sys_bytes"`
	RSSBytes       uint64  `json:"rss_bytes"`
	CPUPercent     float64 `json:"cpu_percent"`
	Goroutines     int     `json:"goroutines"`
	GCPauseMs      float64 `json:"gc_pause_ms"`
	UptimeSec      int64   `json:"uptime_sec"`
}

// hostMetrics describes the machine daimon is running on.
// All disk numbers refer to the filesystem that contains daimon's data dir
// (cfg.Store.Path), since that's the partition users actually care about
// from the dashboard's perspective.
type hostMetrics struct {
	CPUPercent      float64 `json:"cpu_percent"`
	CPUCores        int     `json:"cpu_cores"`
	MemTotalBytes   uint64  `json:"mem_total_bytes"`
	MemUsedBytes    uint64  `json:"mem_used_bytes"`
	MemPercent      float64 `json:"mem_percent"`
	DiskTotalBytes  uint64  `json:"disk_total_bytes"`
	DiskUsedBytes   uint64  `json:"disk_used_bytes"`
	DiskPercent     float64 `json:"disk_percent"`
	DiskMountpoint  string  `json:"disk_mountpoint,omitempty"`
}

// storageMetrics is daimon's own footprint, broken down by subsystem so the
// user can see where their bytes are going (store vs audit vs skills).
type storageMetrics struct {
	StoreBytes  int64 `json:"store_bytes"`
	AuditBytes  int64 `json:"audit_bytes"`
	SkillsBytes int64 `json:"skills_bytes"`
	TotalBytes  int64 `json:"total_bytes"`
}

// handleGetSystemMetrics serves a one-shot snapshot. Each gopsutil call has a
// short interval to keep latency low; failures are non-fatal and surface as
// zero-valued fields rather than 500s — a partially-populated dashboard
// is better than no dashboard.
func (s *Server) handleGetSystemMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
	defer cancel()

	cfg := s.config()
	resp := systemMetricsResponse{
		Process: collectProcessMetrics(ctx, s.deps.StartedAt),
		Host:    collectHostMetrics(ctx, cfg.Store.Path),
		Storage: collectStorageMetrics(cfg.Store.Path, cfg.Audit.Path, cfg.SkillsDir),
	}

	writeJSON(w, http.StatusOK, resp)
}

func collectProcessMetrics(ctx context.Context, startedAt time.Time) processMetrics {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	pm := processMetrics{
		HeapAllocBytes: ms.HeapAlloc,
		HeapSysBytes:   ms.HeapSys,
		Goroutines:     runtime.NumGoroutine(),
		GCPauseMs:      float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6,
		UptimeSec:      int64(time.Since(startedAt).Seconds()),
	}

	if proc, err := process.NewProcessWithContext(ctx, int32(os.Getpid())); err == nil {
		// CPU% needs an interval to compute deltas; use a short one to keep
		// the endpoint snappy. First call returns 0 — acceptable trade-off.
		if cpuPct, err := proc.CPUPercentWithContext(ctx); err == nil {
			pm.CPUPercent = cpuPct
		}
		if memInfo, err := proc.MemoryInfoWithContext(ctx); err == nil {
			pm.RSSBytes = memInfo.RSS
		}
	}

	return pm
}

func collectHostMetrics(ctx context.Context, storePath string) hostMetrics {
	hm := hostMetrics{CPUCores: runtime.NumCPU()}

	// Host CPU% — short interval keeps latency low; first call after process
	// start returns 0 because gopsutil has no prior sample.
	if pcts, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false); err == nil && len(pcts) > 0 {
		hm.CPUPercent = pcts[0]
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		hm.MemTotalBytes = vm.Total
		hm.MemUsedBytes = vm.Used
		hm.MemPercent = vm.UsedPercent
	}

	// Disk usage is reported for the partition that holds the data dir, which
	// is the relevant partition for daimon. Fall back to "/" if storePath is
	// empty or unresolved.
	probePath := storePath
	if probePath == "" {
		probePath = "/"
	}
	if du, err := disk.UsageWithContext(ctx, probePath); err == nil {
		hm.DiskTotalBytes = du.Total
		hm.DiskUsedBytes = du.Used
		hm.DiskPercent = du.UsedPercent
		hm.DiskMountpoint = du.Path
	}

	return hm
}

func collectStorageMetrics(storePath, auditPath, skillsPath string) storageMetrics {
	sm := storageMetrics{
		StoreBytes:  dirSize(storePath),
		AuditBytes:  dirSize(auditPath),
		SkillsBytes: dirSize(skillsPath),
	}
	sm.TotalBytes = sm.StoreBytes + sm.AuditBytes + sm.SkillsBytes
	return sm
}

// dirSize sums every regular file under root. Returns 0 if root cannot be
// stat'd. Used for the storage breakdown — the absolute number includes WAL
// and SHM sidecars which is what users care about (real disk footprint).
func dirSize(root string) int64 {
	if root == "" {
		return 0
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		total += fi.Size()
		return nil
	})
	return total
}
