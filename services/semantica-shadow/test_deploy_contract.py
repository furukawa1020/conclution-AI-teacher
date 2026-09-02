import json
import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parent


class DeploymentContractTest(unittest.TestCase):
    def test_contract_is_private_bounded_and_region_fixed(self):
        contract = json.loads((ROOT / "deploy-contract.json").read_text(encoding="utf-8"))
        self.assertEqual(contract["schemaVersion"], 1)
        self.assertEqual(contract["service"], "kotae-semantica-shadow")
        self.assertEqual(contract["callerService"], "kotae-api")
        self.assertEqual(contract["region"], "asia-northeast1")
        self.assertEqual(contract["ingress"], "all")
        self.assertIs(contract["allowUnauthenticated"], False)
        self.assertEqual(contract["minInstances"], 0)
        self.assertLessEqual(contract["maxInstances"], 2)
        self.assertLessEqual(contract["concurrency"], 8)
        self.assertLessEqual(contract["timeoutSeconds"], 5)

    def test_script_requires_digest_iam_and_post_deploy_verification(self):
        script = (ROOT / "deploy.ps1").read_text(encoding="utf-8")
        required = (
            "@sha256:[0-9a-f]{64}",
            ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd",
            "--ingress all",
            "--no-allow-unauthenticated",
            "roles/run.invoker",
            "caller Cloud Run service does not use the required invoker identity",
            "$authExitCode = $LASTEXITCODE",
            "get-iam-policy",
            "deployed image is not the requested immutable digest",
        )
        for marker in required:
            with self.subTest(marker=marker):
                self.assertIn(marker, script)
        self.assertNotIn("allUsers", script)
        self.assertNotIn("allAuthenticatedUsers", script)
        self.assertNotIn("auth list --filter=status:ACTIVE --format='value(account)' |", script)


if __name__ == "__main__":
    unittest.main()
