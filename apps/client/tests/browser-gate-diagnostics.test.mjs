import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";

test("browser gate diagnostics come only from exact reviewed codes", async () => {
  const source = await readFile(new URL("../../../scripts/deploy-hosting.ps1", import.meta.url), "utf8");
  const capture = source.slice(source.indexOf("function Get-BrowserAudioFailureCodes"), source.indexOf("function Assert-PromotedBackendBoundary"));
  assert.ok(capture.includes("$allowedCodes.Contains($line)"));
  assert.ok(capture.includes("$process.StartInfo.CreateNoWindow = $true"));
  assert.ok(capture.includes("$process.StartInfo.UseShellExecute = $false"));
  assert.ok(capture.includes("$stdout.Length + $count -gt 65536"));
  assert.ok(capture.includes("$stderrLine.Length -eq 192"));
  assert.doesNotMatch(capture, /\.ReadToEnd\(|\.ReadLine\(|2>\$null/u);
  assert.equal((capture.match(/\$process\.Start\(\)/gu) ?? []).length, 1);
  assert.ok(capture.includes("$process.Dispose()"));
});

test("real PowerShell gate retains safe failures and validates subsequent releases", {
  skip: process.platform !== "win32",
}, () => {
  const output = execFileSync("powershell.exe", [
    "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
    fileURLToPath(new URL("./fixtures/browser-gate/assert-gate.ps1", import.meta.url)),
  ], { encoding: "utf8", windowsHide: true, timeout: 60_000, maxBuffer: 16_384 });
  assert.match(output, /^BROWSER_GATE_DIAGNOSTIC_FIXTURES=PASS cases=21\r?\n$/u);
  assert.doesNotMatch(output, /PRIVATE_TOKEN|SECRET_VALUE|C:\\private|Error:/u);
});
