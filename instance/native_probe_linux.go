//go:build linux

package instance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func probeNativeBackend(ctx context.Context) (BackendCapabilities, error) {
	capabilities := BackendCapabilities{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if err := ctx.Err(); err != nil {
		return capabilities, err
	}
	if !capabilities.availableArchitecture() {
		return capabilities, ErrNativeArchitecture
	}

	probeRoot, err := os.MkdirTemp("", "assistant-namespace-probe-")
	if err != nil {
		return capabilities, fmt.Errorf("create namespace probe directory: %w", err)
	}
	defer func() { _ = os.Remove(probeRoot) }()
	attributes := &os.ProcAttr{
		Dir:   "/",
		Env:   []string{"PATH=/usr/bin:/bin"},
		Files: []*os.File{},
		Sys: &syscall.SysProcAttr{
			Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS |
				syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC |
				syscall.CLONE_NEWUTS | syscall.CLONE_NEWCGROUP,
			UidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getuid(), Size: 1},
			},
			GidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getgid(), Size: 1},
			},
			GidMappingsEnableSetgroups: false,
			Pdeathsig:                  syscall.SIGKILL,
		},
	}
	probeScript := `if awk '$5 == "/" && /shared:/ { found=1 } END { exit !found }' /proc/self/mountinfo; then
  mount --make-rprivate / || exit 1
fi
mount --bind "$1" "$1"`
	process, err := os.StartProcess("/bin/sh", []string{"sh", "-c", probeScript, "sh", probeRoot}, attributes)
	if err != nil {
		return capabilities, fmt.Errorf("create namespace probe: %w", err)
	}
	state, err := process.Wait()
	if err != nil {
		return capabilities, fmt.Errorf("wait for namespace probe: %w", err)
	}
	if !state.Success() {
		return capabilities, fmt.Errorf("namespace probe exited with %v", state)
	}
	capabilities.Namespaces = true

	capabilities.Seccomp, err = seccompAvailable()
	if err != nil {
		return capabilities, err
	}
	capabilities.CgroupV2, capabilities.CgroupDelegated, err = delegatedCgroupAvailable(ctx)
	if err != nil {
		return capabilities, err
	}
	if !capabilities.available() {
		return capabilities, fmt.Errorf("%w: %+v", ErrNativeCapability, capabilities)
	}
	return capabilities, ctx.Err()
}

func (c BackendCapabilities) availableArchitecture() bool {
	return c.GOOS == "linux" && c.GOARCH == "amd64"
}

func seccompAvailable() (bool, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, fmt.Errorf("read seccomp status: %w", err)
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "Seccomp" {
			continue
		}
		mode, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return false, fmt.Errorf("parse seccomp mode: %w", err)
		}
		return mode >= 0 && mode <= 2, nil
	}
	return false, fmt.Errorf("seccomp status missing")
}

func delegatedCgroupAvailable(ctx context.Context) (bool, bool, error) {
	_, err := delegatedCgroupDirectory(ctx)
	return err == nil, err == nil, err
}

func delegatedCgroupDirectory(ctx context.Context) (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read current cgroup: %w", err)
	}
	var relative string
	for line := range strings.SplitSeq(string(data), "\n") {
		kind, path, ok := strings.Cut(line, "::")
		if ok && kind == "0" {
			relative = strings.TrimPrefix(path, "/")
			break
		}
	}
	if relative == "" {
		return "", fmt.Errorf("cgroup v2 path missing")
	}
	current := filepath.Join("/sys/fs/cgroup", filepath.Clean(relative))
	var lastErr error
	for current != "/sys/fs/cgroup" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		probe, err := os.MkdirTemp(current, ".assistant-sandbox-probe-")
		if err != nil {
			lastErr = err
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
			continue
		}
		controllers, readErr := os.ReadFile(filepath.Join(probe, "cgroup.controllers")) //nolint:gosec // probe is created by MkdirTemp under cgroupfs
		available := map[string]bool{}
		for _, controller := range strings.Fields(string(controllers)) {
			available[controller] = true
		}
		if readErr == nil && available["cpu"] && available["memory"] && available["pids"] {
			leaf := filepath.Join(probe, "vm")
			if err := os.Mkdir(leaf, 0o750); err == nil {
				if err := os.WriteFile(filepath.Join(probe, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0o600); err == nil { //nolint:gosec // probe is created by MkdirTemp under cgroupfs
					if err := os.WriteFile(filepath.Join(leaf, "memory.max"), []byte("64M"), 0o600); err == nil { //nolint:gosec // leaf is the probe child
						_ = os.WriteFile(filepath.Join(leaf, "cpu.max"), []byte("50000 100000"), 0o600)
						_ = os.WriteFile(filepath.Join(leaf, "pids.max"), []byte("8"), 0o600)
						_ = os.Remove(leaf)
						_ = os.Remove(probe)
						return current, nil
					}
				}
				_ = os.Remove(leaf)
			}
		}
		_ = os.Remove(probe)
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = fmt.Errorf("cgroup %s cannot enable cpu/memory/pids for a child", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("no writable delegated cgroup: %w", lastErr)
}
