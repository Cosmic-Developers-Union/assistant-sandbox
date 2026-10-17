package commands_test

import (
	"maps"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands"
)

type ledgerBaseline struct {
	Stats struct {
		VSCNames      int            `json:"vscNames"`
		Registered    int            `json:"registered"`
		ShellBuiltins int            `json:"shellBuiltins"`
		SurfaceCounts map[string]int `json:"surfaceCounts"`
	} `json:"stats"`
	Statuses                 []ledgerStatus        `json:"statuses"`
	UnregisteredDispositions map[string]string     `json:"unregisteredDispositions"`
	ReasonMappings           []ledgerReasonMapping `json:"reasonMappings"`
}

type ledgerStatus struct {
	Name    string `json:"name"`
	Surface string `json:"surface"`
	Status  string `json:"status"`
}

type ledgerReasonMapping struct {
	Category string   `json:"category"`
	Names    []string `json:"names"`
	Landed   bool     `json:"landed"`
}

func TestLedgerStatsAndStatuses(t *testing.T) {
	baseline := readCommandBaseline(t)
	registered := commands.Registry(testRunner)
	statuses := make(map[string]string, len(baseline.Ledger.Statuses))
	surfaces := make(map[string]int)
	counts := make(map[string]int)
	for _, entry := range baseline.Ledger.Statuses {
		if _, exists := statuses[entry.Name]; exists {
			t.Errorf("台账重复登记 %s", entry.Name)
		}
		statuses[entry.Name] = entry.Status
		surfaces[entry.Surface]++
		counts[entry.Status]++
		if entry.Status != "已注册" && entry.Status != "待移植" && entry.Status != "shell 内建（本仓库提供）" {
			t.Errorf("命令 %s 状态无效: %q", entry.Name, entry.Status)
		}
		if entry.Status == "已注册" && registered[entry.Name] == nil {
			t.Errorf("台账将 %s 标记为已注册，但命令注册表中不存在", entry.Name)
		}
		if entry.Status == "待移植" && registered[entry.Name] != nil {
			t.Errorf("台账将 %s 标记为待移植，但命令注册表已包含它", entry.Name)
		}
	}
	if len(statuses) != baseline.Ledger.Stats.VSCNames {
		t.Errorf("台账名称数 = %d，统计为 %d", len(statuses), baseline.Ledger.Stats.VSCNames)
	}
	if counts["已注册"] != baseline.Ledger.Stats.Registered {
		t.Errorf("已注册数量 = %d，统计为 %d", counts["已注册"], baseline.Ledger.Stats.Registered)
	}
	if counts["shell 内建（本仓库提供）"] != baseline.Ledger.Stats.ShellBuiltins {
		t.Errorf("shell 内建数量 = %d，统计为 %d", counts["shell 内建（本仓库提供）"], baseline.Ledger.Stats.ShellBuiltins)
	}
	if !maps.Equal(surfaces, baseline.Ledger.Stats.SurfaceCounts) {
		t.Errorf("移植面统计不匹配: %v", surfaces)
	}
}

func TestLedgerUnregisteredDispositionsMatchCoverage(t *testing.T) {
	baseline := readCommandBaseline(t)
	statuses := make(map[string]string, len(baseline.Ledger.Statuses))
	for _, entry := range baseline.Ledger.Statuses {
		statuses[entry.Name] = entry.Status
	}
	coverage := make(map[string]string, len(baseline.Coverage.POSIX))
	for _, entry := range baseline.Coverage.POSIX {
		coverage[entry.Name] = entry.Disposition
	}
	for name, disposition := range baseline.Ledger.UnregisteredDispositions {
		if got := coverage[name]; got != disposition {
			t.Errorf("未注册名称 %s 的处置 = %q，POSIX 基准为 %q", name, disposition, got)
		}
		if statuses[name] != "待移植" {
			t.Errorf("未注册处置中的 %s 状态为 %q", name, statuses[name])
		}
	}
	for name, status := range statuses {
		_, listed := baseline.Ledger.UnregisteredDispositions[name]
		if status == "待移植" && !listed {
			t.Errorf("待移植名称 %s 缺少处置", name)
		}
		if status != "待移植" && listed {
			t.Errorf("已处理名称 %s 仍出现在未注册处置中", name)
		}
	}
	if len(baseline.Ledger.UnregisteredDispositions) != 7 {
		t.Errorf("未注册处置数量 = %d，预期 7", len(baseline.Ledger.UnregisteredDispositions))
	}
}

func TestLedgerReasonMappingsMatchStatuses(t *testing.T) {
	baseline := readCommandBaseline(t)
	statuses := make(map[string]string, len(baseline.Ledger.Statuses))
	for _, entry := range baseline.Ledger.Statuses {
		statuses[entry.Name] = entry.Status
	}
	checked := 0
	for _, mapping := range baseline.Ledger.ReasonMappings {
		if mapping.Category == "" || len(mapping.Names) == 0 {
			t.Errorf("原因映射缺少分类或名称: %+v", mapping)
		}
		for _, name := range mapping.Names {
			status, exists := statuses[name]
			if !exists {
				t.Errorf("原因映射中的名称 %s 不在台账内", name)
				continue
			}
			checked++
			switch {
			case mapping.Landed && status != "已注册" && status != "shell 内建（本仓库提供）":
				t.Errorf("原因分类 %q 将 %s 标记为已落地，但状态是 %q", mapping.Category, name, status)
			case !mapping.Landed && status == "已注册":
				t.Errorf("原因分类 %q 将已注册的 %s 留作待办", mapping.Category, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("没有可核对的名称原因映射")
	}
}
