function Get-GuestTrialReleaseRequirements {
    param([Parameter(Mandatory)][AllowEmptyCollection()][hashtable] $Environment)
    $flag = $Environment["KOTAE_GUEST_TRIAL_ENABLED"]
    if (-not $Environment.ContainsKey("KOTAE_GUEST_TRIAL_ENABLED") -or $flag -ceq "false") {
        return @{ Collections = @(); VerifyRules = $false }
    }
    if ($flag -cne "true" -or $Environment["KOTAE_GUEST_MODE_ENABLED"] -cne "true") {
        throw "Guest trial activation requires explicit true flags for trial and guest mode."
    }
    return @{
        Collections = @("guestTrialControls", "guestTrialControlRateLimits")
        VerifyRules = $true
    }
}
