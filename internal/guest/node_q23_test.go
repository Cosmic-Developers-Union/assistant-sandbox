package guest_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	q23AddonDigest    = "b663e7ad5ac1178095c0f3b672b3efdc832e6f8e9fd21734ce54b518cc9c4c4d"
	q23WrongABIDigest = "9c9f64dd7170c9f9deec81f2b5fec972dea2c83837413904fbb7bb3799b09681"
)

// TestNode22NativeAddon 验证 Q23:目标架构 riscv64 N-API 扩展可加载
// 并调用,错误架构的 ELF 返回可诊断的 dlopen 错误。
func TestNode22NativeAddon(t *testing.T) {
	machine, connection := node22Guest(t)
	addon := python312Asset(t, "testdata/nodejs/q23/q23_addon.node", q23AddonDigest)
	wrong := python312Asset(t, "testdata/nodejs/q23/q23_addon_x86_64.node", q23WrongABIDigest)
	writeGuestFile(t, machine, "/work/q23_addon.node", addon, 0o755)
	writeGuestFile(t, machine, "/work/q23_addon_x86_64.node", wrong, 0o755)
	script := `const result = {};
try {
  const addon = require("/work/q23_addon.node");
  result.add = addon.add(2, 3);
  result.type = typeof addon.add;
} catch (error) {
  result.load = { code: error.code, message: error.message };
}
try {
  require("/work/q23_addon_x86_64.node");
  result.wrong = { code: "loaded" };
} catch (error) {
  result.wrong = { code: error.code, message: error.message };
}
process.stdout.write(JSON.stringify(result) + "\n");
`
	writeGuestFile(t, machine, "/work/q23_addon.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q23_addon.js"}, 240*time.Second)
	if code != 0 {
		if status, err := machine.Status(t.Context()); err == nil {
			for _, report := range status.Crashes {
				t.Logf("crash pid=%d signal=%d trap=%q pc=%#x ra=%#x", report.PID, report.Signal, report.Trap, report.PC, report.Registers[1])
				for _, mapping := range report.Mappings {
					if mapping.Start <= report.PC && report.PC < mapping.End {
						t.Logf("pc mapping %#x-%#x %s %s", mapping.Start, mapping.End, mapping.Prot, mapping.Name)
					}
				}
			}
		}
		t.Fatalf("N-API exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result struct {
		Add  float64 `json:"add"`
		Type string  `json:"type"`
		Load struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"load"`
		Wrong struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"wrong"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("N-API 输出非法: %v (%q)", err, output)
	}
	if result.Load.Code != "" {
		t.Fatalf("目标扩展装载失败: %+v", result.Load)
	}
	if result.Add != 5 || result.Type != "function" {
		t.Fatalf("N-API 调用=%v type=%q", result.Add, result.Type)
	}
	if result.Wrong.Code != "ERR_DLOPEN_FAILED" {
		t.Fatalf("错误架构 code=%q message=%q", result.Wrong.Code, result.Wrong.Message)
	}
	message := strings.ToLower(result.Wrong.Message)
	if !strings.Contains(message, "elf") && !strings.Contains(message, "class") && !strings.Contains(message, "machine") && !strings.Contains(message, "load") {
		t.Fatalf("错误架构诊断不可读: %q", result.Wrong.Message)
	}
}
