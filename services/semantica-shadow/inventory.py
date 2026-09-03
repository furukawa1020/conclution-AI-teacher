"""Generate deterministic CycloneDX and license inventories from the locked image."""

from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
import pathlib
import re
import uuid
from dataclasses import dataclass

GENERATOR_VERSION = "1"
LOCK_HEADER = re.compile(r"^([A-Za-z0-9_.-]+)==([^\s\\]+)")
SHA256 = re.compile(r"--hash=sha256:([0-9a-f]{64})")
REQUIREMENT_NAME = re.compile(r"^([A-Za-z0-9_.-]+)")


def normalize_name(value: str) -> str:
    return re.sub(r"[-_.]+", "-", value).lower()


@dataclass(frozen=True)
class LockedPackage:
    name: str
    version: str
    hashes: tuple[str, ...]

    @property
    def ref(self) -> str:
        return f"pkg:pypi/{self.name}@{self.version}"


def parse_lock(path: pathlib.Path) -> dict[str, LockedPackage]:
    pending: dict[str, dict[str, object]] = {}
    current: str | None = None
    for line in path.read_text(encoding="utf-8").splitlines():
        header = LOCK_HEADER.match(line)
        if header:
            name = normalize_name(header.group(1))
            if name in pending:
                raise ValueError(f"duplicate locked package: {name}")
            pending[name] = {
                "name": name,
                "version": header.group(2),
                "hashes": [],
            }
            current = name
        if current is not None:
            pending[current]["hashes"].extend(SHA256.findall(line))

    result: dict[str, LockedPackage] = {}
    for name, raw in pending.items():
        hashes = tuple(sorted(set(raw["hashes"])))
        if not hashes:
            raise ValueError(f"locked package has no SHA-256: {name}")
        result[name] = LockedPackage(name, str(raw["version"]), hashes)
    if not result:
        raise ValueError("lock contains no packages")
    return result


def _short_license(value: str | None) -> str | None:
    if value is None:
        return None
    value = " ".join(value.split())
    if not value or value.upper() in {"UNKNOWN", "NONE"} or len(value) > 256:
        return None
    return value


def _record_digest(distribution: importlib.metadata.Distribution) -> str | None:
    record = distribution.read_text("RECORD")
    if record is None:
        return None
    return hashlib.sha256(record.encode("utf-8")).hexdigest()


def collect_installed(
    locked: dict[str, LockedPackage],
) -> dict[str, importlib.metadata.Distribution]:
    installed: dict[str, importlib.metadata.Distribution] = {}
    for distribution in importlib.metadata.distributions():
        raw_name = distribution.metadata.get("Name")
        if raw_name:
            installed[normalize_name(raw_name)] = distribution
    missing = sorted(set(locked) - set(installed))
    if missing:
        raise ValueError(f"locked packages missing from image: {', '.join(missing)}")
    for name, package in locked.items():
        if installed[name].version != package.version:
            raise ValueError(
                f"installed version mismatch for {name}: "
                f"{installed[name].version} != {package.version}"
            )
    return {name: installed[name] for name in locked}


def _license_evidence(distribution: importlib.metadata.Distribution) -> dict[str, object]:
    expression = _short_license(distribution.metadata.get("License-Expression"))
    declared = _short_license(distribution.metadata.get("License"))
    classifiers = sorted(
        value.removeprefix("License :: ")
        for value in distribution.metadata.get_all("Classifier", [])
        if value.startswith("License :: ")
    )
    concluded = expression or declared or ("; ".join(classifiers) if classifiers else "NOASSERTION")
    return {
        "concluded": concluded,
        "declared": declared,
        "expression": expression,
        "classifiers": classifiers,
    }


def _dependencies(
    package: LockedPackage,
    distribution: importlib.metadata.Distribution,
    locked: dict[str, LockedPackage],
) -> dict[str, object]:
    refs: set[str] = set()
    for raw in distribution.requires or []:
        match = REQUIREMENT_NAME.match(raw)
        if match is None:
            continue
        name = normalize_name(match.group(1))
        if name in locked and name != package.name:
            refs.add(locked[name].ref)
    return {"ref": package.ref, "dependsOn": sorted(refs)}


def build_documents(
    lock_path: pathlib.Path,
    source_commit: str,
) -> tuple[dict[str, object], dict[str, object]]:
    if not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise ValueError("source commit must be a full lowercase SHA-1")
    lock_bytes = lock_path.read_bytes()
    lock_digest = hashlib.sha256(lock_bytes).hexdigest()
    locked = parse_lock(lock_path)
    installed = collect_installed(locked)
    components: list[dict[str, object]] = []
    licenses: list[dict[str, object]] = []
    dependencies: list[dict[str, object]] = []

    for name in sorted(locked):
        package = locked[name]
        distribution = installed[name]
        license_evidence = _license_evidence(distribution)
        record_digest = _record_digest(distribution)
        properties = [
            {"name": "kotae:allowed-artifact-sha256-count", "value": str(len(package.hashes))},
            {
                "name": "kotae:allowed-artifact-sha256-set",
                "value": hashlib.sha256("\n".join(package.hashes).encode()).hexdigest(),
            },
        ]
        if record_digest:
            properties.append({"name": "kotae:installed-record-sha256", "value": record_digest})
        component: dict[str, object] = {
            "type": "library",
            "bom-ref": package.ref,
            "name": package.name,
            "version": package.version,
            "purl": package.ref,
            "properties": properties,
        }
        if license_evidence["expression"]:
            component["licenses"] = [{"expression": license_evidence["expression"]}]
        elif license_evidence["concluded"] != "NOASSERTION":
            component["licenses"] = [{"license": {"name": license_evidence["concluded"]}}]
        if package.name == "semantica":
            component["hashes"] = [{"alg": "SHA-256", "content": package.hashes[0]}]
        components.append(component)
        licenses.append(
            {
                "name": package.name,
                "version": package.version,
                **license_evidence,
            }
        )
        dependencies.append(_dependencies(package, distribution, locked))

    application_ref = f"urn:kotae:semantica-shadow:{source_commit}"
    dependencies.insert(0, {"ref": application_ref, "dependsOn": [p.ref for p in locked.values()]})
    dependencies[0]["dependsOn"].sort()
    serial = uuid.uuid5(uuid.NAMESPACE_URL, f"kotae:{source_commit}:{lock_digest}")
    sbom = {
        "bomFormat": "CycloneDX",
        "specVersion": "1.6",
        "serialNumber": f"urn:uuid:{serial}",
        "version": 1,
        "metadata": {
            "component": {
                "type": "application",
                "bom-ref": application_ref,
                "name": "kotae-semantica-shadow",
                "version": source_commit,
            },
            "properties": [
                {"name": "kotae:generator-version", "value": GENERATOR_VERSION},
                {"name": "kotae:requirements-lock-sha256", "value": lock_digest},
            ],
        },
        "components": components,
        "dependencies": dependencies,
    }
    license_document = {
        "schemaVersion": 1,
        "sourceCommit": source_commit,
        "requirementsLockSha256": lock_digest,
        "packageCount": len(licenses),
        "noAssertionCount": sum(item["concluded"] == "NOASSERTION" for item in licenses),
        "packages": licenses,
    }
    return sbom, license_document


def _write_json(path: pathlib.Path, value: dict[str, object]) -> None:
    path.write_text(
        json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--lock", type=pathlib.Path, required=True)
    parser.add_argument("--sbom", type=pathlib.Path, required=True)
    parser.add_argument("--licenses", type=pathlib.Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args()
    sbom, licenses = build_documents(args.lock, args.source_commit)
    _write_json(args.sbom, sbom)
    _write_json(args.licenses, licenses)


if __name__ == "__main__":
    main()
