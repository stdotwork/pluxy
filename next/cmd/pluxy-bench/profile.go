package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const overlayGoTemplate = `package main

import (
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
)

const benchProfileControlFile = %q

func init() {
	prefix := os.Getenv("PLUXY_BENCH_PROFILE")
	secStr := os.Getenv("PLUXY_BENCH_PROFILE_SECONDS")
	stopFile := os.Getenv("PLUXY_BENCH_PROFILE_STOP")
	if prefix == "" && benchProfileControlFile != "" {
		if raw, err := os.ReadFile(benchProfileControlFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) >= 1 {
				prefix = strings.TrimSpace(lines[0])
			}
			if len(lines) >= 2 && secStr == "" {
				secStr = strings.TrimSpace(lines[1])
			}
			if len(lines) >= 3 && stopFile == "" {
				stopFile = strings.TrimSpace(lines[2])
			}
		}
	}
	if prefix == "" {
		return
	}
	secs := 10
	if v, err := strconv.Atoi(secStr); err == nil && v > 0 {
		secs = v
	}
	cpuTmp := prefix + ".cpu.pprof.tmp"
	cpuOut := prefix + ".cpu.pprof"
	f, err := os.Create(cpuTmp)
	if err != nil {
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(cpuTmp)
		return
	}
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			pprof.StopCPUProfile()
			_ = f.Close()
			_ = os.Rename(cpuTmp, cpuOut)

			heapTmp := prefix + ".heap.pprof.tmp"
			heapOut := prefix + ".heap.pprof"
			hf, err := os.Create(heapTmp)
			if err != nil {
				return
			}
			runtime.GC()
			_ = pprof.WriteHeapProfile(hf)
			_ = hf.Close()
			_ = os.Rename(heapTmp, heapOut)
		})
	}
	time.AfterFunc(time.Duration(secs)*time.Second, finish)
	if stopFile != "" {
		go func() {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				if _, err := os.Stat(stopFile); err == nil {
					finish()
					return
				}
			}
		}()
	}
}
`

// buildProfileBinaries builds core and openai-compatible with a -overlay adding the pprof init
// hook to each main package without modifying any source files in the repository.
func buildProfileBinaries(nextRoot, binDir, extControlFile string) (coreBin string, err error) {
	overlayDir, err := os.MkdirTemp("", "pluxy-bench-overlay-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(overlayDir)

	coreSrc := filepath.Join(overlayDir, "core_pprof.go")
	extSrc := filepath.Join(overlayDir, "ext_pprof.go")
	if err := os.WriteFile(coreSrc, []byte(fmt.Sprintf(overlayGoTemplate, "")), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(extSrc, []byte(fmt.Sprintf(overlayGoTemplate, extControlFile)), 0o600); err != nil {
		return "", err
	}

	overlay := struct {
		Replace map[string]string `json:"Replace"`
	}{
		Replace: map[string]string{
			filepath.Join(nextRoot, "cmd", "pluxy", "bench_pprof_overlay.go"):                    coreSrc,
			filepath.Join(nextRoot, "extensions", "openai-compatible", "bench_pprof_overlay.go"): extSrc,
		},
	}
	overlayBytes, err := json.Marshal(overlay)
	if err != nil {
		return "", err
	}
	overlayFile := filepath.Join(overlayDir, "overlay.json")
	if err := os.WriteFile(overlayFile, overlayBytes, 0o600); err != nil {
		return "", err
	}

	coreBin = filepath.Join(binDir, binaryName("pluxy"))
	extBin := filepath.Join(binDir, binaryName("openai-compatible"))

	buildOne := func(pkg, out string) error {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-overlay", overlayFile, "-o", out, pkg)
		cmd.Dir = nextRoot
		if outBytes, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s with profile overlay: %w\n%s", pkg, err, outBytes)
		}
		return nil
	}

	if err := buildOne("./cmd/pluxy", coreBin); err != nil {
		return "", err
	}
	if err := buildOne("./extensions/openai-compatible", extBin); err != nil {
		return "", err
	}
	if err := writeExtensionManifest(binDir); err != nil {
		return "", err
	}
	return coreBin, nil
}

// selectProfileScenarios returns the scenarios selected by filter. With no filter it keeps the
// profile mode's historical default: agents and throughput.
func selectProfileScenarios(specs []ScenarioSpec, filter string) ([]ScenarioSpec, error) {
	if filter == "" {
		filter = "agents,throughput"
	}
	return filterScenarios(specs, filter)
}

// RunProfileMode builds overlay-instrumented binaries and runs specs, writing
// <dir>/<scenario>-<process>.cpu.pprof and .heap.pprof.
func RunProfileMode(ctx context.Context, nextRoot, dir string, specs []ScenarioSpec, mock *MockServer) ([]string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	binDir, err := os.MkdirTemp("", "pluxy-bench-profbin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(binDir)

	extControlFile := filepath.Join(binDir, "ext-profile.cfg")
	coreBin, err := buildProfileBinaries(nextRoot, binDir, extControlFile)
	if err != nil {
		return nil, err
	}

	var written []string
	for _, spec := range specs {
		corePrefix := filepath.Join(absDir, spec.Name+"-core")
		extPrefix := filepath.Join(absDir, spec.Name+"-openai-compatible")
		coreStop := filepath.Join(absDir, spec.Name+"-core.stop")
		extStop := filepath.Join(absDir, spec.Name+"-openai-compatible.stop")
		defer os.Remove(coreStop)
		defer os.Remove(extStop)

		for _, p := range []string{
			corePrefix + ".cpu.pprof", corePrefix + ".heap.pprof",
			extPrefix + ".cpu.pprof", extPrefix + ".heap.pprof",
			coreStop, extStop,
		} {
			_ = os.Remove(p)
		}

		// The stop files end the profiles immediately after the scenario, so request-count
		// scenarios are profiled for their whole workload rather than a guessed duration. The
		// timer is only a fallback for a process whose stop signal cannot be delivered.
		if err := os.WriteFile(extControlFile, []byte(extPrefix+"\n3600\n"+extStop+"\n"), 0o600); err != nil {
			return nil, err
		}

		cfgDir, err := os.MkdirTemp("", "pluxy-bench-profcfg-")
		if err != nil {
			return nil, err
		}
		cfgPath := filepath.Join(cfgDir, "config.json")
		if err := writeCoreConfig(cfgPath, filepath.Join(binDir, "pluxy-extension.json"), mock.BaseURL()); err != nil {
			os.RemoveAll(cfgDir)
			return nil, err
		}

		env := []string{
			"PLUXY_BENCH_PROFILE=" + corePrefix,
			"PLUXY_BENCH_PROFILE_SECONDS=3600",
			"PLUXY_BENCH_PROFILE_STOP=" + coreStop,
		}
		proc, err := startCoreProcess(coreBin, cfgPath, env)
		if err != nil {
			os.RemoveAll(cfgDir)
			return nil, err
		}

		res, runErr := RunSide(ctx, spec, true, proc.BaseURL, benchClientKey, "bench-messages", mock)
		for _, stop := range []string{coreStop, extStop} {
			if err := os.WriteFile(stop, nil, 0o600); err != nil && runErr == nil {
				runErr = fmt.Errorf("stopping %s profile: %w", spec.Name, err)
			}
		}

		expectedFiles := []string{
			corePrefix + ".cpu.pprof",
			corePrefix + ".heap.pprof",
			extPrefix + ".cpu.pprof",
			extPrefix + ".heap.pprof",
		}
		waitDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(waitDeadline) {
			allReady := true
			for _, p := range expectedFiles {
				if st, err := os.Stat(p); err != nil || st.Size() == 0 {
					allReady = false
					break
				}
			}
			if allReady {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		_ = proc.Stop()
		os.RemoveAll(cfgDir)

		if runErr != nil {
			return nil, fmt.Errorf("profile %s: %w", spec.Name, runErr)
		}
		if res.Errors > 0 || res.FidelityFails > 0 {
			return nil, fmt.Errorf("profile %s had %d errors and %d fidelity failures", spec.Name, res.Errors, res.FidelityFails)
		}
		for _, p := range expectedFiles {
			if st, err := os.Stat(p); err != nil || st.Size() == 0 {
				return nil, fmt.Errorf("profile %s missing output %s", spec.Name, p)
			}
			written = append(written, p)
		}
	}
	return written, nil
}
