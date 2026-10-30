$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
. (Join-Path (Split-Path -Parent $PSScriptRoot) "guest-trial-release-policy.ps1")
foreach ($environment in @(@{}, @{KOTAE_GUEST_TRIAL_ENABLED="false"})) {
    $result = Get-GuestTrialReleaseRequirements -Environment $environment
    if ($result.VerifyRules -or $result.Collections.Count -ne 0) { throw "Disabled trial requires no additional rollout" }
}
$result = Get-GuestTrialReleaseRequirements -Environment @{KOTAE_GUEST_TRIAL_ENABLED="true"; KOTAE_GUEST_MODE_ENABLED="true"}
if (-not $result.VerifyRules -or ($result.Collections -join ',') -cne "guestTrialControls,guestTrialControlRateLimits") {
    throw "Enabled trial must verify both TTLs and deployed rules"
}
foreach ($environment in @(
    @{KOTAE_GUEST_TRIAL_ENABLED="TRUE"; KOTAE_GUEST_MODE_ENABLED="true"},
    @{KOTAE_GUEST_TRIAL_ENABLED=""},
    @{KOTAE_GUEST_TRIAL_ENABLED="true"},
    @{KOTAE_GUEST_TRIAL_ENABLED="true"; KOTAE_GUEST_MODE_ENABLED="false"}
)) {
    $failed = $false
    try { Get-GuestTrialReleaseRequirements -Environment $environment | Out-Null } catch { $failed = $true }
    if (-not $failed) { throw "Invalid activation accepted" }
}
Write-Output "PASS guest trial release policy: 7 cases"
