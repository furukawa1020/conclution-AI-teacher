[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")]
    [string] $ProjectId,

    [ValidateRange(1, 60)]
    [int] $PollIntervalSeconds = 15,

    [ValidateRange(60, 3600)]
    [int] $WaitTimeoutSeconds = 1800,

    [string] $GcloudPath = ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$expectedProjectId = "kotae-ai-u22-2026"
$databaseId = "(default)"
$requiredCollectionGroups = @(
    "evaluations",
    "evaluationRateLimits",
    "voiceRateLimits",
    "guestVoiceRateLimits",
    "voiceLiveLeases",
    "passkey_ceremonies_v1",
    "passkeyClientRateLimits",
    "passkeyAppRateLimits"
)

$workspace = Split-Path -Parent $PSScriptRoot
if ([System.IO.Path]::IsPathRooted($GcloudPath)) {
    $gcloud = [System.IO.Path]::GetFullPath($GcloudPath)
} else {
    $gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))
}

if ($ProjectId -cne $expectedProjectId) {
    throw "Refusing to configure project '$ProjectId'; expected '$expectedProjectId'."
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "Google Cloud CLI does not exist: $gcloud"
}

function Invoke-GcloudQuiet {
    param(
        [Parameter(Mandatory)]
        [string[]] $CommandArguments,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    # Windows PowerShell converts any native stderr output into an ErrorRecord.
    # gcloud writes harmless progress messages (for example, "Request issued")
    # to stderr even when it exits successfully, so Stop would turn a successful
    # command into a terminating PowerShell error before we can inspect its exit
    # code. Suppress stderr under Continue, then fail only on the native exit code.
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $null = & $gcloud @CommandArguments 2>$null
        $commandExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($commandExitCode -ne 0) {
        throw "Google Cloud CLI failed while $Operation."
    }
}

function Get-TtlPolicies {
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $jsonLines = @(
            & $gcloud firestore fields ttls list `
                --database=$databaseId `
                --project=$ProjectId `
                --format=json `
                --quiet `
                --verbosity=error 2>$null
        )
        $commandExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($commandExitCode -ne 0) {
        throw "Google Cloud CLI failed while checking Firestore TTL policies."
    }

    $jsonText = ($jsonLines -join [System.Environment]::NewLine).Trim()
    if ([string]::IsNullOrWhiteSpace($jsonText)) {
        throw "Firestore TTL policy check returned no JSON."
    }

    try {
        $decoded = ConvertFrom-Json -InputObject $jsonText -ErrorAction Stop
    } catch {
        throw "Firestore TTL policy check returned invalid JSON."
    }

    return @($decoded)
}

foreach ($collectionGroup in $requiredCollectionGroups) {
    Invoke-GcloudQuiet `
        -Operation "enabling the required Firestore TTL policy" `
        -CommandArguments @(
            "firestore", "fields", "ttls", "update", "expiresAt",
            "--collection-group=$collectionGroup",
            "--database=$databaseId",
            "--enable-ttl",
            "--project=$ProjectId",
            "--quiet",
            "--verbosity=error"
        )
}

$waitTimer = [System.Diagnostics.Stopwatch]::StartNew()
while ($true) {
    $policies = @(Get-TtlPolicies)
    $states = @{}

    foreach ($policy in $policies) {
        if ($null -eq $policy) {
            continue
        }

        $nameProperty = $policy.PSObject.Properties["name"]
        if ($null -eq $nameProperty -or [string]::IsNullOrWhiteSpace([string]$nameProperty.Value)) {
            throw "Firestore TTL policy JSON is missing a resource name."
        }

        $resourceName = [string]$nameProperty.Value
        foreach ($collectionGroup in $requiredCollectionGroups) {
            $requiredSuffix = "/collectionGroups/$collectionGroup/fields/expiresAt"
            if (-not $resourceName.EndsWith($requiredSuffix, [System.StringComparison]::Ordinal)) {
                continue
            }
            if ($states.ContainsKey($collectionGroup)) {
                throw "Firestore TTL policy JSON contains a duplicate required policy."
            }

            $ttlProperty = $policy.PSObject.Properties["ttlConfig"]
            if ($null -eq $ttlProperty -or $null -eq $ttlProperty.Value) {
                throw "Firestore TTL policy JSON is missing ttlConfig."
            }
            $stateProperty = $ttlProperty.Value.PSObject.Properties["state"]
            if ($null -eq $stateProperty -or [string]::IsNullOrWhiteSpace([string]$stateProperty.Value)) {
                throw "Firestore TTL policy JSON is missing ttlConfig.state."
            }
            $states[$collectionGroup] = [string]$stateProperty.Value
        }
    }

    $missingGroups = @(
        $requiredCollectionGroups | Where-Object { -not $states.ContainsKey($_) }
    )

    $activeGroups = @(
        $requiredCollectionGroups | Where-Object { $states[$_] -ceq "ACTIVE" }
    )
    if ($activeGroups.Count -eq $requiredCollectionGroups.Count) {
        $waitTimer.Stop()
        Write-Host "All required Firestore TTL policies are ACTIVE."
        break
    }

    $repairGroups = @(
        $requiredCollectionGroups | Where-Object { $states[$_] -ceq "NEEDS_REPAIR" }
    )
    if ($repairGroups.Count -ne 0) {
        throw "A required Firestore TTL policy needs repair."
    }

    if ($waitTimer.Elapsed.TotalSeconds -ge $WaitTimeoutSeconds) {
        if ($missingGroups.Count -ne 0) {
            throw "Timed out while one or more required Firestore TTL policies were still missing."
        }
        throw "Timed out waiting for required Firestore TTL policies to become ACTIVE."
    }

    $remainingSeconds = $WaitTimeoutSeconds - $waitTimer.Elapsed.TotalSeconds
    $sleepSeconds = [Math]::Min($PollIntervalSeconds, [Math]::Ceiling($remainingSeconds))
    Start-Sleep -Seconds ([int]$sleepSeconds)
}
