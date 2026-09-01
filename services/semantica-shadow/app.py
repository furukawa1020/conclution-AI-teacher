"""Private, content-free Semantica shadow receiver for Cloud Run."""

from __future__ import annotations

import hashlib
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable


MAX_BODY_BYTES = 4096
SCHEMA_VERSION = 1
QUESTION_SCHEMA = "qba.v1"
SEMANTICA_VERSION = "0.6.5"
SEMANTICA_WHEEL_SHA256 = (
    "5bc33a5529aaa496dfdbca6d0bfc8301cb8f795b127e360d93ecb5b16a6a5fc0"
)

EXPECTED_NODES = (
    ("question", "Question"),
    ("utterance", "RespondentUtterance"),
    ("claim", "Claim"),
    ("verification", "Verification"),
)
EXPECTED_EDGE_PREFIX = (
    ("question", "utterance", "elicits"),
    ("utterance", "claim", "expresses"),
    ("claim", "verification", "verified_as"),
)
RELATIONS = frozenset({"direct", "restatement", "unresolved", "conflict"})


class RequestRejected(ValueError):
    """A request failed the closed, content-free schema boundary."""


def _unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise RequestRejected("duplicate key")
        result[key] = value
    return result


def _reject_constant(_: str) -> None:
    raise RequestRejected("non-finite number")


def decode_payload(raw: bytes) -> dict[str, Any]:
    if not raw or len(raw) > MAX_BODY_BYTES:
        raise RequestRejected("body size")
    try:
        value = json.loads(
            raw,
            object_pairs_hook=_unique_object,
            parse_constant=_reject_constant,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, RequestRejected) as exc:
        raise RequestRejected("invalid json") from exc
    if not isinstance(value, dict):
        raise RequestRejected("root type")
    validate_payload(value)
    return value


def _exact_keys(value: Any, keys: set[str]) -> bool:
    return isinstance(value, dict) and set(value) == keys


def validate_payload(value: dict[str, Any]) -> None:
    if not _exact_keys(value, {"schemaVersion", "provenance", "nodes", "edges"}):
        raise RequestRejected("root schema")
    if type(value["schemaVersion"]) is not int or value["schemaVersion"] != SCHEMA_VERSION:
        raise RequestRejected("schema version")

    provenance = value["provenance"]
    if not _exact_keys(
        provenance,
        {"turnDigest", "questionSchema", "semanticaVersion", "semanticaWheelSha256"},
    ):
        raise RequestRejected("provenance schema")
    digest = provenance["turnDigest"]
    if not isinstance(digest, str) or len(digest) != 64:
        raise RequestRejected("turn digest")
    try:
        bytes.fromhex(digest)
    except ValueError as exc:
        raise RequestRejected("turn digest") from exc
    if provenance["questionSchema"] != QUESTION_SCHEMA:
        raise RequestRejected("question schema")
    if provenance["semanticaVersion"] != SEMANTICA_VERSION:
        raise RequestRejected("semantica version")
    if provenance["semanticaWheelSha256"] != SEMANTICA_WHEEL_SHA256:
        raise RequestRejected("wheel digest")

    nodes = value["nodes"]
    if not isinstance(nodes, list) or len(nodes) != len(EXPECTED_NODES):
        raise RequestRejected("node count")
    for node, expected in zip(nodes, EXPECTED_NODES, strict=True):
        if not _exact_keys(node, {"id", "type"}):
            raise RequestRejected("node schema")
        if (node["id"], node["type"]) != expected:
            raise RequestRejected("node value")

    edges = value["edges"]
    if not isinstance(edges, list) or len(edges) != len(EXPECTED_EDGE_PREFIX):
        raise RequestRejected("edge count")
    for index, (edge, expected) in enumerate(zip(edges, EXPECTED_EDGE_PREFIX, strict=True)):
        required = {"source", "target", "type", "relation"} if index == 2 else {"source", "target", "type"}
        if not _exact_keys(edge, required):
            raise RequestRejected("edge schema")
        if (edge["source"], edge["target"], edge["type"]) != expected:
            raise RequestRejected("edge value")
        if index == 2 and edge["relation"] not in RELATIONS:
            raise RequestRejected("relation")


def canonical_digest(value: dict[str, Any]) -> str:
    encoded = json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode()
    return hashlib.sha256(b"kotae-semantica-shadow-graph-v1\x00" + encoded).hexdigest()


def reconstruct_graph(
    value: dict[str, Any], graph_factory: Callable[[], Any] | None = None
) -> tuple[Any, str]:
    validate_payload(value)
    if graph_factory is None:
        from semantica.context import ContextGraph

        graph_factory = ContextGraph

    graph = graph_factory()
    provenance = value["provenance"]
    for node in value["nodes"]:
        properties: dict[str, str] = {}
        if node["id"] == "verification":
            properties = {
                "turn_digest": provenance["turnDigest"],
                "question_schema": QUESTION_SCHEMA,
                "semantica_version": SEMANTICA_VERSION,
                "semantica_wheel_sha256": SEMANTICA_WHEEL_SHA256,
            }
        accepted = graph.add_node(node["id"], node["type"], content=None, **properties)
        if accepted is False:
            raise RuntimeError("semantica rejected finite node")
    for edge in value["edges"]:
        properties = {"relation": edge["relation"]} if "relation" in edge else {}
        accepted = graph.add_edge(edge["source"], edge["target"], edge["type"], **properties)
        if accepted is False:
            raise RuntimeError("semantica rejected finite edge")
    return graph, canonical_digest(value)


class ShadowHandler(BaseHTTPRequestHandler):
    server_version = "kotae-semantica-shadow"
    sys_version = ""

    def log_message(self, _format: str, *_args: Any) -> None:
        # Never copy request paths, bodies, headers, or exception strings to logs.
        return

    def _problem(self, status: int, code: str) -> None:
        body = json.dumps({"code": code}, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/problem+json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path != "/healthz":
            self._problem(404, "not_found")
            return
        self.send_response(204)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path != "/v1/shadow/graphs":
            self._problem(404, "not_found")
            return
        if self.headers.get_content_type() != "application/json":
            self._problem(415, "unsupported_media_type")
            return
        length_header = self.headers.get("Content-Length")
        try:
            length = int(length_header) if length_header is not None else -1
        except ValueError:
            length = -1
        if length < 1 or length > MAX_BODY_BYTES:
            self._problem(413, "invalid_body_size")
            return
        raw = self.rfile.read(length)
        if len(raw) != length:
            self._problem(400, "invalid_request")
            return
        try:
            reconstruct_graph(decode_payload(raw))
        except RequestRejected:
            self._problem(400, "invalid_request")
            return
        except Exception:
            self._problem(503, "graph_unavailable")
            return
        self.send_response(204)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()


def main() -> None:
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), ShadowHandler)
    server.daemon_threads = True
    server.serve_forever()


if __name__ == "__main__":
    main()
