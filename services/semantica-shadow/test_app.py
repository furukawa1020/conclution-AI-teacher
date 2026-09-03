import copy
import json
import sys
import types
import unittest
from unittest import mock

import app


def valid_payload():
    return {
        "schemaVersion": 1,
        "provenance": {
            "turnDigest": "ab" * 32,
            "questionSchema": "qba.v1",
            "semanticaVersion": "0.6.5",
            "semanticaWheelSha256": app.SEMANTICA_WHEEL_SHA256,
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


class FakeGraph:
    def __init__(self):
        self.nodes = []
        self.edges = []

    def add_node(self, node_id, node_type, content=None, **properties):
        self.nodes.append((node_id, node_type, content, properties))
        return True

    def add_edge(self, source_id, target_id, edge_type, **properties):
        self.edges.append((source_id, target_id, edge_type, properties))
        return True


class ReceiverBoundaryTest(unittest.TestCase):
    def test_semantica_is_warmed_before_the_server_accepts_requests(self):
        calls = []

        class WarmGraph:
            def __init__(self):
                calls.append("constructed")

        semantica = types.ModuleType("semantica")
        context = types.ModuleType("semantica.context")
        context.ContextGraph = WarmGraph
        semantica.context = context
        with mock.patch.dict(
            sys.modules,
            {"semantica": semantica, "semantica.context": context},
        ):
            factory = app.load_graph_factory()
        self.assertIs(factory, WarmGraph)
        self.assertEqual(calls, ["constructed"])

    def test_health_path_avoids_cloud_run_reserved_healthz(self):
        self.assertEqual(app.HEALTH_PATH, "/health")

    def test_reconstructs_only_four_nodes_and_three_edges_without_content(self):
        graph, digest = app.reconstruct_graph(valid_payload(), FakeGraph)
        self.assertEqual(len(graph.nodes), 4)
        self.assertEqual(len(graph.edges), 3)
        self.assertTrue(all(node[2] is None for node in graph.nodes))
        self.assertEqual(graph.edges[-1][-1], {"relation": "direct"})
        self.assertEqual(len(digest), 64)

    def test_every_relation_is_deterministic(self):
        for relation in sorted(app.RELATIONS):
            value = valid_payload()
            value["edges"][-1]["relation"] = relation
            first = app.reconstruct_graph(value, FakeGraph)[1]
            second = app.reconstruct_graph(value, FakeGraph)[1]
            self.assertEqual(first, second)

    def test_unknown_or_free_form_fields_fail_closed(self):
        mutations = []
        extra = valid_payload()
        extra["transcript"] = "本人の回答本文"
        mutations.append(extra)
        content = valid_payload()
        content["nodes"][0]["content"] = "質問本文"
        mutations.append(content)
        relation = valid_payload()
        relation["edges"][-1]["relation"] = "direct:本人の回答"
        mutations.append(relation)
        digest = valid_payload()
        digest["provenance"]["turnDigest"] = "raw-request-id"
        mutations.append(digest)
        for value in mutations:
            with self.subTest(value=value):
                with self.assertRaises(app.RequestRejected):
                    app.validate_payload(value)

    def test_duplicate_keys_and_non_finite_numbers_are_rejected(self):
        with self.assertRaises(app.RequestRejected):
            app.decode_payload(b'{"schemaVersion":1,"schemaVersion":1}')
        with self.assertRaises(app.RequestRejected):
            app.decode_payload(b'{"schemaVersion":NaN}')

    def test_a_hundred_thousand_finite_graphs_have_stable_digests(self):
        expected = {}
        for index in range(100_000):
            value = valid_payload()
            relation = sorted(app.RELATIONS)[index % 4]
            value["edges"][-1]["relation"] = relation
            value["provenance"]["turnDigest"] = f"{index % 257:064x}"
            digest = app.reconstruct_graph(value, FakeGraph)[1]
            key = (relation, index % 257)
            if key in expected:
                self.assertEqual(digest, expected[key])
            else:
                expected[key] = digest

    def test_canonical_form_does_not_depend_on_json_key_order(self):
        value = valid_payload()
        reversed_root = dict(reversed(list(copy.deepcopy(value).items())))
        self.assertEqual(app.canonical_digest(value), app.canonical_digest(reversed_root))
        raw = json.dumps(value, ensure_ascii=False).encode()
        self.assertEqual(app.decode_payload(raw), value)


if __name__ == "__main__":
    unittest.main()
