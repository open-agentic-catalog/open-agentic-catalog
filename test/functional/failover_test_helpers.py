
"""Service-free checks for writer-recovery evidence and fault orchestration."""

def evidence(profile="failover-abrupt"):
    return {"scenario": profile, "replication": "asynchronous-physical", "barrier": "0/FF",
            "replayedBarrier": "1/0", "postgresVersions": [180003, 180003],
            "processDigests": {name: "sha256:" + "a" * 64 for name in ("api", "worker")},
            "writerChanged": True, "completedReplay": True, "pendingRepair": profile == "failover-abrupt",
            "jobRecovered": profile == "failover-abrupt", "retainedPublicState": True, "policyRetained": True, "baselineLost": 0,
            "recoverySeconds": 12.5}
