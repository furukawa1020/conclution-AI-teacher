"""Authenticated, content-free graph probe executed inside a temporary Cloud Run Job."""

from __future__ import annotations

import json
import os
import statistics
import time
import urllib.parse
import urllib.request


SEMANTICA_WHEEL_SHA256 = (
    "5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0"
)


def payload(index: int) -> bytes:
    value = {
        "schemaVersion": 1,
        "provenance": {
            "turnDigest": f"{index:064x}",
            "questionSchema": "qba.v1",
            "semanticaVersion": "0.6.5",
            "semanticaWheelSha256": SEMANTICA_WHEEL_SHA256,
        },
        "nodes": [
            {"id": "question", "type": "Question"},
            {"id": "utterance", "type": "RespondentUtterance"},
            {"id": "claim", "type": "Claim"},
            {"id": "verification", "type": "Verification"},
        ],
        "edges": [
            {"source": "question", "target": "utterance", "type": "elicits"},
            {"source": "utterance", "target": "claim", "type": "expresses"},
            {
                "source": "claim",
                "target": "verification",
                "type": "verified_as",
                "relation": "direct",
            },
        ],
    }
    return json.dumps(value, separators=(",", ":"), sort_keys=True).encode()


def identity_token(audience: str) -> str:
    encoded_audience = urllib.parse.quote(audience, safe="")
    url = (
        "http://metadata.google.internal/computeMetadata/v1/instance/"
        "service-accounts/default/identity?audience="
        f"{encoded_audience}&format=full"
    )
    request = urllib.request.Request(url, headers={"Metadata-Flavor": "Google"})
    return urllib.request.urlopen(request, timeout=10).read().decode()


def main() -> None:
    target = os.environ["KOTAE_TARGET_URL"].rstrip("/")
    audience = os.environ.get("KOTAE_AUDIENCE", target)
    count = int(os.environ.get("KOTAE_PROBE_COUNT", "20"))
    if count < 1 or count > 100:
        raise ValueError("KOTAE_PROBE_COUNT must be between 1 and 100")
    token = identity_token(audience)
    durations: list[float] = []
    for index in range(count):
        request = urllib.request.Request(
            target + "/v1/shadow/graphs",
            data=payload(index),
            headers={
                "Authorization": "Bearer " + token,
                "Content-Type": "application/json",
            },
            method="POST",
        )
        started = time.perf_counter()
        response = urllib.request.urlopen(request, timeout=30)
        body = response.read()
        durations.append((time.perf_counter() - started) * 1000)
        if response.status != 204 or body or response.headers.get("Cache-Control") != "no-store":
            raise RuntimeError("shadow receiver response contract failed")
    print(
        json.dumps(
            {
                "schemaVersion": 1,
                "requestCount": count,
                "status": 204,
                "bodyBytes": 0,
                "clientLatencyMs": {
                    "minimum": min(durations),
                    "mean": statistics.fmean(durations),
                    "maximum": max(durations),
                },
            },
            separators=(",", ":"),
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
