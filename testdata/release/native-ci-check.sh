#!/bin/sh
set -eu

export ASSISTANT_REQUIRE_NATIVE=1
export PYTHONDONTWRITEBYTECODE=1
export GOFLAGS=-p=2
export GOMEMLIMIT=768MiB

mkdir -p native-evidence

go test ./instance -run '^TestNative' -count=1 -timeout 15m
go test ./cmd/assistant-sandbox \
  -run '^TestCLI(NativeBackendIdentity|AutoBackendIdentity|ExplicitRISCVBackend|RejectsUnknownBackend)$' \
  -count=1 -timeout 10m

make build VERSION=native-ci
python3 testdata/release/pty-node-check.py ./assistant-sandbox \
  native-evidence/native-x11.pty.log \
  --expect-backend linux/amd64 --expect-arch x64

python3 -m unittest discover -s testdata/release -p 'test_perf_samples.py' -v
set +e
python3 testdata/release/perf_samples.py testdata/release/perf-over-threshold.json \
  > native-evidence/perf-over-threshold.result.json
fixture_status=$?
set -e
if [ "$fixture_status" -ne 1 ]; then
  echo "over-threshold fixture exited $fixture_status, want 1" >&2
  exit 1
fi

python3 testdata/release/perf-node-check.py ./assistant-sandbox \
  --host-node /opt/node-v22.23.5-linux-x64/bin/node \
  --host-python /usr/local/bin/python3.12 \
  --output native-evidence/native-x12-performance.json \
  --transcript-dir native-evidence/native-x12-transcripts

python3 - <<'PY'
import json
from pathlib import Path
report = json.loads(Path("native-evidence/native-x12-performance.json").read_text())
if report["status"] != "complete" or not report["passed"]:
    raise SystemExit(f"native performance gate failed: {report.get('error')}")
print("native performance gate passed")
PY

sh testdata/release/build-release.sh native-evidence/dist native-ci
mkdir -p native-evidence/download
cp native-evidence/dist/assistant-sandbox-* native-evidence/dist/SBOM.txt \
  native-evidence/dist/SHA256SUMS native-evidence/download/
sh testdata/release/verify-download.sh native-evidence/download native-ci
PATH=/nonexistent /usr/local/bin/python3 testdata/release/pty-node-check.py \
  native-evidence/download/assistant-sandbox-linux-amd64 \
  native-evidence/downloaded-native-x11.pty.log \
  --expect-backend linux/amd64 --expect-arch x64
