"""Explicit Rapid shrinking, replay and execution qualification in a disposable module."""

import argparse
import json
import os
from pathlib import Path
import signal
import tempfile

import property as p


SOURCE = '''package probe
import ("testing"; "pgregory.net/rapid")
func TestPropertyProbe(t *testing.T) { rapid.Check(t, func(t *rapid.T) {
n := rapid.IntRange(0,100).Draw(t,"n")
if n >= 10 { t.Fatalf("expected n < 10, got %d", n) }
}) }
'''


def test_output(raw):
    return "".join(json.loads(line).get("Output", "") for line in raw.splitlines())


def qualify(go, toolchain):
    env = p.environment(os.environ, p.settings("100", "1"))
    env["GOTOOLCHAIN"] = toolchain
    old = Path.cwd()
    with tempfile.TemporaryDirectory(prefix="property-qualification-") as directory:
        root = Path(directory)
        try:
            os.chdir(root)
            (root / "go.mod").write_text("module example.com/probe\n\ngo 1.27.0\n\nrequire pgregory.net/rapid v1.3.0\n")
            p.run([go, "mod", "download", "pgregory.net/rapid@v1.3.0"], env=env)
            source = root / "probe_test.go"
            source.write_text(SOURCE)
            command = [go, "test", "-json", "-count=1", "-timeout=10s", "-run=^TestProperty", "."]
            initial = p.run(command, env=dict(env, RAPID_NOFAILFILE="0"), check=False, binary=True)
            p.require(initial.code == 1 and "expected n < 10, got 10" in test_output(initial.stdout), "shrinking did not reach expected counterexample")
            artifacts = list((root / "testdata/rapid/TestPropertyProbe").glob("*.fail"))
            p.require(len(artifacts) == 1, "missing minimized replay artifact")
            replay_file = root / "replay.fail"
            artifacts[0].rename(replay_file)  # Explicit replay must not accidentally use the corpus.
            # Zero random checks is deliberate only in this replay control: a
            # random failure must not stand in for execution of the artifact.
            replay_env = dict(env, RAPID_CHECKS="0", RAPID_FAILFILE=str(replay_file))
            for name, inputs in (("seed", env), ("file", replay_env)):
                result = p.run(command, env=inputs, check=False, binary=True)
                p.require(result.code == 1 and "expected n < 10, got 10" in test_output(result.stdout), "failed " + name + " reproduction")
                p.validate_events(result.stdout, "example.com/probe", ["TestPropertyProbe"], int(inputs["RAPID_CHECKS"]), failed_run=True)
                print("Qualified Rapid " + name + " replay and shrinking", flush=True)
            # Fix the deliberate assertion; the same replay must now pass.
            source.write_text(SOURCE.replace("n >= 10", "n > 100"))
            result = p.run(command, env=replay_env, binary=True)
            p.validate_events(result.stdout, "example.com/probe", ["TestPropertyProbe"], 0)
            print("Qualified regression after correction", flush=True)
            broken = root / "bad.fail"
            broken.write_text("malformed replay")
            for name, inputs, arguments in (
                ("missing replay", dict(env, RAPID_FAILFILE=str(root / "missing.fail")), []),
                ("malformed replay", dict(env, RAPID_FAILFILE=str(broken)), []),
                ("short mode", env, ["-short"]),
                ("zero checks", dict(env, RAPID_CHECKS="0"), []),
            ):
                result = p.run([*command[:-1], *arguments, "."], env=inputs, check=False, binary=True)
                p.require(result.code == 0, "upstream control did not exhibit expected success: " + name)
                try:
                    p.validate_events(result.stdout, "example.com/probe", ["TestPropertyProbe"], 100)
                except ValueError:
                    print("Rejected Rapid " + name + " false success", flush=True)
                else:
                    raise ValueError("invalid run accepted: " + name)
            source.write_text(SOURCE.replace('"testing";', '"testing"; "time";').replace(
                'n := rapid.IntRange', 'time.Sleep(30*time.Millisecond)\nn := rapid.IntRange').replace("n >= 10", "n > 100"))
            reduced = [arg.replace("-timeout=10s", "-timeout=120ms") for arg in command]
            result = p.run(reduced, env=env, check=False, binary=True)
            p.require(result.code == 0, "deadline fixture did not exercise upstream early success")
            try:
                p.validate_events(result.stdout, "example.com/probe", ["TestPropertyProbe"], 100)
            except ValueError:
                print("Rejected Rapid deadline-reduced success", flush=True)
            else:
                raise ValueError("partial run accepted")
        finally:
            os.chdir(old)
    print("Rapid qualification passed; owned fixtures removed.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--toolchain", required=True)
    args = parser.parse_args()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, p.interrupt)
    qualify(args.go, args.toolchain)
