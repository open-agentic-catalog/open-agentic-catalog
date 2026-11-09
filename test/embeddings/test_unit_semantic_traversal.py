"""Independent seed identities and scripted pages qualify the traversal oracle."""

import unittest

import semantic


class ScaleTraversalTests(unittest.TestCase):
    def pages(self, failure=""):
        calls = []

        def fetch(query):
            number = len(calls)
            self.assertEqual(query, {"text": "Scale", "tags": ["scale"], "limit": 20,
                                    **({"cursor": f"cursor-{number}"} if number else {})})
            calls.append(query)
            items = []
            # Reverse order demonstrates that the oracle predicts membership,
            # not model-dependent scoring or tie ordering.
            for offset in range(number * 20, min((number + 1) * 20, 997)):
                index = 996 - offset
                identifier = f"urn:air:scale.example:scale:local-{index:06d}"
                items.append({"identifier": identifier, "id": f"local:release-{index}", "type": "local",
                              "local": {"release": {"id": f"release-{index}"}}, "tags": ["scale"],
                              "kind": ["agent", "mcp_server", "skill", "plugin"][index % 4]})
            page = {"items": items, "tier": "hybrid", "profile": "sha256:" + "a" * 64,
                    "generation": "sha256:" + "c" * 64, "durableTotal": 997, "liveCount": 0}
            if number < 49:
                page["nextCursor"] = f"cursor-{number + 1}"
            if number == 1:
                if failure == "missing":
                    page["items"].pop()
                elif failure == "repeated-identity":
                    page["items"][0].update(identifier="urn:air:scale.example:scale:local-000996")
                elif failure == "unknown":
                    page["items"][0]["identifier"] = "urn:air:scale.example:scale:local-000997"
                elif failure == "result-id":
                    page["items"][0]["id"] = "local:release-996"
                    page["items"][0]["local"]["release"]["id"] = "release-996"
                elif failure == "early-terminal":
                    page.pop("nextCursor")
                elif failure == "cursor":
                    page["nextCursor"] = "cursor-1"
                elif failure == "profile":
                    page["profile"] = "sha256:" + "b" * 64
                elif failure == "generation":
                    page["generation"] = "sha256:" + "d" * 64
                elif failure == "missing-generation":
                    page.pop("generation")
                elif failure == "filter":
                    page["items"][0]["tags"] = []
                elif failure == "kind":
                    page["items"][0]["kind"] = "unknown"
                elif failure == "tier":
                    page["tier"] = "core"
            if failure == "no-terminal" and number == 49:
                page["nextCursor"] = "cursor-50"
            return page
        return fetch, calls

    def test_complete_independent_population_and_context_on_every_page(self):
        fetch, calls = self.pages()
        result = semantic.scale_traversal(fetch)
        self.assertEqual(result, {"pages": 50, "items": 997,
                                 "kindCounts": {"agent": 250, "mcp_server": 249, "skill": 249, "plugin": 249},
                                 "profile": "sha256:" + "a" * 64, "generation": "sha256:" + "c" * 64})
        self.assertEqual(len(calls), 50)

    def test_incomplete_changed_or_nonadvancing_chains_are_rejected(self):
        for failure in ("missing", "repeated-identity", "unknown", "result-id", "early-terminal", "cursor",
                        "profile", "generation", "missing-generation", "filter", "kind", "tier", "no-terminal"):
            fetch, calls = self.pages(failure)
            with self.subTest(failure=failure), self.assertRaises(RuntimeError):
                semantic.scale_traversal(fetch)
            self.assertLessEqual(len(calls), 50)


if __name__ == "__main__":
    unittest.main()
