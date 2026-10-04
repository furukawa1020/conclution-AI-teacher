$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$deployPath = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "../../../../../scripts/deploy-hosting.ps1"))
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($deployPath, [ref] $tokens, [ref] $parseErrors)
if ($parseErrors.Count -ne 0) { throw "fixture_parse_failed" }
# Evaluate only these real functions, never the cloud deployment entry point.
$names = @("Get-BrowserAudioFailureCodes", "Invoke-BrowserAudioGateProcess", "Assert-BrowserAudioGate")
foreach ($name in $names) {
    $functions = @($ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq $name
    }, $true))
    if ($functions.Count -ne 1) { throw "fixture_function_missing" }
    . ([scriptblock]::Create($functions[0].Extent.Text))
}

$publicRoot = "fixture path with spaces\"
$ExpectedGitCommit = "a" * 40
$manifest = "b" * 64
$calls = 0
$previousMode = $env:KOTAE_BROWSER_GATE_FIXTURE
$fixtureChild = Join-Path $PSScriptRoot "test-browser-audio.mjs"
# ScriptBlock.Create has no source filename/PSScriptRoot. Replace only this
# path boundary; the real command invocation, capture and validation execute.
function Join-Path {
    param([AllowEmptyString()] [string] $Path, [string] $ChildPath)
    if ($ChildPath -cne "test-browser-audio.mjs") { throw "fixture_unexpected_path" }
    return $fixtureChild
}
function Assert-GateFailure {
    param([string] $Mode, [string] $Expected)
    $env:KOTAE_BROWSER_GATE_FIXTURE = $Mode
    $message = $null
    try { Assert-BrowserAudioGate -ExpectedManifestSha256 $manifest } catch { $message = $_.Exception.Message }
    if ($message -cne $Expected) { throw "fixture_failure_message_mismatch_$Mode" }
    $script:calls++
}

try {
    $failure = "The real-browser AudioWorklet release gate failed: "
    Assert-GateFailure "known" ($failure + "browser_audio_devtools_timeout.")
    Assert-GateFailure "fixture" ($failure + "browser_audio_fixture_failed_confirmed_frames_timeout.")
    Assert-GateFailure "unterminated" ($failure + "browser_audio_chrome_exited_early.")
    foreach ($mode in @("unknown", "forgedFixture", "embedded", "empty", "longLine")) {
        Assert-GateFailure $mode ($failure + "browser_audio_gate_failed.")
    }
    Assert-GateFailure "debug" ($failure + "browser_audio_cdp_closed.")
    Assert-GateFailure "flood" ($failure + "browser_audio_fixture_timeout.")
    Assert-GateFailure "validJsonFailure" ($failure + "browser_audio_devtools_timeout.")
    Assert-GateFailure "oversized" "The real-browser AudioWorklet release gate returned oversized JSON."
    Assert-GateFailure "badJson" "The real-browser AudioWorklet release gate returned invalid JSON."
    Assert-GateFailure "noJson" "The real-browser AudioWorklet release gate returned no JSON."
    foreach ($mode in @("wrongCommit", "wrongManifest", "wrongProvenance")) {
        Assert-GateFailure $mode "The real-browser AudioWorklet release gate did not attest the reviewed release commit."
    }
    # Explicit rerun only: a prior failure must not contaminate the next result.
    foreach ($mode in @("success", "successNoise")) {
        $env:KOTAE_BROWSER_GATE_FIXTURE = $mode
        $output = @(Assert-BrowserAudioGate -ExpectedManifestSha256 $manifest)
        if ($output.Count -ne 0) { throw "fixture_success_output_leaked" }
        $calls++
    }
    Assert-GateFailure "empty" ($failure + "browser_audio_gate_failed.")
    $launchFailure = $null
    try {
        Invoke-BrowserAudioGateProcess -NodePath ($fixtureChild + ".missing.exe") -CommandArguments @("ignored")
    } catch {
        $launchFailure = $_.Exception.Message
    }
    if ($launchFailure -cne ($failure + "browser_audio_gate_failed.")) { throw "fixture_launch_failure_leaked" }
    $calls++
    Write-Output "BROWSER_GATE_DIAGNOSTIC_FIXTURES=PASS cases=$calls"
} finally {
    $env:KOTAE_BROWSER_GATE_FIXTURE = $previousMode
}
