import copy

"""Closed child report validation and the real nonzero-process handoff."""

def profile():
    return {"port": 12345, "warmupSeconds": 0, "seconds": 1, "workers": 1, "maxRequests": 10}

def report():
    empty = {"count": 0, "successfulResponseBytes": 0, "succeeded": 0, "failed": 0, "elapsedSeconds": 0,
             "latencySecondsTotal": 0, "latencyBuckets": [0] * 12, "latencyOverflow": 0,
             "outcomes": {key: 0 for key in ["timeout", "transport", "status", "body", "correctness", "canceled"]}, "httpFailureStatuses": {},
             "startedAtUnixMillis": 0, "issuanceEndedAtUnixMillis": 0, "finishedAtUnixMillis": 0, "complete": True, "clockAdjustmentMillis": 0}
    measured = copy.deepcopy(empty)
    measured.update(successfulResponseBytes=600, count=3, succeeded=3, elapsedSeconds=1, latencySecondsTotal=.03,
                    latencyBuckets=[0, 0] + [3] * 10, startedAtUnixMillis=1000,
                    issuanceEndedAtUnixMillis=2000, finishedAtUnixMillis=2000)
    return {"schemaVersion": 3, "model": "closed-loop-development-diagnostic", "profile": profile(),
            "baselineResults": 3, "responseDigest": "sha256:" + "a" * 64,
            "bucketUpperSeconds": [.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5],
            "warmup": empty, "measured": measured, "verdict": "passed",
            "generator": {"gomaxprocs": 2, "heapBytesAtFinish": 1, "allocatedBytes": 2, "gcCycles": 0, "goroutinesAtFinish": 1}}
