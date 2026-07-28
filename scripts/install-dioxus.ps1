[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$workspace = Split-Path -Parent $PSScriptRoot
$version = "0.7.9"
$toolRoot = Join-Path $workspace ".tools\dioxus-$version"
$dx = Join-Path $toolRoot "dx.exe"

if (-not (Test-Path -LiteralPath $dx)) {
    $headers = @{
        Accept = "application/vnd.github+json"
        "User-Agent" = "kotae-ai-dioxus-bootstrap"
        "X-GitHub-Api-Version" = "2022-11-28"
    }
    $release = Invoke-RestMethod `
        -Uri "https://api.github.com/repos/DioxusLabs/dioxus/releases/tags/v$version" `
        -Headers $headers
    $assetName = "dx-x86_64-pc-windows-msvc.zip"
    $asset = $release.assets | Where-Object { $_.name -eq $assetName } | Select-Object -First 1
    if ($null -eq $asset) {
        throw "Dioxus release asset $assetName was not found."
    }
    if ([string]::IsNullOrWhiteSpace($asset.digest) -or -not $asset.digest.StartsWith("sha256:")) {
        throw "Dioxus release does not publish a SHA-256 digest; refusing an unverified binary."
    }

    New-Item -ItemType Directory -Force -Path $toolRoot | Out-Null
    $archive = Join-Path $toolRoot "dx.partial.zip"
    if (Test-Path -LiteralPath $archive) {
        Remove-Item -LiteralPath $archive -Force
    }
    & curl.exe `
        --fail `
        --location `
        --silent `
        --show-error `
        --retry 3 `
        --output $archive `
        $asset.browser_download_url
    if ($LASTEXITCODE -ne 0) {
        throw "Dioxus CLI download failed."
    }

    $expected = $asset.digest.Substring(7).ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "Dioxus CLI SHA-256 mismatch. Expected $expected, got $actual."
    }

    Expand-Archive -LiteralPath $archive -DestinationPath $toolRoot -Force
    Remove-Item -LiteralPath $archive -Force
}

& $dx --version
if ($LASTEXITCODE -ne 0) {
    throw "Dioxus CLI version check failed."
}

Write-Output "DIOXUS=$dx"
