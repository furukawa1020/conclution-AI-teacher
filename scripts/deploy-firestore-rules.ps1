[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")]
    [string] $ProjectId,

    [string] $RulesPath = "firestore.rules",

    [string] $GcloudPath = ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
$rulesFile = [System.IO.Path]::GetFullPath((Join-Path $workspace $RulesPath))
$gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))

if (-not (Test-Path -LiteralPath $rulesFile -PathType Leaf)) {
    throw "Firestore rules file does not exist: $rulesFile"
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "Google Cloud CLI does not exist: $gcloud"
}

$content = [System.IO.File]::ReadAllText($rulesFile, [System.Text.Encoding]::UTF8)
$sha256 = [System.Security.Cryptography.SHA256]::Create()
try {
    $fingerprint = [Convert]::ToBase64String(
        $sha256.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($content))
    )
} finally {
    $sha256.Dispose()
}

$token = ((& $gcloud auth print-access-token) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($token)) {
    throw "Could not obtain a Google Cloud access token."
}
$headers = @{
    Authorization         = "Bearer $token"
    "x-goog-user-project" = $ProjectId
}
$apiRoot = "https://firebaserules.googleapis.com/v1"

$rulesetBody = @{
    source = @{
        files = @(
            @{
                name = "firestore.rules"
                content = $content
                fingerprint = $fingerprint
            }
        )
    }
} | ConvertTo-Json -Depth 10 -Compress

$ruleset = Invoke-RestMethod `
    -Method Post `
    -Uri "$apiRoot/projects/$ProjectId/rulesets" `
    -Headers $headers `
    -ContentType "application/json" `
    -Body $rulesetBody

if ([string]::IsNullOrWhiteSpace($ruleset.name)) {
    throw "Firebase Rules API did not return a ruleset name."
}

$releaseName = "projects/$ProjectId/releases/cloud.firestore"
$releaseBody = @{
    name = $releaseName
    rulesetName = $ruleset.name
} | ConvertTo-Json -Compress

$releaseExists = $true
try {
    $null = Invoke-RestMethod `
        -Method Get `
        -Uri "$apiRoot/$releaseName" `
        -Headers $headers
} catch {
    if ($null -ne $_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 404) {
        $releaseExists = $false
    } else {
        throw
    }
}

if ($releaseExists) {
    $encodedMask = [Uri]::EscapeDataString("rulesetName")
    $release = Invoke-RestMethod `
        -Method Patch `
        -Uri "$apiRoot/$releaseName`?updateMask=$encodedMask" `
        -Headers $headers `
        -ContentType "application/json" `
        -Body $releaseBody
} else {
    $release = Invoke-RestMethod `
        -Method Post `
        -Uri "$apiRoot/projects/$ProjectId/releases" `
        -Headers $headers `
        -ContentType "application/json" `
        -Body $releaseBody
}

Write-Output "FIRESTORE_RULESET=$($ruleset.name)"
Write-Output "FIRESTORE_RELEASE=$($release.name)"
