#!/usr/bin/env python3
"""Run the X12 real-PTY Node and Python/Node performance gates.

The Node benchmark uses five fresh host/guest REPL pairs.  The six language
scenes reuse the scripts from internal/guest/performance_languages_test.go and
use five fresh process pairs per scene.  Guest invocations use the default CLI
with no backend override.  stdout is one raw JSON report; a failed gate exits
non-zero after the report has been written.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import pty
import re
import select
import shlex
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time
from typing import Any, Sequence

from perf_samples import DEFAULT_GATE, evaluate_pair_samples

BENCHMARK = "console.time('bench'); let s = 0; for (let i = 0; i < 1e8; i++) s += Math.sqrt(i); console.timeEnd('bench'); console.log(s);"
RESULT = "666666661666.567"
EXPECTED_NODE_VERSION = "v22.23.5"
EXPECTED_PYTHON_VERSION = "3.12.16"
GUEST_BACKEND = "linux/amd64"
GUEST_NODE = "/usr/bin/node"
GUEST_PYTHON = "/usr/bin/python3.12"
PAIR_COUNT = 5

PYTHON_STARTUP = "print(42)"
PYTHON_INTEGER = """x = 0
for i in range(1000000):
    x += i
print(x)
"""
PYTHON_FILE = """from pathlib import Path
path = Path("perf.bin")
data = b"x" * (1 << 20)
path.write_bytes(data)
assert path.read_bytes() == data
print(len(data))
"""

NODE_STARTUP = "console.log(42)"
NODE_INTEGER = """let x = 0;
for (let i = 0; i < 100000; i++) x += i;
process.stdout.write(String(x) + "\\n");
"""
NODE_FILE = """const fs = require("node:fs");
const data = Buffer.alloc(1 << 20, 120);
fs.writeFileSync("perf.bin", data);
if (!fs.readFileSync("perf.bin").equals(data)) throw Error("data");
console.log(data.length);
"""

ANSI = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]")
BENCH_TIMING = re.compile(rb"bench:\s*([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)\r?\n")
NODE_METADATA = re.compile(rb"__PERF_META__(\{[^\r\n]*\})")


class HarnessError(RuntimeError):
    pass


class PTYSession:
    """A small reusable real-PTY session with byte-exact transcript capture."""

    def __init__(self, command: list[str], transcript: Path) -> None:
        self.transcript = transcript
        self.transcript.parent.mkdir(parents=True, exist_ok=True)
        self.transcript.write_bytes(b"")
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 120, 0, 0))
        try:
            self.launch_started = time.monotonic()
            self.process = subprocess.Popen(
                command,
                stdin=slave,
                stdout=slave,
                stderr=slave,
                start_new_session=True,
                close_fds=True,
            )
        except BaseException:
            os.close(slave)
            os.close(self.master)
            raise
        os.close(slave)
        self.pending = bytearray()
        self.closed = False

    def read(self, timeout: float) -> bytes:
        ready, _, _ = select.select([self.master], [], [], timeout)
        if not ready:
            return b""
        try:
            chunk = os.read(self.master, 65536)
        except OSError as exc:
            if exc.errno == errno.EIO:
                self.closed = True
                return b""
            raise
        if not chunk:
            self.closed = True
            return b""
        with self.transcript.open("ab") as output:
            output.write(chunk)
        self.pending.extend(chunk)
        return chunk

    def expect(self, needle: bytes, timeout: float, description: str) -> bytes:
        deadline = time.monotonic() + timeout
        start = 0
        while time.monotonic() < deadline:
            found = self.pending.find(needle, start)
            if found >= 0:
                end = found + len(needle)
                data = bytes(self.pending[start:end])
                del self.pending[:end]
                return data
            self.read(min(0.25, max(0.0, deadline - time.monotonic())))
            if self.closed and self.process.poll() is not None:
                break
        raise HarnessError(
            f"timeout waiting for {description}; tail={bytes(self.pending[-2000:])!r}"
        )

    def expect_match(
        self, pattern: re.Pattern[bytes], timeout: float, description: str
    ) -> re.Match[bytes]:
        deadline = time.monotonic() + timeout
        start = 0
        while time.monotonic() < deadline:
            found = pattern.search(bytes(self.pending), start)
            if found is not None:
                end = found.end()
                del self.pending[:end]
                return found
            self.read(min(0.25, max(0.0, deadline - time.monotonic())))
            if self.closed and self.process.poll() is not None:
                break
        raise HarnessError(
            f"timeout waiting for {description}; tail={bytes(self.pending[-2000:])!r}"
        )

    def send(self, data: str) -> None:
        encoded = data.encode()
        while encoded:
            written = os.write(self.master, encoded)
            encoded = encoded[written:]

    def finish(self, timeout: float = 15) -> int:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.read(min(0.25, max(0.0, deadline - time.monotonic())))
            if self.process.poll() is not None:
                return self.process.returncode
        raise HarnessError("process did not exit after shell/REPL exit")

    def close(self) -> None:
        try:
            if self.process.poll() is None:
                try:
                    os.killpg(self.process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=5)
        finally:
            os.close(self.master)


def resolve_program(value: str) -> str:
    if os.sep in value:
        path = Path(value).expanduser().resolve()
        if not path.is_file() or not os.access(path, os.X_OK):
            raise HarnessError(f"program is not executable: {path}")
        return str(path)
    found = shutil.which(value)
    if found is None:
        raise HarnessError(f"program not found in PATH: {value}")
    return str(Path(found).resolve())


def command_output(command: Sequence[str], timeout: float = 30) -> str:
    completed = subprocess.run(
        list(command), capture_output=True, text=True, timeout=timeout, check=False
    )
    if completed.returncode != 0:
        raise HarnessError(
            f"command {shlex.join(command)} exited {completed.returncode}: "
            f"{completed.stderr.strip()}"
        )
    return (completed.stdout + completed.stderr).strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def cpu_model() -> str | None:
    try:
        for line in Path("/proc/cpuinfo").read_text(encoding="utf-8").splitlines():
            if line.lower().startswith("model name") and ":" in line:
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return None


def physical_cores() -> int | None:
    try:
        lines = Path("/proc/cpuinfo").read_text(encoding="utf-8").splitlines()
    except OSError:
        return None
    cores: set[tuple[str, str]] = set()
    physical: str | None = None
    core: str | None = None
    for line in lines + [""]:
        if not line:
            if physical is not None and core is not None:
                cores.add((physical, core))
            physical = None
            core = None
        elif ":" in line:
            key, value = (part.strip() for part in line.split(":", 1))
            if key == "physical id":
                physical = value
            elif key == "core id":
                core = value
    return len(cores) or None


def memory_bytes() -> int | None:
    try:
        pages = os.sysconf("SC_PHYS_PAGES")
        page_size = os.sysconf("SC_PAGE_SIZE")
        return int(pages) * int(page_size)
    except (OSError, ValueError):
        return None


def git_commit() -> str | None:
    candidates = [Path.cwd(), Path(__file__).resolve().parents[2]]
    visited: set[Path] = set()
    for directory in candidates:
        if directory in visited:
            continue
        visited.add(directory)
        try:
            return command_output(["git", "-C", str(directory), "rev-parse", "HEAD"])
        except (HarnessError, OSError, subprocess.SubprocessError):
            continue
    return None


def collect_metadata(
    binary: Path, host_node: str, host_python: str, expected_node: str
) -> dict[str, Any]:
    commit = git_commit()
    try:
        go_version: str | None = command_output(["go", "version"])
    except (HarnessError, OSError, subprocess.SubprocessError):
        go_version = None

    host_node_version = command_output([host_node, "--version"])
    host_python_version = command_output([host_python, "--version"])
    return {
        "commit": commit,
        "binary": str(binary),
        "binary_sha256": sha256_file(binary),
        "hardware": {
            "system": platform.system(),
            "machine": platform.machine(),
            "cpu_model": cpu_model(),
            "logical_cpus": os.cpu_count(),
            "physical_cores": physical_cores(),
            "memory_bytes": memory_bytes(),
        },
        "kernel": platform.release(),
        "kernel_version": platform.version(),
        "go": go_version,
        "node": {
            "expected_version": expected_node,
            "host_path": host_node,
            "host_version": host_node_version,
            "guest_path": GUEST_NODE,
        },
        "python": {
            "harness_executable": sys.executable,
            "harness_version": platform.python_version(),
            "harness_implementation": platform.python_implementation(),
            "host_path": host_python,
            "host_version": host_python_version,
            "guest_path": GUEST_PYTHON,
        },
        "execArgv": {},
        "started_at_unix": time.time(),
    }


def probe_guest(binary: Path, expected_node: str, expected_python: str) -> tuple[str, str]:
    command = (
        'if [ "$ASSISTANT_SANDBOX_BACKEND" != "linux/amd64" ]; then '
        'echo "wrong guest backend: $ASSISTANT_SANDBOX_BACKEND" >&2; exit 97; fi; '
        'printf "%s\\n" "$ASSISTANT_SANDBOX_BACKEND"; '
        f"{GUEST_NODE} --version; {GUEST_PYTHON} --version"
    )
    completed = subprocess.run(
        [str(binary), "-c", command],
        capture_output=True,
        text=True,
        timeout=180,
        check=False,
    )
    if completed.returncode != 0:
        raise HarnessError(
            f"guest runtime probe exited {completed.returncode}: {completed.stderr.strip()}"
        )
    lines = completed.stdout.splitlines()
    if len(lines) != 3 or lines[0] != GUEST_BACKEND:
        raise HarnessError(f"unexpected guest runtime probe output: {completed.stdout!r}")
    if lines[1] != expected_node:
        raise HarnessError(
            f"guest Node version {lines[1]!r} != expected {expected_node!r}"
        )
    if not re.fullmatch(r"Python 3\.12\.\d+", lines[2]):
        raise HarnessError(f"unexpected guest Python version: {lines[2]!r}")
    guest_python = lines[2].removeprefix("Python ")
    if guest_python != expected_python:
        raise HarnessError(
            f"guest Python version {guest_python!r} != expected {expected_python!r}"
        )
    return lines[1], guest_python


def strip_ansi(data: bytes) -> bytes:
    return ANSI.sub(b"", data)


def bench_seconds(match: re.Match[bytes]) -> tuple[float, str]:
    value = float(match.group(1))
    unit = match.group(2)
    scale = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}[unit.decode()]
    raw = f"{match.group(1).decode()}{unit.decode()}"
    return value * scale, raw


def run_node_repl(
    command: list[str],
    transcript: Path,
    expected_node: str,
    *,
    guest: bool,
    startup_timeout: float,
    bench_timeout: float,
) -> dict[str, Any]:
    session = PTYSession(command, transcript)
    try:
        if guest:
            session.expect(b"sandbox:/work# ", startup_timeout, "initial shell prompt")
            session.send("node\n")
        session.expect(b"> ", startup_timeout, "first Node prompt")
        startup = time.monotonic() - session.launch_started
        expression = (
            "console.log('__PERF_META__' + JSON.stringify({"
            "version: process.version, arch: process.arch, "
            "backend: process.env.ASSISTANT_SANDBOX_BACKEND || null, "
            "execPath: process.execPath, execArgv: process.execArgv}))"
        )
        session.send(expression + "\n")
        metadata_match = session.expect_match(
            NODE_METADATA, 30, "Node runtime metadata"
        )
        node = json.loads(strip_ansi(metadata_match.group(1)))
        if node.get("version") != expected_node:
            raise HarnessError(
                f"{'guest' if guest else 'host'} Node version {node.get('version')!r} "
                f"!= expected {expected_node!r}"
            )
        if not isinstance(node.get("execArgv"), list):
            raise HarnessError(f"Node execArgv is not an array: {node.get('execArgv')!r}")
        if guest:
            if node.get("backend") != GUEST_BACKEND:
                raise HarnessError(
                    f"actual guest backend {node.get('backend')!r} != {GUEST_BACKEND!r}"
                )
            if node.get("arch") != "x64":
                raise HarnessError(
                    f"guest process.arch {node.get('arch')!r} != 'x64'"
                )
        session.expect(b"> ", 30, "Node prompt after metadata")

        session.send(BENCHMARK + "\n")
        timing = session.expect_match(BENCH_TIMING, bench_timeout, "benchmark timing")
        elapsed, raw_timing = bench_seconds(timing)
        session.expect(RESULT.encode(), 60, "benchmark result")
        session.expect(b"> ", 60, "Node prompt after benchmark")
        session.send("s\n")
        session.expect(RESULT.encode(), 30, "persistent s variable")
        session.expect(b"> ", 30, "Node prompt after variable readback")
        session.send(".exit\n")
        if guest:
            session.expect(b"sandbox:/work# ", 60, "shell prompt after Node")
            session.send("exit\n")
        code = session.finish()
        if code != 0:
            raise HarnessError(f"{'guest' if guest else 'host'} REPL exited {code}")
        return {
            "startup_seconds": startup,
            "bench_seconds": elapsed,
            "bench_raw": raw_timing,
            "result": RESULT,
            "s_readback": RESULT,
            "node": node,
        }
    finally:
        session.close()


@dataclass(frozen=True)
class LanguageScene:
    key: str
    runtime: str
    name: str
    script: str
    evaluator_flag: str
    guest_program: str
    expected_stdout: str


SCENES = (
    LanguageScene(
        "python_startup", "python", "startup", PYTHON_STARTUP, "-c", GUEST_PYTHON, "42\n"
    ),
    LanguageScene(
        "python_integer",
        "python",
        "integer",
        PYTHON_INTEGER,
        "-c",
        GUEST_PYTHON,
        "499999500000\n",
    ),
    LanguageScene(
        "python_file_1mib",
        "python",
        "file_1mib",
        PYTHON_FILE,
        "-c",
        GUEST_PYTHON,
        "1048576\n",
    ),
    LanguageScene(
        "node_startup", "node", "startup", NODE_STARTUP, "-e", GUEST_NODE, "42\n"
    ),
    LanguageScene(
        "node_integer",
        "node",
        "integer",
        NODE_INTEGER,
        "-e",
        GUEST_NODE,
        "4999950000\n",
    ),
    LanguageScene(
        "node_file_1mib",
        "node",
        "file_1mib",
        NODE_FILE,
        "-e",
        GUEST_NODE,
        "1048576\n",
    ),
)


def run_timed_command(
    command: list[str], *, cwd: Path | None, timeout: float, side: str
) -> dict[str, Any]:
    started = time.monotonic()
    try:
        completed = subprocess.run(
            command,
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise HarnessError(f"{side} command timed out after {timeout:g}s") from exc
    elapsed = time.monotonic() - started
    if completed.returncode != 0:
        raise HarnessError(
            f"{side} command exited {completed.returncode}: {completed.stderr.strip()}"
        )
    return {
        "seconds": elapsed,
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


class GuestShell:
    """Persistent default-CLI shell used to exclude VM startup from scenes."""

    def __init__(self, binary: Path, transcript: Path) -> None:
        self.session = PTYSession([str(binary), "--session"], transcript)
        self.counter = 0
        try:
            self.session.expect(b"sandbox:/work# ", 180, "initial guest shell prompt")
            self.session.send("stty -echo\n")
            self.session.expect(b"sandbox:/work# ", 30, "quiet guest shell prompt")
        except BaseException:
            self.session.close()
            raise

    def run(self, command: str, timeout: float) -> dict[str, Any]:
        self.counter += 1
        token = f"{os.getpid()}-{self.counter}"
        marker = re.compile(
            rb"(.*)\x1ePERF-END:" + token.encode() + rb":(\d+)\x1f", re.DOTALL
        )
        wrapped = (
            "cd /work && "
            + command
            + "; __assistant_perf_status=$?; printf '\\036PERF-END:"
            + token
            + ":%d\\037' \"$__assistant_perf_status\"\n"
        )
        started = time.monotonic()
        self.session.send(wrapped)
        found = self.session.expect_match(marker, timeout, "guest scene marker")
        elapsed = time.monotonic() - started
        output = found.group(1).decode(errors="replace").replace("\r\n", "\n")
        output = strip_ansi(output.encode()).decode(errors="replace")
        status = int(found.group(2))
        self.session.expect(b"sandbox:/work# ", 30, "guest shell prompt after scene")
        if status != 0:
            raise HarnessError(f"guest scene exited {status}: {output!r}")
        return {"seconds": elapsed, "stdout": output, "stderr": "", "backend_asserted": GUEST_BACKEND}

    def close(self) -> None:
        try:
            if self.session.process.poll() is None:
                self.session.send("exit\n")
                self.session.finish()
        finally:
            self.session.close()


def run_language_sample(
    scene: LanguageScene,
    side: str,
    *,
    guest_shell: GuestShell,
    host_program: str,
    timeout: float,
) -> dict[str, Any]:
    arguments = [scene.evaluator_flag, scene.script]
    if side == "host":
        with tempfile.TemporaryDirectory(prefix="assistant-perf-host-") as directory:
            sample = run_timed_command(
                [host_program, *arguments],
                cwd=Path(directory),
                timeout=timeout,
                side=f"{scene.key} host",
            )
    else:
        sample = guest_shell.run(shlex.join([scene.guest_program, *arguments]), timeout)
    if sample["stdout"] != scene.expected_stdout:
        raise HarnessError(
            f"{scene.key} {side} stdout {sample['stdout']!r} != {scene.expected_stdout!r}"
        )
    return sample


def pair_order(pair_number: int) -> tuple[str, str]:
    return ("host", "guest") if pair_number % 2 else ("guest", "host")


def run_node_gate(
    binary: Path,
    host_node: str,
    expected_node: str,
    transcripts: Path,
    startup_timeout: float,
    bench_timeout: float,
    report: dict[str, Any],
    persist: Any,
) -> None:
    pairs: list[dict[str, Any]] = report["node_repl"]["pairs"]
    for pair_number in range(1, PAIR_COUNT + 1):
        order = pair_order(pair_number)
        pair: dict[str, Any] = {"pair": pair_number, "order": list(order)}
        for side in order:
            command = [host_node] if side == "host" else [str(binary)]
            pair[side] = run_node_repl(
                command,
                transcripts / f"node-repl-pair-{pair_number:02d}-{side}.pty.log",
                expected_node,
                guest=side == "guest",
                startup_timeout=startup_timeout,
                bench_timeout=bench_timeout,
            )
        pair["ratio"] = pair["guest"]["bench_seconds"] / pair["host"]["bench_seconds"]
        pairs.append(pair)
        if pair_number == 1:
            report["metadata"]["execArgv"] = {
                "host_node": pair["host"]["node"]["execArgv"],
                "guest_node": pair["guest"]["node"]["execArgv"],
            }
            report["metadata"]["node"].update(
                {
                    "guest_version": pair["guest"]["node"]["version"],
                    "guest_arch": pair["guest"]["node"]["arch"],
                    "guest_backend": pair["guest"]["node"]["backend"],
                    "guest_exec_path": pair["guest"]["node"]["execPath"],
                    "host_arch": pair["host"]["node"]["arch"],
                    "host_exec_path": pair["host"]["node"]["execPath"],
                }
            )
        update_node_summary(report["node_repl"])
        persist()


def update_node_summary(section: dict[str, Any]) -> None:
    bench = evaluate_pair_samples(section["pairs"], "bench_seconds", DEFAULT_GATE)
    startup = evaluate_pair_samples(section["pairs"], "startup_seconds", DEFAULT_GATE)
    section.update(
        {
            "bench": bench,
            "startup": startup,
            "ratio": bench["ratio"],
            "gate": DEFAULT_GATE,
            "passed": bench["passed"],
        }
    )


def run_language_gate(
    scene: LanguageScene,
    *,
    binary: Path,
    host_program: str,
    timeout: float,
    transcripts: Path,
    report: dict[str, Any],
    persist: Any,
) -> None:
    section = report["language_scenes"][scene.key]
    pairs: list[dict[str, Any]] = section["pairs"]
    guest_shell = GuestShell(binary, transcripts / f"{scene.key}-guest.pty.log")
    try:
        for pair_number in range(1, PAIR_COUNT + 1):
            order = pair_order(pair_number)
            pair: dict[str, Any] = {"pair": pair_number, "order": list(order)}
            for side in order:
                pair[side] = run_language_sample(
                    scene,
                    side,
                    guest_shell=guest_shell,
                    host_program=host_program,
                    timeout=timeout,
                )
            pair["ratio"] = pair["guest"]["seconds"] / pair["host"]["seconds"]
            pairs.append(pair)
            update_scene_summary(section)
            persist()
    finally:
        guest_shell.close()


def update_scene_summary(section: dict[str, Any]) -> None:
    summary = evaluate_pair_samples(section["pairs"], "seconds", DEFAULT_GATE)
    section.update(summary)


def new_report(metadata: dict[str, Any]) -> dict[str, Any]:
    language_scenes: dict[str, Any] = {}
    for scene in SCENES:
        language_scenes[scene.key] = {
            "runtime": scene.runtime,
            "scene": scene.name,
            "script": scene.script,
            "evaluator_flag": scene.evaluator_flag,
            "guest_program": scene.guest_program,
            "expected_stdout": scene.expected_stdout,
            "pairs": [],
        }
    return {
        "schema_version": 1,
        "status": "running",
        "passed": False,
        "gate": DEFAULT_GATE,
        "pair_count": PAIR_COUNT,
        "benchmark": BENCHMARK,
        "expected_result": RESULT,
        "metadata": metadata,
        "node_repl": {"pairs": []},
        "language_scenes": language_scenes,
    }


def write_report(path: Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            encoding="utf-8",
            dir=path.parent,
            prefix=f".{path.name}.",
            suffix=".tmp",
            delete=False,
        ) as output:
            temporary = Path(output.name)
            json.dump(report, output, indent=2, sort_keys=True, allow_nan=False)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def emit_report(report: dict[str, Any]) -> None:
    print(json.dumps(report, indent=2, sort_keys=True, allow_nan=False))


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", nargs="?", type=Path, default=Path("./assistant-sandbox"))
    parser.add_argument("--output", type=Path, default=Path("perf-results.json"))
    parser.add_argument("--transcript-dir", type=Path)
    parser.add_argument("--host-node", default="node")
    parser.add_argument("--host-python", default="python3")
    parser.add_argument("--expected-node-version", default=EXPECTED_NODE_VERSION)
    parser.add_argument("--expected-python-version", default=EXPECTED_PYTHON_VERSION)
    parser.add_argument("--startup-timeout", type=float, default=180)
    parser.add_argument("--bench-timeout", type=float, default=900)
    parser.add_argument("--scene-timeout", type=float, default=300)
    args = parser.parse_args(argv)
    if args.startup_timeout <= 0 or args.bench_timeout <= 0 or args.scene_timeout <= 0:
        parser.error("timeouts must be positive")
    return args


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    output = args.output.resolve()
    transcripts = (
        args.transcript_dir.expanduser().resolve()
        if args.transcript_dir is not None
        else output.parent / "perf-pty-transcripts"
    )
    report: dict[str, Any] = {
        "schema_version": 1,
        "status": "running",
        "passed": False,
        "error": None,
    }

    def persist() -> None:
        write_report(output, report)

    try:
        transcripts.mkdir(parents=True, exist_ok=True)
        binary = args.binary.expanduser().resolve()
        if not binary.is_file() or not os.access(binary, os.X_OK):
            raise HarnessError(f"binary is not executable: {binary}")
        host_node = resolve_program(args.host_node)
        host_python = resolve_program(args.host_python)
        metadata = collect_metadata(
            binary, host_node, host_python, args.expected_node_version
        )
        report = new_report(metadata)
        persist()
        if metadata["node"]["host_version"] != args.expected_node_version:
            raise HarnessError(
                f"host Node version {metadata['node']['host_version']!r} "
                f"!= expected {args.expected_node_version!r}"
            )
        if metadata["python"]["host_version"].removeprefix("Python ") != args.expected_python_version:
            raise HarnessError(
                f"host Python version {metadata['python']['host_version']!r} "
                f"!= expected {args.expected_python_version!r}"
            )
        guest_node, guest_python = probe_guest(
            binary, args.expected_node_version, args.expected_python_version
        )
        if guest_node != metadata["node"]["host_version"]:
            raise HarnessError(
                f"guest Node version {guest_node!r} != host Node version "
                f"{metadata['node']['host_version']!r}"
            )
        report["metadata"]["node"]["guest_version"] = guest_node
        report["metadata"]["python"]["guest_version"] = guest_python
        persist()

        run_node_gate(
            binary,
            host_node,
            args.expected_node_version,
            transcripts,
            args.startup_timeout,
            args.bench_timeout,
            report,
            persist,
        )
        for scene in SCENES:
            host_program = host_python if scene.runtime == "python" else host_node
            run_language_gate(
                scene,
                binary=binary,
                host_program=host_program,
                timeout=args.scene_timeout,
                transcripts=transcripts,
                report=report,
                persist=persist,
            )

        node_passed = report["node_repl"]["passed"]
        scenes_passed = all(
            section["passed"] for section in report["language_scenes"].values()
        )
        report["status"] = "complete"
        report["passed"] = node_passed and scenes_passed
        report["finished_at_unix"] = time.time()
        persist()
    except Exception as exc:
        report["status"] = "error"
        report["passed"] = False
        report["error"] = f"{type(exc).__name__}: {exc}"
        try:
            persist()
        except OSError as write_exc:
            report["output_error"] = f"{type(write_exc).__name__}: {write_exc}"

    emit_report(report)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
