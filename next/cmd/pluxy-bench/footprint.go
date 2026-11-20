package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostInfo describes the machine and build environment printed in the report header.
type HostInfo struct {
	GitCommit  string `json:"git_commit"`
	GoVersion  string `json:"go_version"`
	OSArch     string `json:"os_arch"`
	CPUModel   string `json:"cpu_model"`
	CPUCount   int    `json:"cpu_count"`
	Memory     string `json:"memory"`
	LoadBefore string `json:"load_before"`
	LoadAfter  string `json:"load_after"`
}

// ProcessRSS holds RSS in megabytes for one process (or "total").
type ProcessRSS struct {
	Name  string  `json:"name"`
	PID   int     `json:"pid,omitempty"`
	RSSMB float64 `json:"rss_mb"`
}

// ProcessCPU holds CPU usage over one scenario for one process (or "total").
type ProcessCPU struct {
	Name      string  `json:"name"`
	PID       int     `json:"pid,omitempty"`
	Cores     float64 `json:"cores"`
	MSPerReq  float64 `json:"ms_per_req"`
	CPUTotalS float64 `json:"cpu_total_s"`
}

// SideFootprint holds the measured RSS and CPU footprint of the proxy process tree during one
// scenario run.
type SideFootprint struct {
	Measured bool         `json:"measured"`
	PeakRSS  []ProcessRSS `json:"peak_rss,omitempty"`
	CPU      []ProcessCPU `json:"cpu,omitempty"`
}

// IdleSnapshot holds idle RSS measurements across the proxy process tree.
type IdleSnapshot struct {
	Measured  bool         `json:"measured"`
	Processes []ProcessRSS `json:"processes,omitempty"`
}

var clkTck = readClkTck()

func readClkTck() float64 {
	if runtime.GOOS != "linux" {
		return 100
	}
	out, err := exec.Command("getconf", "CLK_TCK").Output()
	if err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && v > 0 {
			return v
		}
	}
	return 100
}

// ReadLoadAvg reads the Linux load average string, or returns "not measured" off Linux.
func ReadLoadAvg() string {
	if runtime.GOOS != "linux" {
		return "not measured"
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "not measured"
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 3 {
		return strings.Join(fields[:3], " ")
	}
	return strings.TrimSpace(string(b))
}

// CollectHostInfo gathers repository, Go, CPU, memory, and initial load metadata.
func CollectHostInfo(repoRoot string) HostInfo {
	info := HostInfo{
		GitCommit:  readGitCommit(repoRoot),
		GoVersion:  runtime.Version(),
		OSArch:     runtime.GOOS + "/" + runtime.GOARCH,
		CPUModel:   "not measured",
		CPUCount:   runtime.NumCPU(),
		Memory:     "not measured",
		LoadBefore: ReadLoadAvg(),
	}
	if runtime.GOOS == "linux" {
		info.CPUModel = readCPUModel()
		info.Memory = readMemTotal()
	}
	return info
}

func readGitCommit(repoRoot string) string {
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	sha := strings.TrimSpace(string(out))
	st, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if err == nil && len(bytes.TrimSpace(st)) > 0 {
		sha += " (dirty)"
	}
	return sha
}

func readCPUModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") {
			if _, val, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(val)
			}
		}
	}
	return "unknown"
}

func readMemTotal() string {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseFloat(fields[1], 64); err == nil {
					return fmt.Sprintf("%.2f GiB", kb/(1024*1024))
				}
			}
		}
	}
	return "unknown"
}

type procInfo struct {
	pid  int
	name string
}

// discoverProcessTree returns corePID and all direct/indirect child PIDs on Linux.
func discoverProcessTree(rootPID int) []procInfo {
	if runtime.GOOS != "linux" || rootPID <= 0 {
		return nil
	}
	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	var pids []int
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		pids = append(pids, p)
		children := findChildren(p)
		for _, c := range children {
			if !seen[c] {
				seen[c] = true
				queue = append(queue, c)
			}
		}
	}
	out := make([]procInfo, 0, len(pids))
	for _, p := range pids {
		name := readProcName(p)
		if name == "" {
			if p == rootPID {
				name = "pluxy"
			} else {
				name = fmt.Sprintf("child-%d", p)
			}
		}
		out = append(out, procInfo{pid: p, name: name})
	}
	return out
}

func findChildren(ppid int) []int {
	// Prefer /proc/<pid>/task/*/children when available.
	matches, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", ppid))
	var out []int
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
				out = append(out, pid)
			}
		}
	}
	if len(out) > 0 {
		slices.Sort(out)
		return slices.Compact(out)
	}
	// Fallback: scan /proc/*/stat for matching ppid.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if p, _, ok := readProcStat(pid); ok && p == ppid {
			out = append(out, pid)
		}
	}
	slices.Sort(out)
	return out
}

func readProcName(pid int) string {
	if cb, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && len(cb) > 0 {
		if idx := bytes.IndexByte(cb, 0); idx > 0 {
			cb = cb[:idx]
		}
		if base := filepath.Base(string(cb)); base != "" && base != "." {
			return base
		}
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "Name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		}
	}
	return ""
}

func readProcRSSKB(pid int) (int64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					return kb, true
				}
			}
		}
	}
	return 0, false
}

// readProcStat returns (ppid, utime+stime ticks, ok) from /proc/<pid>/stat.
func readProcStat(pid int) (int, uint64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	idx := bytes.LastIndexByte(b, ')')
	if idx < 0 || idx+2 >= len(b) {
		return 0, 0, false
	}
	fields := strings.Fields(string(b[idx+2:]))
	// fields[0]=state, fields[1]=ppid, ..., fields[11]=utime, fields[12]=stime.
	if len(fields) <= 12 {
		return 0, 0, false
	}
	ppid, err1 := strconv.Atoi(fields[1])
	utime, err2 := strconv.ParseUint(fields[11], 10, 64)
	stime, err3 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	return ppid, utime + stime, true
}

// MeasureIdleRSS reads current VmRSS for corePID and its children.
func MeasureIdleRSS(corePID int) IdleSnapshot {
	if runtime.GOOS != "linux" || corePID <= 0 {
		return IdleSnapshot{Measured: false}
	}
	procs := discoverProcessTree(corePID)
	var list []ProcessRSS
	var totalKB int64
	for _, p := range procs {
		kb, ok := readProcRSSKB(p.pid)
		if !ok {
			continue
		}
		totalKB += kb
		list = append(list, ProcessRSS{
			Name:  p.name,
			PID:   p.pid,
			RSSMB: float64(kb) / 1024.0,
		})
	}
	if len(list) == 0 {
		return IdleSnapshot{Measured: false}
	}
	list = append(list, ProcessRSS{
		Name:  "total",
		RSSMB: float64(totalKB) / 1024.0,
	})
	return IdleSnapshot{Measured: true, Processes: list}
}

// TotalRSSMB returns the "total" RSS entry in MB from an IdleSnapshot or PeakRSS slice.
func TotalRSSMB(list []ProcessRSS) float64 {
	for _, p := range list {
		if p.Name == "total" {
			return p.RSSMB
		}
	}
	var sum float64
	for _, p := range list {
		sum += p.RSSMB
	}
	return sum
}

// FootprintTracker samples RSS every 250 ms and tracks CPU ticks across one scenario run.
type FootprintTracker struct {
	corePID  int
	measured bool
	startT   time.Time
	procs    []procInfo
	cpuStart map[int]uint64

	mu        sync.Mutex
	peakKB    map[int]int64
	peakTotal int64
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// StartFootprintTracker begins sampling RSS and snapshotting initial CPU ticks for corePID.
func StartFootprintTracker(corePID int) *FootprintTracker {
	ft := &FootprintTracker{
		corePID:  corePID,
		measured: runtime.GOOS == "linux" && corePID > 0,
		startT:   time.Now(),
		cpuStart: make(map[int]uint64),
		peakKB:   make(map[int]int64),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	if !ft.measured {
		close(ft.doneCh)
		return ft
	}
	ft.procs = discoverProcessTree(corePID)
	for _, p := range ft.procs {
		if _, ticks, ok := readProcStat(p.pid); ok {
			ft.cpuStart[p.pid] = ticks
		}
	}
	ft.sampleOnce()
	go func() {
		defer close(ft.doneCh)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ft.sampleOnce()
			case <-ft.stopCh:
				ft.sampleOnce()
				return
			}
		}
	}()
	return ft
}

func (ft *FootprintTracker) sampleOnce() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var total int64
	for _, p := range ft.procs {
		kb, ok := readProcRSSKB(p.pid)
		if !ok {
			continue
		}
		total += kb
		if kb > ft.peakKB[p.pid] {
			ft.peakKB[p.pid] = kb
		}
	}
	if total > ft.peakTotal {
		ft.peakTotal = total
	}
}

// Stop ends sampling and returns the SideFootprint for the scenario.
func (ft *FootprintTracker) Stop(completedReqs int) *SideFootprint {
	if !ft.measured {
		return &SideFootprint{Measured: false}
	}
	close(ft.stopCh)
	<-ft.doneCh
	elapsed := time.Since(ft.startT).Seconds()

	ft.mu.Lock()
	peakRSS := make([]ProcessRSS, 0, len(ft.procs)+1)
	for _, p := range ft.procs {
		peakRSS = append(peakRSS, ProcessRSS{
			Name:  p.name,
			PID:   p.pid,
			RSSMB: float64(ft.peakKB[p.pid]) / 1024.0,
		})
	}
	peakRSS = append(peakRSS, ProcessRSS{
		Name:  "total",
		RSSMB: float64(ft.peakTotal) / 1024.0,
	})
	ft.mu.Unlock()

	cpus := make([]ProcessCPU, 0, len(ft.procs)+1)
	var totalCPUS float64
	for _, p := range ft.procs {
		_, endTicks, ok := readProcStat(p.pid)
		startTicks := ft.cpuStart[p.pid]
		var cpuS float64
		if ok && endTicks >= startTicks {
			cpuS = float64(endTicks-startTicks) / clkTck
		}
		totalCPUS += cpuS
		var cores, msPerReq float64
		if elapsed > 0 {
			cores = cpuS / elapsed
		}
		if completedReqs > 0 {
			msPerReq = (cpuS * 1000.0) / float64(completedReqs)
		}
		cpus = append(cpus, ProcessCPU{
			Name:      p.name,
			PID:       p.pid,
			Cores:     cores,
			MSPerReq:  msPerReq,
			CPUTotalS: cpuS,
		})
	}
	var totalCores, totalMSPerReq float64
	if elapsed > 0 {
		totalCores = totalCPUS / elapsed
	}
	if completedReqs > 0 {
		totalMSPerReq = (totalCPUS * 1000.0) / float64(completedReqs)
	}
	cpus = append(cpus, ProcessCPU{
		Name:      "total",
		Cores:     totalCores,
		MSPerReq:  totalMSPerReq,
		CPUTotalS: totalCPUS,
	})

	return &SideFootprint{
		Measured: true,
		PeakRSS:  peakRSS,
		CPU:      cpus,
	}
}
