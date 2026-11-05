// Bounded subprocesses shared by acquisition and local validation.
import { spawn } from "node:child_process";

export class Failure extends Error {
  // Retain a safe diagnostic and CLI status without attaching raw subprocess output.
  constructor(message, code = 2) {
    super(message);
    this.code = code;
  }
}

// cleanEnvironment separates explicit network acquisition from read-only checks;
// runtime credentials and arbitrary tool configuration are not inherited.
export function cleanEnvironment(source = process.env, acquisition = false) {
  const names = acquisition
    ? ["PATH", "HOME", "TMPDIR", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTOOLCHAIN",
       "GOPROXY", "GOSUMDB", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
       "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "npm_config_cache"]
    : ["PATH", "TMPDIR"];
  return {
    ...Object.fromEntries(names.filter((key) => source[key]).map((key) => [key, source[key]])),
    REDOCLY_TELEMETRY: "off",
    REDOCLY_SUPPRESS_UPDATE_NOTICE: "true",
    NO_COLOR: "1",
  };
}

// execute captures bounded combined output, kills the child process group on
// timeout/interruption, and resolves nonzero exits for tool-specific interpretation.
export async function execute(command, args, {
  cwd, env = cleanEnvironment(), timeout = 30_000, limit = 2 << 20,
} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd, env, detached: process.platform !== "win32", stdio: ["ignore", "pipe", "pipe"],
    });
    let size = 0;
    let failure;
    const stdout = [];
    const stderr = [];
    const stop = (message) => {
      failure ??= new Failure(message);
      if (child.pid) {
        try {
          if (process.platform === "win32") child.kill("SIGKILL");
          else process.kill(-child.pid, "SIGKILL");
        } catch (error) {
          if (error.code !== "ESRCH") failure = new Failure("could not stop validation process");
        }
      }
    };
    const interrupted = () => stop("validation interrupted");
    process.on("SIGINT", interrupted);
    process.on("SIGTERM", interrupted);
    const timer = setTimeout(() => stop("validation process exceeded its deadline"), timeout);
    const collect = (chunks) => (chunk) => {
      size += chunk.length;
      if (size > limit) stop("validation output exceeds its limit");
      else chunks.push(chunk);
    };
    child.stdout.on("data", collect(stdout));
    child.stderr.on("data", collect(stderr));
    child.on("error", () => {
      failure ??= new Failure("required validation executable is unavailable");
    });
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      process.off("SIGINT", interrupted);
      process.off("SIGTERM", interrupted);
      if (failure) reject(failure);
      else if (signal || code === null) reject(new Failure("validation process terminated"));
      else resolve({
        code, stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
      });
    });
  });
}
