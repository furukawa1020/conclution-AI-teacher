$ErrorActionPreference = "Stop"
$workspace = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$rulesTestState = @{content=[IO.File]::ReadAllText((Join-Path $workspace "firestore.rules")); variant="valid"; calls=0}
function Invoke-RestMethod {
    param($Method, $Uri, $Headers)
    if ($Method -cne "Get") { throw "Mutation attempted" }
    $rulesTestState.calls++
    if ($Uri -match "cloudresourcemanager") {
        return @{projectId="kotae-ai-u22-2026"; name="projects/551920539470"; state="ACTIVE"}
    }
    $name = "projects/kotae-ai-u22-2026/rulesets/test-rules"
    if ($Uri.EndsWith("releases/cloud.firestore")) {
        if ($rulesTestState.variant -eq "foreign") { $name = "projects/other/rulesets/test-rules" }
        return @{name="projects/kotae-ai-u22-2026/releases/cloud.firestore"; rulesetName=$name}
    }
    $content = $rulesTestState.content
    if ($rulesTestState.variant -eq "modified") { $content += "changed" }
    $files = @(@{name="firestore.rules"; content=$content})
    if ($rulesTestState.variant -eq "extra") { $files += @{name="extra.rules"; content=$content} }
    return @{name=$name; source=@{files=$files}}
}
foreach ($variant in @("valid", "foreign", "modified", "extra")) {
    $rulesTestState.variant = $variant
    $rulesTestState.calls = 0
    $failed = $false
    try {
        $result = & (Join-Path $workspace "scripts/deploy-firestore-rules.ps1") `
            -ProjectId kotae-ai-u22-2026 -CheckOnly `
            -GcloudPath "scripts/tests/fixtures/rules-check-auth.ps1"
    } catch { $failed = $true; if ($variant -eq "valid") { throw } }
    if (($variant -eq "valid") -eq $failed) { throw "Unexpected result for $variant" }
    if ($variant -eq "valid" -and $result -cne "FIRESTORE_DENY_ALL_MATCH=true") { throw "Missing verification result" }
    $expectedCalls = if ($variant -eq "foreign") { 2 } else { 3 }
    if ($rulesTestState.calls -ne $expectedCalls) { throw "Unexpected request count for $variant" }
    Write-Output "PASS $variant"
}
