package instance

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestTopologyAndMemoryOptionsAcceptArbitraryValues(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		cpus    int
		memory  uint64
		session uint64
	}{
		{"最小", MinCPUs, MinMemoryBytes, MinMemoryBytes},
		{"任意中小值", 3, 300 << 20, 300 << 20},
		{"非 2 次幂", 7, 5_500_000_000, 5_500_000_000},
		{"最大", MaxCPUs, MaxMemoryBytes, MaxMemoryBytes},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			machine, err := New(t.Context(), Options{
				Deterministic: true,
				Seed:          1,
				CPUs:          testCase.cpus,
				MemoryBytes:   testCase.memory,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = machine.Close(context.Background()) }()
			status, err := machine.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if status.Session.MemoryLimit != testCase.session {
				t.Fatalf("会话内存=%d want %d", status.Session.MemoryLimit, testCase.session)
			}
		})
	}
}

func TestTopologyAndMemoryOptionsRejectOutOfRange(t *testing.T) {
	for _, options := range []Options{
		{CPUs: -1},
		{CPUs: MaxCPUs + 1},
		{MemoryBytes: MinMemoryBytes - 1},
		{MemoryBytes: MaxMemoryBytes + 1},
	} {
		if machine, err := New(t.Context(), options); err == nil {
			_ = machine.Close(context.Background())
			t.Fatalf("Options=%+v 应失败", options)
		}
	}
}

func TestAffinityAndMeminfoReflectConfiguredValues(t *testing.T) {
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          1,
		CPUs:          4,
		MemoryBytes:   300 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process, err := connection.Exec(runCtx, ExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", "nproc; head -1 /proc/meminfo"},
		Dir:     "/work",
		Env:     map[string]string{"PATH": "/bin:/usr/bin"},
		Streams: fd.Streams{Output: output, Error: output},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Wait(runCtx, process.ID); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "4\n") || !strings.Contains(text, "307200 kB") {
		t.Fatalf("输出=%q", text)
	}
}

// TestMultiCoreTypicalConfigs 在 1/2/4 三个典型配置上让 4 个客体进程
// 同时越过同一屏障执行,验证令牌数、亲和性与进程结算没有互相破坏。
func TestMultiCoreTypicalConfigs(t *testing.T) {
	for _, cpus := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("%dc", cpus), func(t *testing.T) {
			machine, err := New(t.Context(), Options{
				Deterministic: false,
				Seed:          1,
				CPUs:          cpus,
				MemoryBytes:   4 << 30,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = machine.Close(context.Background()) }()
			if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
				t.Fatal(err)
			}
			connection, err := machine.Connect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			type guestProcess struct {
				process proc.Proc
				output  *bytes.Buffer
			}
			processes := make([]guestProcess, 0, 4)
			for index := range 4 {
				output := &bytes.Buffer{}
				process, err := connection.Exec(t.Context(), ExecSpec{
					Program: "/bin/sh",
					Args: []string{"-c",
						fmt.Sprintf("printf '%d' $$ > /work/pid-%d; while [ ! -e /work/go ]; do :; done; nproc", index, index)},
					Dir:     "/work",
					Env:     map[string]string{"PATH": "/bin:/usr/bin"},
					Streams: fd.Streams{Output: output, Error: output},
				})
				if err != nil {
					t.Fatal(err)
				}
				processes = append(processes, guestProcess{process: process, output: output})
			}
			time.Sleep(50 * time.Millisecond)
			barrier, err := machine.Filesystem().Open(t.Context(), "/work/go", vmfs.Create|vmfs.ReadWrite, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			_ = barrier.Close()
			waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, item := range processes {
				state, err := connection.Wait(waitCtx, item.process.ID)
				if err != nil || state.ExitCode != 0 {
					t.Fatalf("CPU=%d 进程 %d 退出=%+v err=%v output=%q",
						cpus, item.process.ID, state, err, item.output.String())
				}
				if strings.TrimSpace(item.output.String()) != fmt.Sprint(cpus) {
					t.Fatalf("CPU=%d nproc=%q", cpus, item.output.String())
				}
			}
		})
	}
}
