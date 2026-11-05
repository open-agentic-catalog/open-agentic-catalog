import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { posix, win32 } from "node:path";
import type { Stats } from "node:fs";
import { ClientError } from "./config.js";

export function supportedPlatform(platform = process.platform): boolean {
  return ["linux", "darwin", "win32"].includes(platform);
}
export function nativeAbsolute(
  value: string,
  platform = process.platform,
): boolean {
  // Windows drive-relative and root-relative paths depend on ambient state.
  return platform === "win32"
    ? /^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))/u.test(
        value,
      ) &&
        win32.isAbsolute(value) &&
        !/^[\\/]{2}[?.][\\/]/u.test(value)
    : posix.isAbsolute(value);
}
export function sameNativePath(
  a: string,
  b: string,
  platform = process.platform,
): boolean {
  return platform === "win32"
    ? win32.normalize(a).toLowerCase() === win32.normalize(b).toLowerCase()
    : platform === "darwin"
      ? a.normalize("NFC") === b.normalize("NFC")
      : a === b;
}
export function privateMode(
  info: Pick<Stats, "mode" | "uid">,
  directory: boolean,
  executable = false,
  platform = process.platform,
): boolean {
  if (platform === "win32") return true; // DACL admission below owns Windows access.
  return (
    (process.getuid === undefined || info.uid === process.getuid()) &&
    (directory
      ? (info.mode & 0o022) === 0
      : (info.mode & 0o777) === (executable ? 0o700 : 0o600))
  );
}
/** Fixed PowerShell program; untrusted paths travel only through a private env input. */
export async function windowsDirectory(
  path: string,
  created: boolean,
): Promise<void> {
  if (process.platform !== "win32") return;
  const root = process.env.SystemRoot ?? process.env.SYSTEMROOT;
  if (!root || !nativeAbsolute(root, "win32"))
    throw new ClientError("windows_acl_unavailable");
  const script = `
$ErrorActionPreference = 'Stop'
$p = $env:OAC_PACKAGE_ACL_PATH
$self = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$item = Get-Item -LiteralPath $p -Force
if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Reparse point' }
$acl = Get-Acl -LiteralPath $p
if ($acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value -ne $self.Value) { throw 'Owner' }
if ($env:OAC_PACKAGE_ACL_CREATE -eq '1') {
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRuleSpecific($rule) }
  foreach ($sid in @($self.Value, 'S-1-5-18', 'S-1-5-32-544')) {
    $identity = New-Object System.Security.Principal.SecurityIdentifier($sid)
    $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
  }
  Set-Acl -LiteralPath $p -AclObject $acl
  $acl = Get-Acl -LiteralPath $p
}
foreach ($rule in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
  if ($rule.AccessControlType -eq 'Allow' -and $rule.IdentityReference.Value -notin @($self.Value, 'S-1-5-18', 'S-1-5-32-544') -and ($env:OAC_PACKAGE_ACL_PRIVATE -eq '1' -or ($rule.FileSystemRights -band ([System.Security.AccessControl.FileSystemRights]::Write -bor [System.Security.AccessControl.FileSystemRights]::Delete -bor [System.Security.AccessControl.FileSystemRights]::ChangePermissions -bor [System.Security.AccessControl.FileSystemRights]::TakeOwnership -bor [System.Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles)) -ne 0)) { throw 'Other writer' }
}
`;
  try {
    await promisify(execFile)(
      win32.join(
        root,
        "System32",
        "WindowsPowerShell",
        "v1.0",
        "powershell.exe",
      ),
      [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-EncodedCommand",
        Buffer.from(script, "utf16le").toString("base64"),
      ],
      {
        timeout: 10000,
        maxBuffer: 16384,
        windowsHide: true,
        env: {
          SystemRoot: root,
          WINDIR: root,
          OAC_PACKAGE_ACL_PATH: path,
          OAC_PACKAGE_ACL_CREATE: created ? "1" : "0",
          OAC_PACKAGE_ACL_PRIVATE: path.split(/[\\/]/u).includes(".oac-catalog")
            ? "1"
            : "0",
        },
      },
    );
  } catch {
    throw new ClientError("unsafe_windows_installation_acl");
  }
}
export function packageProcessControl(): string | undefined {
  if (process.platform !== "win32") return;
  const root = process.env.SystemRoot ?? process.env.SYSTEMROOT;
  if (!root || !nativeAbsolute(root, "win32"))
    throw new ClientError("windows_process_control_unavailable");
  return win32.join(root, "System32", "taskkill.exe");
}
export async function stopPackageProcess(pid: number): Promise<void> {
  if (process.platform !== "win32") {
    try {
      process.kill(-pid, "SIGKILL");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
    }
    return;
  }
  // Only the invocation-owned process tree, never a process name or global job.
  await promisify(execFile)(
    packageProcessControl()!,
    ["/PID", String(pid), "/T", "/F"],
    { windowsHide: true, timeout: 10000, maxBuffer: 16384 },
  );
}

/** Bind conventional process inputs to the execution host, including Windows casing. */
export function nativeEnvironmentAllowed(
  key: string,
  platform = process.platform,
): boolean {
  return /^(PATH|PATHEXT|COMSPEC|HOME|USERPROFILE|SYSTEMROOT|WINDIR|APPDATA|LOCALAPPDATA|TEMP|TMP|TMPDIR|LANG|LC_[A-Z_]+|NPM_CONFIG_CACHE|NPM_CONFIG_OFFLINE)$/u.test(
    platform === "win32" ? key.toUpperCase() : key,
  );
}
