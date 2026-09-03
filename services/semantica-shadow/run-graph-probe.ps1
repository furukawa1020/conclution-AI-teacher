[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ProjectId,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z0-9.-]+-docker\.pkg\.dev/.+@sha256:[0-9a-f]{64}$')]
    [string]$ImageDigest,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^https://[a-z0-9-]+(?:\.[a-z0-9-]+)*\.run\.app$')]
    [string]$TargetUrl,
    [ValidateRange(1, 100)][int]$RequestCount = 20,
    [string]$Region = 'asia-northeast1',
    [string]$JobName = 'kotae-semantica-graph-probe',
    [string]$GcloudPath = '.tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd'
)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$gcloud = if ([System.IO.Path]::IsPathRooted($GcloudPath)) {
    [System.IO.Path]::GetFullPath($GcloudPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))
}
$probePath = Join-Path $PSScriptRoot 'authenticated_graph_probe.py'
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "fixed Google Cloud CLI is missing: $gcloud"
}
if (-not (Test-Path -LiteralPath $probePath -PathType Leaf)) {
    throw 'authenticated graph probe source is missing'
}

$savedErrorAction = $ErrorActionPreference
try {
    $ErrorActionPreference = 'SilentlyContinue'
    & $gcloud run jobs describe $JobName --project $ProjectId --region $Region --format='value(metadata.name)' *> $null
    $jobExists = $LASTEXITCODE -eq 0
} finally {
    $ErrorActionPreference = $savedErrorAction
}
if ($jobExists) {
    throw "refusing to replace an existing Cloud Run Job: $JobName"
}

$source = Get-Content -LiteralPath $probePath -Raw
$encoded = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($source))
$jobArgument = "--args=-c,import base64;exec(base64.b64decode('$encoded'))"
$created = $false
$startedAt = (Get-Date).ToUniversalTime()
try {
    & $gcloud run jobs create $JobName `
        --project $ProjectId `
        --region $Region `
        --image $ImageDigest `
        --service-account "kotae-api-runtime@$ProjectId.iam.gserviceaccount.com" `
        --command python `
        $jobArgument `
        --set-env-vars "KOTAE_TARGET_URL=$TargetUrl,KOTAE_AUDIENCE=$TargetUrl,KOTAE_PROBE_COUNT=$RequestCount" `
        --tasks 1 `
        --max-retries 0 `
        --task-timeout 300s `
        --cpu 2 `
        --memory 4Gi `
        --quiet
    if ($LASTEXITCODE -ne 0) {
        throw 'Cloud Run probe Job creation failed'
    }
    $created = $true
    $executionLines = @(& $gcloud run jobs execute $JobName `
        --project $ProjectId --region $Region --wait --format='value(metadata.name)')
    $executionExitCode = $LASTEXITCODE
    if ($executionExitCode -ne 0) {
        throw 'Cloud Run graph probe execution failed'
    }
    $executionName = $executionLines | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Last 1
    [pscustomobject]@{
        job = $JobName
        execution = $executionName
        requestCount = $RequestCount
        startedAt = $startedAt.ToString('o')
        completedAt = (Get-Date).ToUniversalTime().ToString('o')
    } | ConvertTo-Json -Compress
} finally {
    if ($created) {
        & $gcloud run jobs delete $JobName --project $ProjectId --region $Region --quiet
        if ($LASTEXITCODE -ne 0) {
            throw 'temporary Cloud Run probe Job cleanup failed'
        }
    }
}
