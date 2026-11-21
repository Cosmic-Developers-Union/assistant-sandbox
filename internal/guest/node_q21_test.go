package guest_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestNode22ChildProcess 验证 Q21:spawn/execFile 的管道、stdin、
// 退出码、信号、环境变量,且子程序是客体 /bin/sh。
const q21SignalDigest = "2dfe5cfc187b5ee1457b873b1eeb6c753c4c10d788f7f715287193f51f39bbb6"

func TestNode22ChildProcess(t *testing.T) {
	machine, connection := node22RealtimeGuest(t)
	installGuestShell(t, machine)
	signalProbe := python312Asset(t, "testdata/q21-signal", q21SignalDigest)
	writeGuestFile(t, machine, "/bin/q21-signal", signalProbe, 0o755)
	script := `const { spawn, execFile } = require("node:child_process");

function collect(child) {
  return new Promise((resolve) => {
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (data) => { stdout += data.toString(); });
    child.stderr.on("data", (data) => { stderr += data.toString(); });
    child.on("close", (code, signal) => resolve({ stdout, stderr, code, signal, pid: child.pid }));
  });
}

(async () => {
  const env = { ...process.env, Q21_CHILD: "child-env" };
  const result = {};

  const echo = spawn("/bin/sh", ["-c", 'printf "%s" "$Q21_CHILD"; printf "warning" >&2; exit 7'], {
    env,
    stdio: ["pipe", "pipe", "pipe"],
  });
  result.spawn = await collect(echo);

  const stdin = spawn("/bin/sh", ["-c", 'IFS= read -r line; printf "got:%s" "$line"'], {
    env,
    stdio: ["pipe", "pipe", "pipe"],
  });
  result.stdinWriteError = "";
  stdin.stdin.on("error", (error) => { result.stdinWriteError = error.code; });
  stdin.stdin.end("ping\n");
  result.stdin = await collect(stdin);

  result.execFile = await new Promise((resolve) => {
    execFile("/bin/sh", ["-c", 'printf "exec:%s" "$Q21_CHILD"'], { env }, (error, stdout, stderr) => {
      resolve({ error: error && error.code, stdout, stderr });
    });
  });

  const signalProbe = spawn("/bin/q21-signal", [], {
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let signalOutput = "";
  signalProbe.stdout.on("data", (data) => { signalOutput += data.toString(); });
  await new Promise((resolve) => signalProbe.stdout.once("data", resolve));
  signalProbe.kill("SIGTERM");
  result.killed = await new Promise((resolve) => {
    signalProbe.on("close", (code, signal) => resolve({ code, signal, stdout: signalOutput }));
  });
  result.mainPid = process.pid;

  process.stdout.write(JSON.stringify(result) + "\n");
})().catch((error) => {
  process.stderr.write(String(error && error.stack || error) + "\n");
  process.exitCode = 1;
});
`
	writeGuestFile(t, machine, "/work/q21_child_process.js", []byte(script), 0o644)
	output, diagnostic, code := runNode22(t, connection, []string{"--jitless", "/work/q21_child_process.js"}, 300*time.Second)
	if code != 0 {
		if status, err := machine.Status(t.Context()); err == nil {
			for _, report := range status.Crashes {
				t.Logf("crash pid=%d tid=%d signal=%d trap=%q pc=%#x", report.PID, report.TID, report.Signal, report.Trap, report.PC)
			}
		}
		t.Fatalf("child_process exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	var result struct {
		Spawn struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Code   int    `json:"code"`
			PID    int    `json:"pid"`
		} `json:"spawn"`
		Stdin struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Code   int    `json:"code"`
		} `json:"stdin"`
		StdinWriteError string `json:"stdinWriteError"`
		ExecFile        struct {
			Error  any    `json:"error"`
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		} `json:"execFile"`
		Killed struct {
			Code   int    `json:"code"`
			Signal string `json:"signal"`
			Stdout string `json:"stdout"`
		} `json:"killed"`
		MainPID int `json:"mainPid"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("child_process 输出非法: %v (%q) stderr=%q", err, output, diagnostic)
	}
	if result.Spawn.Stdout != "child-env" || result.Spawn.Stderr != "warning" || result.Spawn.Code != 7 {
		t.Fatalf("spawn=%+v", result.Spawn)
	}
	if result.Spawn.PID <= 0 || result.Spawn.PID == result.MainPID {
		t.Fatalf("子进程 pid=%d 主线 pid=%d", result.Spawn.PID, result.MainPID)
	}
	if result.Stdin.Stdout != "got:ping" || result.Stdin.Code != 0 || result.StdinWriteError != "" {
		t.Fatalf("stdin 管道=%+v writeError=%q", result.Stdin, result.StdinWriteError)
	}
	if result.ExecFile.Error != nil || result.ExecFile.Stdout != "exec:child-env" {
		t.Fatalf("execFile=%+v", result.ExecFile)
	}
	if result.Killed.Code != 0 || !strings.Contains(result.Killed.Stdout, "ready") || !strings.Contains(result.Killed.Stdout, "signal") {
		t.Fatalf("SIGTERM 子进程 close=%+v", result.Killed)
	}
}
