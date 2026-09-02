import hashlib
import json
import pathlib
import unittest

import inventory


ROOT = pathlib.Path(__file__).resolve().parent
CONFIG = ROOT.parents[1] / "config"


class InventoryTest(unittest.TestCase):
    def test_lock_parser_covers_every_hashed_requirement(self):
        lock = inventory.parse_lock(ROOT / "requirements.lock")
        self.assertGreater(len(lock), 100)
        self.assertIn("semantica", lock)
        self.assertEqual(lock["semantica"].version, "0.6.5")
        self.assertIn(
            "5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0",
            lock["semantica"].hashes,
        )
        for package in lock.values():
            with self.subTest(package=package.name):
                self.assertTrue(package.hashes)
                self.assertTrue(all(len(value) == 64 for value in package.hashes))

    def test_name_normalization_is_pep_503_compatible(self):
        self.assertEqual(inventory.normalize_name("typing_extensions"), "typing-extensions")
        self.assertEqual(inventory.normalize_name("Flask.Cors"), "flask-cors")

    def test_generator_and_cloud_build_contract_are_fixed(self):
        dockerfile = (ROOT / "Dockerfile").read_text(encoding="utf-8")
        build = (ROOT / "cloudbuild.image.yaml").read_text(encoding="utf-8")
        ignore = (ROOT / ".gcloudignore").read_text(encoding="utf-8")
        self.assertIn("COPY inventory.py ./inventory.py", dockerfile)
        self.assertIn("!inventory.py", ignore)
        self.assertIn("semantica-shadow-sbom.cdx.json", build)
        self.assertIn("semantica-shadow-licenses.json", build)
        self.assertIn("semantica-shadow-inventory/$BUILD_ID/", build)
        expected_base = (
            "python:3.11.9-slim-bookworm@sha256:"
            "2856e6af199e8128161abd320575eb9b341f3b76f017b5d0c9cd364f60d8a050"
        )
        self.assertIn(expected_base, build)

    def test_lock_digest_is_stable(self):
        first = hashlib.sha256((ROOT / "requirements.lock").read_bytes()).hexdigest()
        second = hashlib.sha256((ROOT / "requirements.lock").read_bytes()).hexdigest()
        self.assertEqual(first, second)

    def test_checked_in_inventory_matches_the_complete_lock(self):
        lock = inventory.parse_lock(ROOT / "requirements.lock")
        sbom = json.loads(
            (CONFIG / "semantica-shadow-sbom.cdx.json").read_text(encoding="utf-8")
        )
        licenses = json.loads(
            (CONFIG / "semantica-shadow-licenses.json").read_text(encoding="utf-8")
        )
        self.assertEqual(sbom["bomFormat"], "CycloneDX")
        self.assertEqual(sbom["specVersion"], "1.6")
        components = {item["name"]: item for item in sbom["components"]}
        self.assertEqual(set(components), set(lock))
        self.assertEqual(
            {name: item["version"] for name, item in components.items()},
            {name: item.version for name, item in lock.items()},
        )
        license_versions = {
            item["name"]: item["version"] for item in licenses["packages"]
        }
        self.assertEqual(license_versions, {name: item.version for name, item in lock.items()})
        self.assertEqual(licenses["packageCount"], len(lock))
        lock_digest = hashlib.sha256((ROOT / "requirements.lock").read_bytes()).hexdigest()
        self.assertEqual(licenses["requirementsLockSha256"], lock_digest)
        metadata_properties = {
            item["name"]: item["value"] for item in sbom["metadata"]["properties"]
        }
        self.assertEqual(metadata_properties["kotae:requirements-lock-sha256"], lock_digest)
        semantica_hashes = {
            item["content"] for item in components["semantica"]["hashes"]
        }
        self.assertIn(
            "5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0",
            semantica_hashes,
        )

    def test_unknown_licenses_remain_explicit_instead_of_being_guessed(self):
        licenses = json.loads(
            (CONFIG / "semantica-shadow-licenses.json").read_text(encoding="utf-8")
        )
        unknown = {
            item["name"] for item in licenses["packages"] if item["concluded"] == "NOASSERTION"
        }
        self.assertEqual(unknown, {"cuda-toolkit", "py-rust-stemmers"})
        self.assertEqual(licenses["noAssertionCount"], len(unknown))


if __name__ == "__main__":
    unittest.main()
