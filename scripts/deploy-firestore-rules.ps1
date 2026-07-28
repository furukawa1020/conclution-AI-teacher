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

$expectedProjectId = "kotae-ai-u22-2026"
$expectedProjectNumber = "551920539470"
$expectedRulesSha256 = "cd5089e4e5116dbb994013dc5fd5e7e411ec348935b8d06d13acd00173cca15b"

$workspace = Split-Path -Parent $PSScriptRoot
$rulesFile = [System.IO.Path]::GetFullPath((Join-Path $workspace $RulesPath))
$expectedRulesFile = [System.IO.Path]::GetFullPath((Join-Path $workspace "firestore.rules"))
$gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))

if ($ProjectId -cne $expectedProjectId) {
    throw "Refusing to deploy project '$ProjectId'; expected '$expectedProjectId'."
}
if (-not [string]::Equals(
        $rulesFile,
        $expectedRulesFile,
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
    throw "Refusing to deploy a rules file other than workspace/firestore.rules."
}
if (-not (Test-Path -LiteralPath $rulesFile -PathType Leaf)) {
    throw "Firestore rules file does not exist: $rulesFile"
}
if (
    ((Get-Item -LiteralPath $rulesFile -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Firestore rules file must not be a reparse point."
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "Google Cloud CLI does not exist: $gcloud"
}

$content = [System.IO.File]::ReadAllText($rulesFile, [System.Text.Encoding]::UTF8)
$sha256 = [System.Security.Cryptography.SHA256]::Create()
try {
    $digest = $sha256.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($content))
    $fingerprint = [Convert]::ToBase64String($digest)
    $digestHex = ([System.BitConverter]::ToString($digest)).Replace("-", "").ToLowerInvariant()
} finally {
    $sha256.Dispose()
}
if ($digestHex -cne $expectedRulesSha256) {
    throw "Refusing to deploy modified rules; this script is locked to the reviewed deny-all ruleset."
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

$cloudProject = Invoke-RestMethod `
    -Method Get `
    -Uri "https://cloudresourcemanager.googleapis.com/v3/projects/$ProjectId" `
    -Headers $headers
if (
    $cloudProject.projectId -cne $expectedProjectId -or
    $cloudProject.name -cne "projects/$expectedProjectNumber" -or
    $cloudProject.state -cne "ACTIVE"
) {
    throw "Google Cloud project identity check failed."
}

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
    $updateBody = @{
        release = @{
            name = $releaseName
            rulesetName = $ruleset.name
        }
        updateMask = "rulesetName"
    } | ConvertTo-Json -Depth 5 -Compress
    $release = Invoke-RestMethod `
        -Method Patch `
        -Uri "$apiRoot/$releaseName" `
        -Headers $headers `
        -ContentType "application/json" `
        -Body $updateBody
} else {
    $release = Invoke-RestMethod `
        -Method Post `
        -Uri "$apiRoot/projects/$ProjectId/releases" `
        -Headers $headers `
        -ContentType "application/json" `
        -Body $releaseBody
}

$verifiedRelease = Invoke-RestMethod `
    -Method Get `
    -Uri "$apiRoot/$releaseName" `
    -Headers $headers
if (
    $verifiedRelease.name -cne $releaseName -or
    $verifiedRelease.rulesetName -cne $ruleset.name
) {
    throw "Firestore rules release read-back verification failed."
}

Write-Output "FIRESTORE_RULESET=$($ruleset.name)"
Write-Output "FIRESTORE_RELEASE=$($verifiedRelease.name)"
