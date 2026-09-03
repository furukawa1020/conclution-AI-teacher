import json
import pathlib
import unittest

import app
import authenticated_graph_probe


ROOT = pathlib.Path(__file__).resolve().parent
REPO = ROOT.parents[1]
SNAPSHOT = REPO / "config" / "semantica-shadow-runtime-measurement.json"


class RuntimeMeasurementTest(unittest.TestCase):
    def test_graph_probe_payload_passes_the_real_receiver_boundary(self):
        value = app.decode_payload(authenticated_graph_probe.payload(1))
        self.assertIsNone(app.validate_payload(value))

    def test_script_uses_fixed_tools_metrics_and_fail_closed_checks(self):
        script = (ROOT / "measure.ps1").read_text(encoding="utf-8")
        required = (
            ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd",
            "run revisions describe",
            "revision image digest does not match",
            "run.googleapis.com/container/startup_latencies",
            "run.googleapis.com/container/memory/usage",
            "run.googleapis.com/request_latencies",
            "run.googleapis.com/request_latency/e2e_latencies",
            "run.googleapis.com/request_latency/pending",
            "no non-zero samples",
            "does not contain exactly $ExpectedSuccessCount",
        )
        for marker in required:
            with self.subTest(marker=marker):
                self.assertIn(marker, script)

    def test_snapshot_has_exact_provenance_and_no_false_percentile(self):
        value = json.loads(SNAPSHOT.read_text(encoding="utf-8"))
        self.assertEqual(value["schemaVersion"], 1)
        provenance = value["provenance"]
        self.assertEqual(provenance["projectId"], "kotae-ai-u22-2026")
        self.assertEqual(provenance["region"], "asia-northeast1")
        self.assertEqual(provenance["service"], "kotae-semantica-shadow")
        self.assertEqual(provenance["workload"], "content-free-fixed-graph-v1")
        self.assertEqual(provenance["expectedSuccessfulRequests"], 20)
        self.assertRegex(provenance["imageDigest"], r"^sha256:[0-9a-f]{64}$")
        self.assertGreater(provenance["imageSizeBytes"], 0)
        self.assertFalse(value["interpretationBoundary"]["percentileClaimed"])
        self.assertTrue(value["interpretationBoundary"]["requestLatencyExcludesStartup"])

    def test_every_metric_has_real_nonzero_observations(self):
        value = json.loads(SNAPSHOT.read_text(encoding="utf-8"))
        expected = {
            "containerStartupLatency",
            "containerMemoryUsage",
            "successfulRequestLatency",
            "successfulEndToEndLatency",
            "successfulPendingLatency",
        }
        self.assertEqual(set(value["metrics"]), expected)
        for name, metric in value["metrics"].items():
            with self.subTest(metric=name):
                self.assertGreater(metric["observationCount"], 0)
                self.assertGreater(metric["distributionPointCount"], 0)
                self.assertGreaterEqual(metric["weightedMean"], 0)
                self.assertLessEqual(metric["minimumPointMean"], metric["maximumPointMean"])

    def test_graph_probe_is_content_free_authenticated_and_self_cleaning(self):
        probe = (ROOT / "authenticated_graph_probe.py").read_text(encoding="utf-8")
        runner = (ROOT / "run-graph-probe.ps1").read_text(encoding="utf-8")
        self.assertNotIn("transcript", probe)
        self.assertNotIn("caption", probe)
        self.assertIn('"content-type": "application/json"', probe.lower())
        self.assertIn('target + "/v1/shadow/graphs"', probe)
        self.assertIn("metadata.google.internal", probe)
        self.assertIn("response.status != 204", probe)
        self.assertIn("kotae-api-runtime@$ProjectId.iam.gserviceaccount.com", runner)
        self.assertIn("refusing to replace an existing Cloud Run Job", runner)
        self.assertIn("finally", runner)
        self.assertIn("run jobs delete $JobName", runner)

    def test_documented_cloud_metric_values_match_the_snapshot(self):
        value = json.loads(SNAPSHOT.read_text(encoding="utf-8"))
        document = (REPO / "docs" / "semantica-shadow-runtime-measurement.md").read_text(
            encoding="utf-8"
        )
        expected = (
            f'{value["metrics"]["containerStartupLatency"]["weightedMean"]:,.3f} ms',
            f'{value["metrics"]["containerMemoryUsage"]["weightedMean"]:,.0f} bytes',
            f'{value["metrics"]["successfulRequestLatency"]["weightedMean"]:,.3f} ms',
            f'{value["metrics"]["successfulEndToEndLatency"]["weightedMean"]:,.3f} ms',
            f'{value["provenance"]["imageSizeBytes"]:,} bytes',
        )
        for rendered in expected:
            with self.subTest(rendered=rendered):
                self.assertIn(rendered, document)


if __name__ == "__main__":
    unittest.main()
