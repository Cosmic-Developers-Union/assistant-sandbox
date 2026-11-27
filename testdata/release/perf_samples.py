#!/usr/bin/env python3
"""Pure statistical gate for paired performance samples.

The functions in this module do not launch processes, read files, or consult a
clock.  The small CLI at the bottom evaluates a JSON fixture so CI can prove
that an over-threshold sample produces a non-zero exit status.
"""

from __future__ import annotations

import argparse
import json
import math
from pathlib import Path
import statistics
import sys
from typing import Any, Iterable, Mapping, Sequence

DEFAULT_GATE = 3.0


def _validated_samples(samples: Iterable[float], side: str) -> list[float]:
    values = list(samples)
    if not values:
        raise ValueError(f"{side} samples must not be empty")
    validated: list[float] = []
    for index, value in enumerate(values):
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"{side} sample {index} must be numeric, got {value!r}")
        number = float(value)
        if not math.isfinite(number):
            raise ValueError(f"{side} sample {index} must be finite, got {value!r}")
        if number <= 0:
            raise ValueError(f"{side} sample {index} must be positive, got {value!r}")
        validated.append(number)
    return validated


def evaluate_samples(
    host_samples: Iterable[float],
    guest_samples: Iterable[float],
    gate: float = DEFAULT_GATE,
) -> dict[str, Any]:
    """Return medians and the guest/host ratio for two paired sample sets.

    Both sides must contain the same non-zero number of finite, positive
    durations.  A ratio exactly equal to ``gate`` passes; only a ratio above
    the gate fails.
    """

    if isinstance(gate, bool) or not isinstance(gate, (int, float)):
        raise ValueError(f"gate must be numeric, got {gate!r}")
    gate_value = float(gate)
    if not math.isfinite(gate_value) or gate_value <= 0:
        raise ValueError(f"gate must be finite and positive, got {gate!r}")

    host = _validated_samples(host_samples, "host")
    guest = _validated_samples(guest_samples, "guest")
    if len(host) != len(guest):
        raise ValueError(
            f"paired sample count differs: host={len(host)} guest={len(guest)}"
        )

    host_median = statistics.median(host)
    guest_median = statistics.median(guest)
    ratio = guest_median / host_median
    return {
        "sample_count": len(host),
        "host_samples": host,
        "guest_samples": guest,
        "host_median": host_median,
        "guest_median": guest_median,
        "ratio": ratio,
        "gate": gate_value,
        "passed": ratio <= gate_value,
    }


def evaluate_pair_samples(
    pairs: Sequence[Mapping[str, Mapping[str, Any]]],
    metric: str,
    gate: float = DEFAULT_GATE,
) -> dict[str, Any]:
    """Evaluate one numeric metric from ``pair['host'|'guest']`` records."""

    host: list[float] = []
    guest: list[float] = []
    for index, pair in enumerate(pairs):
        for side, destination in (("host", host), ("guest", guest)):
            try:
                destination.append(pair[side][metric])
            except (KeyError, TypeError) as exc:
                raise ValueError(
                    f"pair {index} {side} sample is missing metric {metric!r}"
                ) from exc
    return evaluate_samples(host, guest, gate)


def evaluate_fixture(fixture: Mapping[str, Any]) -> dict[str, Any]:
    """Evaluate the fixed-failure fixture format used by the unit tests."""

    try:
        host = fixture["host_samples"]
        guest = fixture["guest_samples"]
    except KeyError as exc:
        raise ValueError(f"fixture is missing {exc.args[0]!r}") from exc
    return evaluate_samples(host, guest, fixture.get("gate", DEFAULT_GATE))


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("fixture", type=Path)
    args = parser.parse_args(argv)
    try:
        fixture = json.loads(args.fixture.read_text(encoding="utf-8"))
        if not isinstance(fixture, dict):
            raise ValueError("fixture root must be a JSON object")
        result = evaluate_fixture(fixture)
    except (OSError, json.JSONDecodeError, TypeError, ValueError) as exc:
        print(f"evaluate performance samples: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, indent=2, sort_keys=True, allow_nan=False))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
