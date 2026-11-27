#!/usr/bin/env python3
"""Unit tests for the pure performance sample evaluator."""

from __future__ import annotations

import json
import math
from pathlib import Path
import subprocess
import sys
import unittest

from perf_samples import evaluate_fixture, evaluate_pair_samples, evaluate_samples

FIXTURE = Path(__file__).with_name("perf-over-threshold.json")
EVALUATOR = Path(__file__).with_name("perf_samples.py")


class EvaluateSamplesTests(unittest.TestCase):
    def test_median_and_ratio_use_all_five_pairs(self) -> None:
        result = evaluate_samples([5, 1, 4, 2, 3], [10, 2, 8, 4, 6])
        self.assertEqual(result["sample_count"], 5)
        self.assertEqual(result["host_median"], 3)
        self.assertEqual(result["guest_median"], 6)
        self.assertEqual(result["ratio"], 2)
        self.assertTrue(result["passed"])

    def test_ratio_exactly_at_gate_passes(self) -> None:
        result = evaluate_samples([0.5, 0.5], [1.5, 1.5])
        self.assertEqual(result["ratio"], 3)
        self.assertTrue(result["passed"])

    def test_ratio_above_gate_fails(self) -> None:
        result = evaluate_samples([1, 1], [3.0001, 3.0001])
        self.assertGreater(result["ratio"], 3)
        self.assertFalse(result["passed"])

    def test_pair_metric_evaluation(self) -> None:
        pairs = [
            {"host": {"bench_seconds": 1}, "guest": {"bench_seconds": 2}},
            {"host": {"bench_seconds": 3}, "guest": {"bench_seconds": 6}},
        ]
        result = evaluate_pair_samples(pairs, "bench_seconds")
        self.assertEqual(result["host_median"], 2)
        self.assertEqual(result["guest_median"], 4)
        self.assertEqual(result["ratio"], 2)

    def test_mismatched_pair_counts_are_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "sample count differs"):
            evaluate_samples([1, 2], [2])

    def test_empty_samples_are_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "must not be empty"):
            evaluate_samples([], [])

    def test_invalid_durations_are_rejected(self) -> None:
        invalid = [0, -1, math.inf, -math.inf, math.nan, True, "1.0", None]
        for value in invalid:
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    evaluate_samples([1], [value])

    def test_invalid_gate_is_rejected(self) -> None:
        for gate in [0, -1, math.inf, math.nan, True, "3"]:
            with self.subTest(gate=gate):
                with self.assertRaises(ValueError):
                    evaluate_samples([1], [1], gate)

    def test_inputs_are_not_mutated(self) -> None:
        host = [3, 1, 2]
        guest = [6, 2, 4]
        evaluate_samples(host, guest)
        self.assertEqual(host, [3, 1, 2])
        self.assertEqual(guest, [6, 2, 4])


class FixedFailureFixtureTests(unittest.TestCase):
    def test_fixed_over_threshold_fixture_fails(self) -> None:
        fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
        result = evaluate_fixture(fixture)
        self.assertEqual(result["sample_count"], 5)
        self.assertEqual(result["host_median"], 1.0)
        self.assertEqual(result["guest_median"], 3.03)
        self.assertGreater(result["ratio"], 3.0)
        self.assertFalse(result["passed"])

    def test_fixture_cli_returns_nonzero_and_raw_json(self) -> None:
        completed = subprocess.run(
            [sys.executable, str(EVALUATOR), str(FIXTURE)],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 1, completed.stderr)
        self.assertEqual(completed.stderr, "")
        result = json.loads(completed.stdout)
        self.assertFalse(result["passed"])
        self.assertGreater(result["ratio"], 3.0)


if __name__ == "__main__":
    unittest.main()
