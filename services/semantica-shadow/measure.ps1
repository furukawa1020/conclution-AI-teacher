[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ProjectId,
    [Parameter(Mandatory = $true)][string]$Revision,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$ImageDigest,
    [Parameter(Mandatory = $true)][datetime]$StartTime,
    [Parameter(Mandatory = $true)][datetime]$EndTime,
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9a-f]{40}$')]
    [string]$SourceCommit,
    [Parameter(Mandatory = $true)][string]$BuildId,
    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 10000)]
    [int]$ExpectedSuccessCount,
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$Region = 'asia-northeast1',
    [string]$Service = 'kotae-semantica-shadow',
    [string]$GcloudPath = '.tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd'
)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$gcloud = if ([System.IO.Path]::IsPathRooted($GcloudPath)) {
    [System.IO.Path]::GetFullPath($GcloudPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "fixed Google Cloud CLI is missing: $gcloud"
}
if ($EndTime.ToUniversalTime() -le $StartTime.ToUniversalTime()) {
    throw 'EndTime must be after StartTime'
}

function Invoke-GcloudJson {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $lines = @(& $script:gcloud @Arguments)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "gcloud command failed with exit code $exitCode"
    }
    return (($lines -join "`n") | ConvertFrom-Json)
}

function Get-MetricSummary {
    param(
        [string]$MetricType,
        [string]$Unit,
        [string]$ResponseCode = ''
    )
    $filter = "metric.type=`"$MetricType`" AND resource.type=`"cloud_run_revision`" AND resource.labels.service_name=`"$Service`" AND resource.labels.revision_name=`"$Revision`""
    if (-not [string]::IsNullOrWhiteSpace($ResponseCode)) {
        $filter += " AND metric.labels.response_code=`"$ResponseCode`""
    }
    $startUtc = $StartTime.ToUniversalTime().ToString('o')
    $endUtc = $EndTime.ToUniversalTime().ToString('o')
    $uri = "https://monitoring.googleapis.com/v3/projects/$ProjectId/timeSeries?filter=$([Uri]::EscapeDataString($filter))&interval.startTime=$([Uri]::EscapeDataString($startUtc))&interval.endTime=$([Uri]::EscapeDataString($endUtc))&view=FULL&pageSize=1000"
    $response = Invoke-RestMethod -Uri $uri -Headers @{ Authorization = "Bearer $script:accessToken" } -Method Get -TimeoutSec 60
    $samples = @(
        $response.timeSeries | ForEach-Object {
            $_.points | ForEach-Object {
                $distribution = $_.value.distributionValue
                $count = if ($null -eq $distribution.count) { 0 } else { [long]$distribution.count }
                if ($count -gt 0) {
                    [pscustomobject]@{
                        count = $count
                        mean = [double]$distribution.mean
                        endTime = [datetime]$_.interval.endTime
                    }
                }
            }
        }
    )
    if ($samples.Count -eq 0) {
        throw "no non-zero samples for $MetricType"
    }
    $sampleCount = [long](($samples | Measure-Object -Property count -Sum).Sum)
    $weightedSum = [double](($samples | ForEach-Object { $_.mean * $_.count } | Measure-Object -Sum).Sum)
    return [ordered]@{
        metricType = $MetricType
        unit = $Unit
        responseCode = if ($ResponseCode) { [int]$ResponseCode } else { $null }
        distributionPointCount = $samples.Count
        observationCount = $sampleCount
        weightedMean = $weightedSum / $sampleCount
        minimumPointMean = [double](($samples | Measure-Object -Property mean -Minimum).Minimum)
        maximumPointMean = [double](($samples | Measure-Object -Property mean -Maximum).Maximum)
        firstPointEndTime = ($samples | Sort-Object endTime | Select-Object -First 1).endTime.ToUniversalTime().ToString('o')
        lastPointEndTime = ($samples | Sort-Object endTime -Descending | Select-Object -First 1).endTime.ToUniversalTime().ToString('o')
    }
}

$revisionDescription = Invoke-GcloudJson run revisions describe $Revision `
    --project $ProjectId --region $Region --format=json
if ($revisionDescription.metadata.labels.'serving.knative.dev/service' -ne $Service) {
    throw 'revision does not belong to the required service'
}
$qualifiedDigest = $revisionDescription.status.imageDigest
if (-not $qualifiedDigest.EndsWith("@$ImageDigest", [StringComparison]::Ordinal)) {
    throw 'revision image digest does not match the requested digest'
}

$images = @(Invoke-GcloudJson artifacts docker images list `
    "asia-northeast1-docker.pkg.dev/$ProjectId/cloud-run-source-deploy/semantica-shadow" `
    --project $ProjectId --include-tags --format=json)
$image = @($images | Where-Object { $_.version -eq $ImageDigest })
if ($image.Count -ne 1) {
    throw 'exactly one Artifact Registry image must match the digest'
}

$tokens = @(& $gcloud auth print-access-token)
$tokenExitCode = $LASTEXITCODE
$accessToken = $tokens | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1
if ($tokenExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($accessToken)) {
    throw 'Cloud Monitoring access token is unavailable'
}

$metrics = [ordered]@{
    containerStartupLatency = Get-MetricSummary 'run.googleapis.com/container/startup_latencies' 'ms'
    containerMemoryUsage = Get-MetricSummary 'run.googleapis.com/container/memory/usage' 'By'
    successfulRequestLatency = Get-MetricSummary 'run.googleapis.com/request_latencies' 'ms' '204'
    successfulEndToEndLatency = Get-MetricSummary 'run.googleapis.com/request_latency/e2e_latencies' 'ms' '204'
    successfulPendingLatency = Get-MetricSummary 'run.googleapis.com/request_latency/pending' 'ms' '204'
}
foreach ($metricName in @('successfulRequestLatency', 'successfulEndToEndLatency', 'successfulPendingLatency')) {
    if ($metrics[$metricName].observationCount -ne $ExpectedSuccessCount) {
        throw "$metricName does not contain exactly $ExpectedSuccessCount successful observations"
    }
}

$document = [ordered]@{
    schemaVersion = 1
    provenance = [ordered]@{
        projectId = $ProjectId
        region = $Region
        service = $Service
        revision = $Revision
        sourceCommit = $SourceCommit
        cloudBuildId = $BuildId
        workload = 'content-free-fixed-graph-v1'
        expectedSuccessfulRequests = $ExpectedSuccessCount
        imageDigest = $ImageDigest
        imageSizeBytes = [long]$image[0].metadata.imageSizeBytes
        intervalStart = $StartTime.ToUniversalTime().ToString('o')
        intervalEnd = $EndTime.ToUniversalTime().ToString('o')
        gcloudVersion = '577.0.0'
    }
    metrics = $metrics
    interpretationBoundary = [ordered]@{
        percentileClaimed = $false
        reason = 'A percentile is not reported because the observed startup and successful request counts are too small.'
        requestLatencyExcludesStartup = $true
        endToEndLatencyIncludesGoogleNetwork = $true
        monitoringSamplingSeconds = 60
        monitoringVisibilityDelaySeconds = 120
    }
}

$json = $document | ConvertTo-Json -Depth 10
[System.IO.File]::WriteAllText(
    [System.IO.Path]::GetFullPath($OutputPath),
    ($json + "`n"),
    [System.Text.UTF8Encoding]::new($false)
)
