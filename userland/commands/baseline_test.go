package commands_test

import (
	"encoding/json/v2"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands"
)

type commandBaseline struct {
	SourceCommit string `json:"sourceCommit"`
	Coverage     struct {
		Registry []coverageEntry `json:"registry"`
		POSIX    []posixEntry    `json:"posix"`
		Stats    struct {
			Dispositions map[string]int `json:"dispositions"`
			POSIXNames   int            `json:"posixNames"`
			VSCNames     int            `json:"vscNames"`
		} `json:"stats"`
	} `json:"coverage"`
	Ledger ledgerBaseline `json:"ledger"`
}

type coverageEntry struct {
	Name         string `json:"name"`
	Completeness string `json:"completeness"`
}

type posixEntry struct {
	Name        string `json:"name"`
	VSC         bool   `json:"vsc"`
	Disposition string `json:"disposition"`
}

func readCommandBaseline(t *testing.T) commandBaseline {
	t.Helper()
	data, err := os.ReadFile("testdata/command-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline commandBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.SourceCommit == "" {
		t.Fatal("基准缺少来源提交")
	}
	return baseline
}

func TestBaselineMatchesRegistry(t *testing.T) {
	baseline := readCommandBaseline(t)
	registered := commands.Registry(testRunner)
	wantNames := make([]string, 0, len(baseline.Coverage.Registry))
	seen := make(map[string]bool, len(baseline.Coverage.Registry))
	for _, entry := range baseline.Coverage.Registry {
		if seen[entry.Name] {
			t.Fatalf("基准重复登记 %s", entry.Name)
		}
		seen[entry.Name] = true
		if entry.Completeness != "完整" && entry.Completeness != "受限" {
			t.Errorf("命令 %s 完整度无效: %q", entry.Name, entry.Completeness)
		}
		wantNames = append(wantNames, entry.Name)
	}
	gotNames := slices.Sorted(maps.Keys(registered))
	slices.Sort(wantNames)
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("注册表与基准不一致: 实际 %d 项，基准 %d 项", len(gotNames), len(wantNames))
	}
}

func TestBaselineCommandDirectories(t *testing.T) {
	for _, entry := range readCommandBaseline(t).Coverage.Registry {
		for _, file := range []string{"command.go", "command_test.go"} {
			if _, err := os.Stat(entry.Name + "/" + file); err != nil {
				t.Errorf("命令 %s 缺少 %s", entry.Name, file)
			}
		}
	}
}

func TestBaselinePOSIXInventory(t *testing.T) {
	baseline := readCommandBaseline(t)
	registered := commands.Registry(testRunner)
	counts := make(map[string]int, len(baseline.Coverage.Stats.Dispositions)-1)
	for disposition := range baseline.Coverage.Stats.Dispositions {
		if disposition != "合计" {
			counts[disposition] = 0
		}
	}
	vscCount := 0
	posixNames := make(map[string]bool, len(baseline.Coverage.POSIX))
	dispositions := make(map[string]string, len(baseline.Coverage.POSIX))
	for _, entry := range baseline.Coverage.POSIX {
		if posixNames[entry.Name] {
			t.Errorf("POSIX 基准重复登记 %s", entry.Name)
		}
		posixNames[entry.Name] = true
		dispositions[entry.Name] = entry.Disposition
		counts[entry.Disposition]++
		if entry.VSC {
			vscCount++
		}
		if entry.Disposition == "命令" && registered[entry.Name] == nil {
			t.Errorf("POSIX 名称 %s 标记为命令但未注册", entry.Name)
		}
	}
	for name := range registered {
		if disposition, ok := dispositions[name]; ok && disposition != "命令" {
			t.Errorf("已注册命令 %s 在 POSIX 基准中标记为 %q", name, disposition)
		}
	}
	if len(posixNames) != baseline.Coverage.Stats.POSIXNames {
		t.Errorf("POSIX 名称数 = %d，基准统计为 %d", len(posixNames), baseline.Coverage.Stats.POSIXNames)
	}
	if vscCount != baseline.Coverage.Stats.VSCNames {
		t.Errorf("VSC 名称数 = %d，基准统计为 %d", vscCount, baseline.Coverage.Stats.VSCNames)
	}
	wantCounts := make(map[string]int, len(baseline.Coverage.Stats.Dispositions)-1)
	for disposition, count := range baseline.Coverage.Stats.Dispositions {
		if disposition != "合计" {
			wantCounts[disposition] = count
		}
	}
	if !maps.Equal(counts, wantCounts) {
		t.Errorf("POSIX 处置统计不匹配: 实际 %v，基准 %v", counts, wantCounts)
	}
	if total := baseline.Coverage.Stats.Dispositions["合计"]; total != len(baseline.Coverage.POSIX) {
		t.Errorf("POSIX 统计合计 = %d，名称数 %d", total, len(baseline.Coverage.POSIX))
	}
}
