// Pure host-path, permission and environment policy; no native ACL or process calls.
import test from "node:test";
import assert from "node:assert/strict";

const { sameNativePath, privateMode, nativeEnvironmentAllowed } = await import(
  process.env.CATALOG_VSCODE_LIB
);

test("path equality follows the selected host without resolving filesystem aliases", () => {
  const composed = "/workspace/caf\u00e9/skill";
  const decomposed = "/workspace/cafe\u0301/skill";
  assert.equal(sameNativePath(composed, decomposed, "darwin"), true);
  assert.equal(sameNativePath(composed, decomposed, "linux"), false);
  assert.equal(
    sameNativePath("/workspace/Skill", "/workspace/skill", "darwin"),
    false,
  );
  assert.equal(
    sameNativePath("/workspace/Skill", "/workspace/skill", "linux"),
    false,
  );
  assert.equal(
    sameNativePath("C:/Catalog/cache/../Skill", "c:\\catalog\\skill", "win32"),
    true,
  );
  assert.equal(
    sameNativePath("C:/Catalog/Skill", "D:/Catalog/Skill", "win32"),
    false,
  );
  assert.equal(
    sameNativePath(
      "\\\\server\\share\\Skill",
      "\\\\server\\other\\Skill",
      "win32",
    ),
    false,
  );
});

test("POSIX private modes reject foreign ownership and distinguish files from directories", () => {
  const uid = process.getuid();
  for (const mode of [0o600, 0o700]) {
    assert.equal(
      privateMode({ mode, uid: uid + 1 }, false, mode === 0o700, "linux"),
      false,
    );
    assert.equal(
      privateMode({ mode, uid: uid + 1 }, true, false, "linux"),
      false,
    );
  }
  assert.equal(privateMode({ mode: 0o600, uid }, false, false, "linux"), true);
  assert.equal(privateMode({ mode: 0o700, uid }, false, true, "linux"), true);
  assert.equal(privateMode({ mode: 0o600, uid }, false, true, "linux"), false);
  for (const mode of [0o400, 0o640, 0o644, 0o660, 0o700, 0o777])
    assert.equal(privateMode({ mode, uid }, false, false, "linux"), false);
  // Public readability does not grant another writer to an owned directory.
  for (const mode of [0o700, 0o750, 0o755])
    assert.equal(privateMode({ mode, uid }, true, false, "linux"), true);
  for (const mode of [0o720, 0o702, 0o770, 0o757, 0o1777])
    assert.equal(privateMode({ mode, uid }, true, false, "linux"), false);
});

test("process environment admission is closed and applies host-specific key casing", () => {
  for (const key of [
    "PATH",
    "HOME",
    "TMPDIR",
    "LC_ALL",
    "LC_MESSAGES",
    "NPM_CONFIG_CACHE",
    "NPM_CONFIG_OFFLINE",
  ])
    assert.equal(nativeEnvironmentAllowed(key, "linux"), true);
  for (const key of ["Path", "home", "lc_all", "Npm_Config_Cache"])
    assert.equal(nativeEnvironmentAllowed(key, "linux"), false);
  for (const key of [
    "Path",
    "SystemRoot",
    "ComSpec",
    "Pathext",
    "lc_all",
    "Npm_Config_Cache",
  ])
    assert.equal(nativeEnvironmentAllowed(key, "win32"), true);
  for (const platform of ["linux", "darwin", "win32"])
    for (const key of [
      "",
      "PATH_EXTRA",
      "PREFIX_PATH",
      "LC_",
      "LC_1",
      "NODE_OPTIONS",
      "NODE_EXTRA_CA_CERTS",
      "LD_PRELOAD",
      "GIT_SSH_COMMAND",
      "SSH_AUTH_SOCK",
      "DOCKER_CONFIG",
      "HTTP_PROXY",
      "CATALOG_TOKEN",
    ])
      assert.equal(
        nativeEnvironmentAllowed(key, platform),
        false,
        `${platform}: ${key}`,
      );
});
