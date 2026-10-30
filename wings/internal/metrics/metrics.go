// Package metrics samples running game containers and keeps a short rolling
// history of resource usage per server (CPU, memory, network, disk).
package metrics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sample is one point of resource usage for a server.
type Sample struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsedMB  float64 `json:"mem_used_mb"`
	MemLimitMB float64 `json:"mem_limit_mb"`
	NetRXBps   float64 `json:"net_rx_bps"`
	NetTXBps   float64 `json:"net_tx_bps"`
	DiskBytes      uint64 `json:"disk_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	At             int64  `json:"at"` // unix seconds
}

// Registry keeps a rolling buffer of samples per server.
type Registry struct {
	mu       sync.Mutex
	bufs     map[string][]Sample
	max      int   // samples kept per server
	interval time.Duration
	// last disk measurement guard (du is comparatively heavy)
	cancel context.CancelFunc
	done   chan struct{}
}

const defaultMax = 120 // 120 * 5s = 10 minutes of history

// NewRegistry creates a registry and starts the sampler in the background.
func NewRegistry(ctx context.Context, hostDataDir string) *Registry {
	r := &Registry{
		bufs:     map[string][]Sample{},
		max:      defaultMax,
		interval: 5 * time.Second,
		done:     make(chan struct{}),
	}
	cc, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.sampleLoop(cc, hostDataDir)
	return r
}

// Stop halts the background sampler.
func (r *Registry) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

// History returns a copy of the samples for a server, oldest first.
func (r *Registry) History(serverID string) []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Sample, len(r.bufs[serverID]))
	for i, s := range r.bufs[serverID] {
		s.At = s.At / int64(time.Second) // ensure seconds
		out[i] = s
	}
	return out
}

func (r *Registry) sampleLoop(ctx context.Context, hostDataDir string) {
	defer close(r.done)
	diskTick := 0
	t := time.NewTicker(r.interval)
	defer t.Stop()
	prevRx, prevTx := map[string]uint64{}, map[string]uint64{}
	var prevTime map[string]int64 = map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			// which servers are running?
			running := r.runningServers(ctx)
			for _, id := range running {
				s, rx, tx, ok := r.sampleDocker(ctx, id, prevRx[id], prevTx[id], prevTime[id], now.Unix())
				if !ok {
					continue
				}
				prevRx[id], prevTx[id], prevTime[id] = rx, tx, now.Unix()
				// disk: measure every 6th tick (~30s) — du is comparatively heavy
				diskTick++
				if diskTick%6 == 1 {
					filesPath := filepath.Join(hostDataDir, "servers", id, "files")
					if db, err := dirBytes(ctx, filesPath); err == nil {
						s.DiskBytes = db
					}
					if tot, err := fsTotal(ctx, filesPath); err == nil {
						s.DiskTotalBytes = tot
					}
				} else {
					// carry forward the last known disk size
					if h := r.History(id); len(h) > 0 {
						s.DiskBytes = h[len(h)-1].DiskBytes
						s.DiskTotalBytes = h[len(h)-1].DiskTotalBytes
					}
				}
				r.push(id, s)
			}
		}
	}
}

// runningServers returns server IDs whose container is currently active.
func (r *Registry) runningServers(ctx context.Context) []string {
	// List active kg-* containers by name.
	out, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "name=kg-", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "kg-") {
			ids = append(ids, strings.TrimPrefix(name, "kg-"))
		}
	}
	return ids
}

// sampleDocker reads docker stats for one container and computes rates.
func (r *Registry) sampleDocker(ctx context.Context, serverID string, prevRx, prevTx uint64, prevTime, nowTs int64) (Sample, uint64, uint64, bool) {
	name := "kg-" + serverID
	// "|"-delimited to avoid Go field-name guessing on {{json .}} (which emits
	// CPUPerc/MemUsage/NetIO, not snake_case). Fields: cpu|mem|net.
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}|{{.MemUsage}}|{{.NetIO}}", name)
	out, err := cmd.Output()
	if err != nil {
		return Sample{}, 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) < 3 {
		return Sample{}, 0, 0, false
	}
	cpuStr, memStr, netStr := parts[0], parts[1], parts[2]
	s := Sample{MemLimitMB: memLimit(memStr), At: nowTs}
	s.CPUPercent = parseFirstNum(cpuStr)
	s.MemUsedMB = memUsed(memStr)
	rx, tx := parseNetIO(netStr)
	dt := float64(nowTs - prevTime)
	if prevTime > 0 && dt > 0 {
		s.NetRXBps = float64(rx-prevRx) / dt
		s.NetTXBps = float64(tx-prevTx) / dt
	}
	return s, rx, tx, true
}

func (r *Registry) push(id string, s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := r.bufs[id]
	buf = append(buf, s)
	if len(buf) > r.max {
		buf = buf[len(buf)-r.max:]
	}
	r.bufs[id] = buf
}

// fsTotal returns the capacity in bytes of the filesystem containing path.
func fsTotal(ctx context.Context, path string) (uint64, error) {
	out, err := exec.CommandContext(ctx, "df", "-B1", "--output=size", path).Output()
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("df: unexpected output")
	}
	return strconv.ParseUint(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
}

// dirBytes returns the total size in bytes under path (du-like).
func dirBytes(ctx context.Context, path string) (uint64, error) {
	// Use `du` for speed; falls back to a walk if du is unavailable.
	out, err := exec.CommandContext(ctx, "du", "-sb", path).Output()
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			if n, perr := strconv.ParseUint(fields[0], 10, 64); perr == nil {
				return n, nil
			}
		}
	}
	// fallback walk
	var total uint64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if !info.IsDir() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, nil
}

// --- byte-string parsers (docker stats human units) ---

func parseFirstNum(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// memUsed parses "1.234GiB / 8GiB" -> used in MB.
func memUsed(s string) float64 {
	part := strings.SplitN(s, "/", 2)
	if len(part) == 0 {
		return 0
	}
	return toMB(strings.TrimSpace(part[0]))
}

// memLimit parses "1.234GiB / 8GiB" -> limit in MB.
func memLimit(s string) float64 {
	part := strings.SplitN(s, "/", 2)
	if len(part) < 2 {
		return 0
	}
	return toMB(strings.TrimSpace(part[1]))
}

func toMB(s string) float64 {
	s = strings.TrimSpace(s)
	unit := ""
	num := s
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			num = s[:i+1]
			unit = strings.TrimSpace(s[i+1:])
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(unit) {
	case "tb":
		return v * 1024 * 1024
	case "gb", "gib":
		return v * 1024
	case "mb", "mib":
		return v
	case "kb", "kib":
		return v / 1024
	case "b":
		return v / 1024 / 1024
	default:
		return v / 1024
	}
}

func parseNetIO(s string) (rx, tx uint64) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return 0, 0
	}
	rx = toBytes(parts[0])
	tx = toBytes(parts[1])
	return
}

func toBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	unit := ""
	num := s
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			num = s[:i+1]
			unit = strings.TrimSpace(s[i+1:])
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(unit) {
	case "tb":
		return uint64(v * 1024 * 1024 * 1024 * 1024)
	case "gb":
		return uint64(v * 1024 * 1024 * 1024)
	case "mb":
		return uint64(v * 1024 * 1024)
	case "kb":
		return uint64(v * 1024)
	default:
		return uint64(v)
	}
}