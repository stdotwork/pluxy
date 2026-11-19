package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const overlayGoTemplate = `package main

import (
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

const benchProfileControlFile = %q

func init() {
	prefix := os.Getenv("PLUXY_BENCH_PROFILE")
	secStr := os.Getenv("PLUXY_BENCH_PROFILE_SECONDS")
	if prefix == "" && benchProfileControlFile != "" {
		if raw, err := os.ReadFile(benchProfileControlFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) >= 1 {
				prefix = strings.TrimSpace(lines[0])
			}
			if len(lines) >= 2 && secStr == "" {
				secStr = strings.TrimSpace(lines[1])
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
	time.AfterFunc(time.Duration(secs)*time.Second, func() {
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

// RunProfileMode builds overlay-instrumented binaries and runs the agents and throughput
// scenarios, writing <dir>/<scenario>-<process>.cpu.pprof and .heap.pprof.
func RunProfileMode(ctx context.Context, nextRoot, dir string, bodyKB int, mock *MockServer) ([]string, error) {
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

	specs := []ScenarioSpec{
		{
			Name:        "agents",
			Stream:      true,
			Concurrency: 80,
			Duration:    20 * time.Second,
			BodyKB:      bodyKB,
			TTFT:        300 * time.Millisecond,
			Chunks:      100,
			Interval:    30 * time.Millisecond,
		},
		{
			Name:        "throughput",
			Stream:      false,
			Concurrency: 16,
			Duration:    10 * time.Second,
			BodyKB:      bodyKB,
			Latency:     0,
		},
	}

	var written []string
	for _, spec := range specs {
		secs := max(int(spec.Duration.Seconds()), 1)
		corePrefix := filepath.Join(absDir, spec.Name+"-core")
		extPrefix := filepath.Join(absDir, spec.Name+"-openai-compatible")

		for _, p := range []string{
			corePrefix + ".cpu.pprof", corePrefix + ".heap.pprof",
			extPrefix + ".cpu.pprof", extPrefix + ".heap.pprof",
		} {
			_ = os.Remove(p)
		}

		if err := os.WriteFile(extControlFile, []byte(extPrefix+"\n"+strconv.Itoa(secs)+"\n"), 0o600); err != nil {
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
			"PLUXY_BENCH_PROFILE_SECONDS=" + strconv.Itoa(secs),
		}
		proc, err := startCoreProcess(coreBin, cfgPath, env)
		if err != nil {
			os.RemoveAll(cfgDir)
			return nil, err
		}

		res, runErr := RunSide(ctx, spec, true, proc.BaseURL, benchClientKey, "bench-messages", mock)

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
