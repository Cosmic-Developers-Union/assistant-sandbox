#!/usr/bin/env python3
"""Drive the release CLI through a real PTY and retain a complete transcript."""

from __future__ import annotations

import argparse
import errno
import fcntl
import os
from pathlib import Path
import pty
import select
import struct
import subprocess
import termios
import time

BENCHMARK = "console.time('bench'); let s = 0; for (let i = 0; i < 1e8; i++) s += Math.sqrt(i); console.timeEnd('bench'); console.log(s);"
RESULT = "666666661666.567"


class PTYSession:
    def __init__(self, command: list[str], transcript: Path) -> None:
        self.transcript = transcript
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 120, 0, 0))
        self.process = subprocess.Popen(
            command,
            stdin=slave,
            stdout=slave,
            stderr=slave,
            start_new_session=True,
            close_fds=True,
        )
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
        start = len(self.pending)
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
        raise RuntimeError(f"timeout waiting for {description}; tail={bytes(self.pending[-2000:])!r}")

    def send(self, data: str) -> None:
        os.write(self.master, data.encode())

    def finish(self, timeout: float = 15) -> int:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.read(min(0.25, max(0.0, deadline - time.monotonic())))
            if self.process.poll() is not None:
                return self.process.returncode
        self.process.kill()
        raise RuntimeError("CLI did not exit after shell exit")


def run_case(binary: Path, transcript: Path, strict: bool) -> None:
    command = [str(binary)]
    if strict:
        command.append("--allow-guest-rwx=false")
    session = PTYSession(command, transcript)
    try:
        session.expect(b"sandbox:/work# ", 180, "initial shell prompt")
        session.send("node\n")
        if strict:
            session.expect(b"> ", 180, "strict Node prompt")
            session.send("console.log(process.version, process.arch, 6 * 7)\n")
            session.expect(b"42", 60, "strict Node arithmetic")
            session.expect(b"> ", 10, "strict Node prompt after arithmetic")
        else:
            session.expect(b"> ", 180, "Node prompt")
            session.send("process.version\n")
            session.expect(b"v22.", 30, "guest Node version")
            session.expect(b"> ", 10, "Node prompt after version")
            session.send(BENCHMARK + "\n")
            session.expect(b"bench:", 600, "benchmark timing")
            session.expect(RESULT.encode(), 60, "benchmark result")
            session.expect(b"> ", 60, "Node prompt after benchmark")
            session.send("s\n")
            session.expect(RESULT.encode(), 30, "persistent s variable")
            session.expect(b"> ", 10, "Node prompt after variable readback")
        session.send(".exit\n")
        session.expect(b"sandbox:/work# ", 60, "shell prompt after Node")
        session.send("exit\n")
        code = session.finish()
        if code != 0:
            raise RuntimeError(f"CLI exited with {code}")
    finally:
        if session.process.poll() is None:
            session.process.kill()
            session.process.wait()
        os.close(session.master)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=Path)
    parser.add_argument("transcript", type=Path)
    parser.add_argument("--strict", action="store_true")
    args = parser.parse_args()
    args.transcript.parent.mkdir(parents=True, exist_ok=True)
    args.transcript.write_bytes(b"")
    run_case(args.binary.resolve(), args.transcript, args.strict)
    print(f"PTY acceptance passed: {'strict' if args.strict else 'default'}; transcript={args.transcript}")


if __name__ == "__main__":
    main()
