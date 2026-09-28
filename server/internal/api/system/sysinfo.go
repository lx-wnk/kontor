package system

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/platform"
)

// SystemInfo mirrors the TypeScript SystemInfo shape.
type SystemInfo struct {
	CPU     cpuInfo   `json:"cpu"`
	Memory  memInfo   `json:"memory"`
	Disk    diskInfo  `json:"disk"`
	LoadAvg []float64 `json:"loadAvg"`
	Uptime  float64   `json:"uptime"`
}

type cpuInfo struct {
	Usage float64 `json:"usage"`
	Cores int     `json:"cores"`
	Model string  `json:"model"`
}

type memInfo struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usagePercent"`
}

type diskInfo struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Available    uint64  `json:"available"`
	UsagePercent float64 `json:"usagePercent"`
	Mount        string  `json:"mount"`
}

// sysInfoCacheTTL bounds how long a collected SystemInfo snapshot is reused.
// Rapid /api/system polls (multiple tabs, short intervals) within this window are
// served from cache instead of re-forking ~7 subprocesses each time.
const sysInfoCacheTTL = 2 * time.Second

var (
	siMu       sync.Mutex
	siCache    SystemInfo
	siCachedAt time.Time
	siHasCache bool
	// Injectable for tests.
	siCollect = collectSystemInfo
	siNow     = time.Now
)

// cachedSystemInfo returns a recent SystemInfo, recomputing only when the cache is
// empty or older than sysInfoCacheTTL.
func cachedSystemInfo() SystemInfo {
	siMu.Lock()
	defer siMu.Unlock()
	now := siNow()
	if siHasCache && now.Sub(siCachedAt) < sysInfoCacheTTL {
		return siCache
	}
	siCache = siCollect()
	siCachedAt = now
	siHasCache = true
	return siCache
}

// System handles GET /api/system/system.
func System(w http.ResponseWriter, r *http.Request) {
	info := cachedSystemInfo()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// Config handles GET /api/system/config.
func Config(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	// Advertise the `agent-dashboard live` entrypoint, using the actual binary path
	// resolved via os.Executable (same as SelfBinaryPath but without EvalSymlinks so
	// the displayed path is human-readable and matches what the user installed).
	exe, _ := os.Executable()
	exeAbs, _ := filepath.Abs(exe)

	// Apply home-directory shortening so the UI displays a tidy path.
	scriptPath := exeAbs + " live"
	if strings.HasPrefix(exeAbs, home) {
		scriptPath = "~" + exeAbs[len(home):] + " live"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"scriptPath":    scriptPath,
		"homedir":       home,
		"mcpServerName": mcp.ServerName,
		"mcpEndpoint":   mcp.EndpointPath,
	})
}

func collectSystemInfo() SystemInfo {
	return SystemInfo{
		CPU:     getCPUInfo(),
		Memory:  getMemInfo(),
		Disk:    getDiskInfo(),
		LoadAvg: getLoadAvg(),
		Uptime:  getUptimeSeconds(),
	}
}

// CPU model and core count are immutable for the process lifetime, so they are
// resolved once via sync.Once. This keeps the per-collect cost to the usage probe
// alone — the macOS path otherwise forks `sysctl machdep.cpu.brand_string` on
// every cache-miss recompute for a value that never changes.
var (
	cpuStaticOnce  sync.Once
	staticCPUModel string
	staticCPUCores int
)

func cpuStatic() (string, int) {
	cpuStaticOnce.Do(func() {
		staticCPUModel = getCPUModel()
		staticCPUCores = runtime.NumCPU()
	})
	return staticCPUModel, staticCPUCores
}

func getCPUInfo() cpuInfo {
	model, cores := cpuStatic()
	return cpuInfo{Usage: getCPUUsage(), Cores: cores, Model: model}
}

func getCPUModel() string {
	if platform.IsLinux {
		b, err := os.ReadFile("/proc/cpuinfo")
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "model name") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						return strings.TrimSpace(parts[1])
					}
				}
			}
		}
		return "Unknown"
	}
	// macOS: sysctl -n machdep.cpu.brand_string
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		return "Unknown"
	}
	return strings.TrimSpace(string(out))
}

// getCPUUsage returns a rough CPU usage percentage.
// On Linux, reads /proc/stat deltas. On macOS, uses top -l2 -n0.
var (
	cpuMu               sync.Mutex
	prevIdle, prevTotal uint64
)

func getCPUUsage() float64 {
	if platform.IsLinux {
		return linuxCPUUsage()
	}
	return macCPUUsage()
}

func linuxCPUUsage() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var vals []uint64
		for _, f := range fields {
			v, _ := strconv.ParseUint(f, 10, 64)
			vals = append(vals, v)
		}
		if len(vals) < 4 {
			return 0
		}
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4] // iowait
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		cpuMu.Lock()
		if prevTotal == 0 {
			prevIdle, prevTotal = idle, total
			cpuMu.Unlock()
			return 0
		}
		dIdle := idle - prevIdle
		dTotal := total - prevTotal
		prevIdle, prevTotal = idle, total
		cpuMu.Unlock()
		if dTotal == 0 {
			return 0
		}
		return math.Round((1-float64(dIdle)/float64(dTotal))*10000) / 100
	}
	return 0
}

func macCPUUsage() float64 {
	// Sum %cpu across all processes, normalize by core count (~28ms vs top's 2.6s).
	out, err := exec.Command("ps", "-A", "-o", "%cpu").Output()
	if err != nil {
		return 0
	}
	var sum float64
	for _, line := range strings.Split(string(out), "\n") {
		v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err == nil {
			sum += v
		}
	}
	cores := runtime.NumCPU()
	if cores <= 0 {
		cores = 1
	}
	return math.Round(sum/float64(cores)*100) / 100
}

func getMemInfo() memInfo {
	if platform.IsLinux {
		return linuxMemInfo()
	}
	return macMemInfo()
}

func linuxMemInfo() memInfo {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memInfo{}
	}
	kv := make(map[string]uint64)
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		val, _ := strconv.ParseUint(parts[1], 10, 64)
		kv[key] = val * 1024 // kB → bytes
	}
	total := kv["MemTotal"]
	avail := kv["MemAvailable"]
	used := total - avail
	var pct float64
	if total > 0 {
		pct = math.Round(float64(used)/float64(total)*10000) / 100
	}
	return memInfo{Total: total, Used: used, Available: avail, UsagePercent: pct}
}

func macMemInfo() memInfo {
	// sysctl -n hw.memsize → total bytes
	totalOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return memInfo{}
	}
	total, _ := strconv.ParseUint(strings.TrimSpace(string(totalOut)), 10, 64)

	// vm_stat → free + inactive pages
	vmOut, err := exec.Command("vm_stat").Output()
	if err != nil {
		return memInfo{Total: total}
	}
	kv := make(map[string]uint64)
	for _, line := range strings.Split(string(vmOut), "\n") {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		val, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(parts[1], ".")), 10, 64)
		kv[strings.TrimSpace(parts[0])] = val
	}
	pageSize := uint64(4096)
	free := (kv["Pages free"] + kv["Pages inactive"]) * pageSize
	used := total - free
	var pct float64
	if total > 0 {
		pct = math.Round(float64(used)/float64(total)*10000) / 100
	}
	return memInfo{Total: total, Used: used, Available: free, UsagePercent: pct}
}

func getDiskInfo() diskInfo {
	home, _ := os.UserHomeDir()
	out, err := exec.Command("df", "-k", home).Output()
	if err != nil {
		return diskInfo{Mount: home}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return diskInfo{Mount: home}
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		return diskInfo{Mount: home}
	}
	total, _ := strconv.ParseUint(fields[1], 10, 64)
	used, _ := strconv.ParseUint(fields[2], 10, 64)
	avail, _ := strconv.ParseUint(fields[3], 10, 64)
	mount := fields[len(fields)-1]
	total *= 1024
	used *= 1024
	avail *= 1024
	var pct float64
	if total > 0 {
		pct = math.Round(float64(used)/float64(total)*10000) / 100
	}
	return diskInfo{Total: total, Used: used, Available: avail, UsagePercent: pct, Mount: mount}
}

func getLoadAvg() []float64 {
	// /proc/loadavg on Linux; sysctl on macOS
	if platform.IsLinux {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return []float64{0, 0, 0}
		}
		fields := strings.Fields(string(b))
		var avgs []float64
		for _, f := range fields[:min3(3, len(fields))] {
			v, _ := strconv.ParseFloat(f, 64)
			avgs = append(avgs, v)
		}
		return avgs
	}
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return []float64{0, 0, 0}
	}
	// Format: "{ 0.12 0.34 0.56 }"
	s := strings.Trim(strings.TrimSpace(string(out)), "{}")
	fields := strings.Fields(s)
	var avgs []float64
	for _, f := range fields[:min3(3, len(fields))] {
		v, _ := strconv.ParseFloat(f, 64)
		avgs = append(avgs, v)
	}
	return avgs
}

func getUptimeSeconds() float64 {
	if platform.IsLinux {
		b, err := os.ReadFile("/proc/uptime")
		if err != nil {
			return time.Since(startTime).Seconds()
		}
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			v, _ := strconv.ParseFloat(fields[0], 64)
			return v
		}
	}
	out, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return time.Since(startTime).Seconds()
	}
	// Format: "{ sec = 1234567890, usec = 0 } Mon Jan ..."
	s := string(out)
	idx := strings.Index(s, "sec = ")
	if idx < 0 {
		return time.Since(startTime).Seconds()
	}
	rest := s[idx+len("sec = "):]
	end := strings.IndexAny(rest, ", }")
	if end < 0 {
		return time.Since(startTime).Seconds()
	}
	bootSec, _ := strconv.ParseInt(rest[:end], 10, 64)
	return time.Since(time.Unix(bootSec, 0)).Seconds()
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
