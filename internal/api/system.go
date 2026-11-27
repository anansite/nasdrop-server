package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SystemInfoResponse struct {
	Status              string        `json:"status"`
	Service             string        `json:"service"`
	Version             string        `json:"version"`
	Hostname            string        `json:"hostname"`
	OS                  string        `json:"os"`
	Arch                string        `json:"arch"`
	CPUCores            int           `json:"cpu_cores"`
	CPUPercent          float64       `json:"cpu_percent"`
	LoadAvg             []float64     `json:"load_avg"`
	UptimeSeconds       int64         `json:"uptime_seconds"`
	ServerUptimeSeconds int64         `json:"server_uptime_seconds"`
	Memory              MemoryMetrics `json:"memory"`
	Disk                DiskMetrics   `json:"disk"`
}

type MemoryMetrics struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Percent   float64 `json:"percent"`
}

type DiskMetrics struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

var (
	cpuMu        sync.Mutex
	lastCPUTime  time.Time
	lastCPUIdle  uint64
	lastCPUTotal uint64
)

func (rt *Router) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname, _ := os.Hostname()
	cores := runtime.NumCPU()
	loadAvg := readLoadAvg()
	uptime := readUptime()
	serverUptime := int64(time.Since(rt.startTime).Seconds())
	cpuPct := readCPUPercent(cores, loadAvg)
	mem := readMemory()

	totalDisk, freeDisk, usedDisk, diskPct, _ := getDiskUsage(rt.dataDir)

	resp := SystemInfoResponse{
		Status:              "ok",
		Service:             "nasdrop-server",
		Version:             "1.0.0",
		Hostname:            hostname,
		OS:                  runtime.GOOS,
		Arch:                runtime.GOARCH,
		CPUCores:            cores,
		CPUPercent:          cpuPct,
		LoadAvg:             loadAvg,
		UptimeSeconds:       uptime,
		ServerUptimeSeconds: serverUptime,
		Memory:              mem,
		Disk: DiskMetrics{
			Path:    rt.dataDir,
			Total:   totalDisk,
			Used:    usedDisk,
			Free:    freeDisk,
			Percent: diskPct,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func readLoadAvg() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return []float64{}
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return []float64{}
	}
	res := make([]float64, 0, 3)
	for i := 0; i < 3; i++ {
		val, err := strconv.ParseFloat(fields[i], 64)
		if err == nil {
			res = append(res, val)
		}
	}
	return res
}

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(sec)
}

func readMemory() MemoryMetrics {
	file, err := os.Open("/proc/meminfo")
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		var totalKb, availKb, freeKb, buffersKb, cachedKb uint64
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			valStr := strings.TrimSpace(strings.TrimSuffix(parts[1], "kB"))
			valFields := strings.Fields(valStr)
			if len(valFields) == 0 {
				continue
			}
			v, err := strconv.ParseUint(valFields[0], 10, 64)
			if err != nil {
				continue
			}
			switch key {
			case "MemTotal":
				totalKb = v
			case "MemAvailable":
				availKb = v
			case "MemFree":
				freeKb = v
			case "Buffers":
				buffersKb = v
			case "Cached":
				cachedKb = v
			}
		}
		if totalKb > 0 {
			if availKb == 0 {
				availKb = freeKb + buffersKb + cachedKb
			}
			total := totalKb * 1024
			avail := availKb * 1024
			used := uint64(0)
			if total > avail {
				used = total - avail
			}
			pct := float64(used) / float64(total) * 100.0
			return MemoryMetrics{
				Total:     total,
				Used:      used,
				Available: avail,
				Percent:   pct,
			}
		}
	}

	// Fallback to Go runtime memory
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	total := ms.Sys
	used := ms.Alloc
	avail := uint64(0)
	if total > used {
		avail = total - used
	}
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100.0
	}
	return MemoryMetrics{
		Total:     total,
		Used:      used,
		Available: avail,
		Percent:   pct,
	}
}

func readCPUPercent(cores int, loadAvg []float64) float64 {
	cpuMu.Lock()
	defer cpuMu.Unlock()

	data, err := os.ReadFile("/proc/stat")
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		if scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[0] == "cpu" {
				var total uint64
				var idle uint64
				for i := 1; i < len(fields); i++ {
					val, _ := strconv.ParseUint(fields[i], 10, 64)
					total += val
					if i == 4 || i == 5 { // idle + iowait
						idle += val
					}
				}

				now := time.Now()
				if !lastCPUTime.IsZero() && total > lastCPUTotal && idle >= lastCPUIdle {
					diffTotal := total - lastCPUTotal
					diffIdle := idle - lastCPUIdle
					if diffTotal > 0 && diffTotal >= diffIdle {
						pct := float64(diffTotal-diffIdle) / float64(diffTotal) * 100.0
						lastCPUTime = now
						lastCPUTotal = total
						lastCPUIdle = idle
						if pct < 0 {
							pct = 0
						}
						if pct > 100 {
							pct = 100
						}
						return pct
					}
				}
				lastCPUTime = now
				lastCPUTotal = total
				lastCPUIdle = idle
			}
		}
	}

	// Fallback using 1m load average
	if len(loadAvg) > 0 && cores > 0 {
		pct := (loadAvg[0] / float64(cores)) * 100.0
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return pct
	}
	return 0.0
}
