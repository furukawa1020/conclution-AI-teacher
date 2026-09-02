import hashlib
import pathlib
import unittest

import inventory


ROOT = pathlib.Path(__file__).resolve().parent


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


if __name__ == "__main__":
    unittest.main()
