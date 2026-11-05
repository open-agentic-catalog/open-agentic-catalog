"""Synthetic closed evidence shared by the supervisor tests."""


def evidence():
    return {"programDigest": "sha256:" + "a" * 64, "completedReceipts": 2,
            "activationReport": True, "unpublishReport": True, "readerRestricted": True,
            "databaseIsolated": True, "fileMatchesDatabase": True}
