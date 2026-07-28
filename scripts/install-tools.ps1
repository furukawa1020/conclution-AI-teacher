[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
$toolRoot = Join-Path $workspace ".tools"
$gcloudVersion = "577.0.0"
$gcloudArchiveName = "google-cloud-sdk-$gcloudVersion-windows-x86_64-bundled-python.zip"
$gcloudSha256 = "dcf9097b2c7a0a29bd6322571f5090c6046bed96b19c0750e62f549b735b80eb"
$gcloudUri = "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/$gcloudArchiveName"
$gcloudInstallRoot = Join-Path $toolRoot "gcloud-$gcloudVersion"
$firebaseVersion = "15.17.0"
$firebaseAssetName = "firebase-tools-instant-win.exe"
$firebaseInstallRoot = Join-Path $toolRoot "firebase-$firebaseVersion"

New-Item -ItemType Directory -Force -Path $toolRoot | Out-Null

function Assert-Sha256 {
    param(
        [Parameter(Mandatory)]
        [string] $Path,
        [Parameter(Mandatory)]
        [string] $Expected
    )

    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $Expected.ToLowerInvariant()) {
        throw "SHA-256 mismatch for $Path. Expected $Expected, got $actual."
    }
}

function Invoke-VerifiedDownload {
    param(
        [Parameter(Mandatory)]
        [string] $Uri,
        [Parameter(Mandatory)]
        [string] $Destination,
        [Parameter(Mandatory)]
        [string] $ExpectedSha256
    )

    if (Test-Path -LiteralPath $Destination) {
        Remove-Item -LiteralPath $Destination -Force
    }
    & curl.exe --fail --location --silent --show-error --retry 3 --output $Destination $Uri
    if ($LASTEXITCODE -ne 0) {
        throw "Download failed: $Uri"
    }
    Assert-Sha256 -Path $Destination -Expected $ExpectedSha256
}

if (-not (Test-Path -LiteralPath (Join-Path $gcloudInstallRoot "google-cloud-sdk\bin\gcloud.cmd"))) {
    $archivePath = Join-Path $toolRoot "$gcloudArchiveName.partial.zip"
    $legacyArchivePath = Join-Path $toolRoot "$gcloudArchiveName.download"
    if ((Test-Path -LiteralPath $legacyArchivePath) -and -not (Test-Path -LiteralPath $archivePath)) {
        Move-Item -LiteralPath $legacyArchivePath -Destination $archivePath
    }
    if (Test-Path -LiteralPath $archivePath) {
        Assert-Sha256 -Path $archivePath -Expected $gcloudSha256
    } else {
        Invoke-VerifiedDownload -Uri $gcloudUri -Destination $archivePath -ExpectedSha256 $gcloudSha256
    }
    New-Item -ItemType Directory -Force -Path $gcloudInstallRoot | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $gcloudInstallRoot -Force
    Remove-Item -LiteralPath $archivePath -Force
}

if (-not (Test-Path -LiteralPath (Join-Path $firebaseInstallRoot "firebase.exe"))) {
    $releaseUri = "https://api.github.com/repos/firebase/firebase-tools/releases/tags/v$firebaseVersion"
    $headers = @{
        Accept = "application/vnd.github+json"
        "User-Agent" = "kotae-ai-tool-bootstrap"
        "X-GitHub-Api-Version" = "2022-11-28"
    }
    $release = Invoke-RestMethod -Uri $releaseUri -Headers $headers
    $asset = $release.assets | Where-Object { $_.name -eq $firebaseAssetName } | Select-Object -First 1
    if ($null -eq $asset) {
        throw "Firebase CLI release asset $firebaseAssetName was not found."
    }
    if ([string]::IsNullOrWhiteSpace($asset.digest) -or -not $asset.digest.StartsWith("sha256:")) {
        throw "Firebase CLI release does not publish a SHA-256 digest; refusing an unverified binary."
    }

    New-Item -ItemType Directory -Force -Path $firebaseInstallRoot | Out-Null
    $firebasePath = Join-Path $firebaseInstallRoot "firebase.exe.download"
    Invoke-VerifiedDownload `
        -Uri $asset.browser_download_url `
        -Destination $firebasePath `
        -ExpectedSha256 $asset.digest.Substring(7)
    Move-Item -LiteralPath $firebasePath -Destination (Join-Path $firebaseInstallRoot "firebase.exe")
}

$gcloud = Join-Path $gcloudInstallRoot "google-cloud-sdk\bin\gcloud.cmd"
$firebase = Join-Path $firebaseInstallRoot "firebase.exe"

& $gcloud version
if ($LASTEXITCODE -ne 0) {
    throw "gcloud version check failed."
}

& $firebase --version
if ($LASTEXITCODE -ne 0) {
    throw "Firebase CLI version check failed."
}

Write-Output "GCLOUD=$gcloud"
Write-Output "FIREBASE=$firebase"
