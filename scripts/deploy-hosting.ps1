[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")]
    [string] $ProjectId,

    [Parameter(Mandatory)]
    [ValidatePattern("^[0-9a-fA-F]{40}$")]
    [string] $ExpectedGitCommit,

    [string] $SiteId = "kotae-ai",

    [string] $PublicDirectory = "dist/web",

    [string] $RunService = "kotae-api",

    [string] $RunRegion = "asia-northeast1",

    [string] $GcloudPath = ".tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd",

    [switch] $PreflightOnly
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
Add-Type -AssemblyName System.Net.Http

$releaseProvenancePath = Join-Path $PSScriptRoot "release-provenance.ps1"
if (-not (Test-Path -LiteralPath $releaseProvenancePath -PathType Leaf)) {
    throw "The release provenance policy is missing."
}
. $releaseProvenancePath
$toolchainPolicyPath = Join-Path $PSScriptRoot "rust-wasm-toolchain.ps1"
if (-not (Test-Path -LiteralPath $toolchainPolicyPath -PathType Leaf)) {
    throw "The Rust/Wasm release toolchain policy is missing."
}
. $toolchainPolicyPath
$releaseToolchainConfiguration = Get-ReleaseToolchainConfiguration

$expectedProjectId = "kotae-ai-u22-2026"
$expectedProjectNumber = "551920539470"
$expectedAppId = "1:551920539470:web:6518baf6d84d7ab89eb01f"
$expectedSiteId = "kotae-ai"
$expectedDefaultUrl = "https://kotae-ai.web.app"
$expectedRunService = "kotae-api"
$expectedRunRegion = "asia-northeast1"
$expectedRunUrl = "https://kotae-api-r6kgkvtrmq-an.a.run.app"
$expectedRunWebSocketUrl = "wss://kotae-api-r6kgkvtrmq-an.a.run.app"
$expectedVoiceStreamUrl = "$expectedRunUrl/api/v1/voice/turns:stream"
$expectedVoiceLiveUrl = "$expectedRunWebSocketUrl/api/v1/voice/live"
$expectedSourceRevision = "main"
$expectedRuntimeServiceAccount = "kotae-api-runtime@$expectedProjectId.iam.gserviceaccount.com"
$expectedBuildServiceAccount = "projects/$expectedProjectId/serviceAccounts/kotae-api-builder@$expectedProjectId.iam.gserviceaccount.com"
$expectedStateSecretVersion = "1"
$releaseManifestName = ".kotae-release-manifest.json"
$requiredTtlCollectionGroups = @(
    "evaluations",
    "evaluationRateLimits",
    "voiceRateLimits",
    "guestVoiceRateLimits",
    "passkey_account_deletions_v1",
    "voiceLiveLeases",
    "passkey_ceremonies_v1",
    "passkeyClientRateLimits",
    "passkeyAppRateLimits"
)

$workspace = Split-Path -Parent $PSScriptRoot
$ExpectedGitCommit = $ExpectedGitCommit.ToLowerInvariant()
$publicRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace $PublicDirectory))
$expectedPublicRoot = [System.IO.Path]::GetFullPath((Join-Path $workspace "dist\web"))
if ([System.IO.Path]::IsPathRooted($GcloudPath)) {
    $gcloud = [System.IO.Path]::GetFullPath($GcloudPath)
} else {
    $gcloud = [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))
}

if ($ProjectId -cne $expectedProjectId) {
    throw "Refusing to deploy project '$ProjectId'; expected '$expectedProjectId'."
}
if ($SiteId -cne $expectedSiteId) {
    throw "Refusing to deploy Hosting site '$SiteId'; expected '$expectedSiteId'."
}
if ($RunService -cne $expectedRunService -or $RunRegion -cne $expectedRunRegion) {
    throw "Refusing an unexpected Cloud Run target; expected $expectedRunService in $expectedRunRegion."
}
if (-not [string]::Equals(
        $publicRoot.TrimEnd("\", "/"),
        $expectedPublicRoot.TrimEnd("\", "/"),
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
    throw "Refusing to publish outside the generated dist/web directory."
}
if (-not (Test-Path -LiteralPath $publicRoot -PathType Container)) {
    throw "Hosting public directory does not exist: $publicRoot"
}
if (
    ((Get-Item -LiteralPath $publicRoot -Force).Attributes -band
        [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw "Hosting public directory must not be a reparse point."
}
if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "Google Cloud CLI does not exist: $gcloud"
}

function Get-GzipBytes {
    param(
        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )

    $output = [System.IO.MemoryStream]::new()
    try {
        $gzip = [System.IO.Compression.GZipStream]::new(
            $output,
            [System.IO.Compression.CompressionLevel]::Optimal,
            $true
        )
        try {
            $gzip.Write($Bytes, 0, $Bytes.Length)
        } finally {
            $gzip.Dispose()
        }
        # Prevent PowerShell from unrolling byte[] into an object pipeline.
        return ,$output.ToArray()
    } finally {
        $output.Dispose()
    }
}

function Invoke-ReleaseGitText {
    param(
        [Parameter(Mandatory)]
        [System.Management.Automation.ApplicationInfo] $GitCommand,

        [Parameter(Mandatory)]
        [string[]] $CommandArguments,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    $lines = @(& $GitCommand.Source -C $workspace @CommandArguments 2>$null)
    if ($LASTEXITCODE -ne 0) {
        throw "Git failed while $Operation."
    }
    return ($lines -join [System.Environment]::NewLine).Trim()
}

function Assert-HostingReleaseSource {
    $git = Get-CanonicalGitCommand
    $repositoryRoot = Invoke-ReleaseGitText `
        -GitCommand $git `
        -CommandArguments @("rev-parse", "--show-toplevel") `
        -Operation "checking the Hosting release repository root"
    if (-not [string]::Equals(
            [System.IO.Path]::GetFullPath($repositoryRoot).TrimEnd("\", "/"),
            [System.IO.Path]::GetFullPath($workspace).TrimEnd("\", "/"),
            [System.StringComparison]::OrdinalIgnoreCase
        )) {
        throw "Hosting releases must run from the expected repository root."
    }

    $head = Invoke-ReleaseGitText `
        -GitCommand $git `
        -CommandArguments @("rev-parse", "--verify", "HEAD") `
        -Operation "checking the Hosting release commit"
    $originMain = Invoke-ReleaseGitText `
        -GitCommand $git `
        -CommandArguments @("rev-parse", "--verify", "origin/main") `
        -Operation "checking origin/main for the Hosting release"
    if ($head -cne $ExpectedGitCommit -or $originMain -cne $ExpectedGitCommit) {
        throw "Hosting release commit must equal both HEAD and origin/main."
    }

    $status = Invoke-ReleaseGitText `
        -GitCommand $git `
        -CommandArguments @("status", "--porcelain=v1", "--untracked-files=all") `
        -Operation "checking the Hosting release working tree"
    if (-not [string]::IsNullOrWhiteSpace($status)) {
        throw "Hosting releases require a clean tracked and untracked working tree."
    }
}

function ConvertTo-Sha256Hex {
    param(
        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($sha256.ComputeHash($Bytes))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
}

function Invoke-FirebaseJson {
    param(
        [Parameter(Mandatory)]
        [ValidateSet("Get", "Post", "Patch")]
        [string] $Method,

        [Parameter(Mandatory)]
        [string] $Uri,

        [Parameter(Mandatory)]
        [hashtable] $Headers,

        [object] $Body
    )

    $parameters = @{
        Method      = $Method
        Uri         = $Uri
        Headers     = $Headers
        ContentType = "application/json"
    }
    if ($PSBoundParameters.ContainsKey("Body")) {
        $parameters.Body = $Body | ConvertTo-Json -Depth 20 -Compress
    }
    return Invoke-RestMethod @parameters
}

function Assert-FirebaseGuestAuthBoundary {
    param(
        [Parameter(Mandatory)]
        [hashtable] $Headers
    )

    $identityConfig = Invoke-FirebaseJson `
        -Method Get `
        -Uri "https://identitytoolkit.googleapis.com/admin/v2/projects/$ProjectId/config" `
        -Headers $Headers
    if (
        $identityConfig.name -cne "projects/$expectedProjectNumber/config" -or
        $identityConfig.signIn.anonymous.enabled -cne $true
    ) {
        throw "Firebase anonymous authentication is not enabled for the reviewed project."
    }
    $authorizedDomains = @($identityConfig.authorizedDomains)
    if (
        $authorizedDomains.Count -ne ($authorizedDomains | Select-Object -Unique).Count -or
        $authorizedDomains -notcontains "kotae-ai.web.app"
    ) {
        throw "Firebase Auth does not authorize the reviewed Hosting domain exactly once."
    }
}

function Invoke-BinaryUpload {
    param(
        [Parameter(Mandatory)]
        [string] $Uri,

        [Parameter(Mandatory)]
        [string] $AccessToken,

        [Parameter(Mandatory)]
        [string] $QuotaProject,

        [Parameter(Mandatory)]
        [byte[]] $Bytes
    )

    $client = [System.Net.Http.HttpClient]::new()
    $request = [System.Net.Http.HttpRequestMessage]::new(
        [System.Net.Http.HttpMethod]::Post,
        $Uri
    )
    $content = [System.Net.Http.ByteArrayContent]::new($Bytes)
    $response = $null
    try {
        $request.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new(
            "Bearer",
            $AccessToken
        )
        $request.Headers.Add("x-goog-user-project", $QuotaProject)
        $content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new(
            "application/octet-stream"
        )
        $request.Content = $content

        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            $detail = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            throw "Firebase Hosting upload failed with HTTP $([int]$response.StatusCode): $detail"
        }
    } finally {
        $content.Dispose()
        $request.Dispose()
        if ($null -ne $response) {
            $response.Dispose()
        }
        $client.Dispose()
    }
}

function Invoke-GcloudJson {
    param(
        [Parameter(Mandatory)]
        [string[]] $CommandArguments,

        [Parameter(Mandatory)]
        [string] $Operation
    )

    # Windows PowerShell treats native stderr as PowerShell errors. gcloud can
    # write successful progress messages there, so inspect its native exit code
    # instead of letting ErrorActionPreference=Stop terminate this preflight.
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        $jsonLines = @(& $gcloud @CommandArguments 2>$null)
        $commandExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($commandExitCode -ne 0) {
        throw "Google Cloud CLI failed while $Operation."
    }
    $jsonText = ($jsonLines -join [System.Environment]::NewLine).Trim()
    if ([string]::IsNullOrWhiteSpace($jsonText)) {
        throw "Google Cloud CLI returned no JSON while $Operation."
    }
    try {
        # Windows PowerShell 5.1 preserves a top-level JSON array as one
        # non-enumerated pipeline object when ConvertFrom-Json is returned
        # directly. Materialize it first so callers receive every TTL policy.
        $decoded = ConvertFrom-Json -InputObject $jsonText -ErrorAction Stop
    } catch {
        throw "Google Cloud CLI returned invalid JSON while $Operation."
    }
    return @($decoded)
}

function Assert-BrowserAudioGate {
    param(
        [Parameter(Mandatory)]
        [ValidatePattern("^[0-9a-f]{64}$")]
        [string] $ExpectedManifestSha256
    )

    $browserGatePath = Join-Path $PSScriptRoot "test-browser-audio.mjs"
    if (-not (Test-Path -LiteralPath $browserGatePath -PathType Leaf)) {
        throw "The browser audio release gate is missing."
    }
    $node = Get-Command "node" -CommandType Application -ErrorAction Stop
    $browserArguments = @(
        $browserGatePath,
        "--dist", $publicRoot,
        "--expected-commit", $ExpectedGitCommit,
        "--expected-manifest-sha256", $ExpectedManifestSha256
    )

    # Windows PowerShell can surface native stderr as terminating errors. The
    # browser gate is authoritative through its native exit code and JSON.
    $previousErrorActionPreference = $ErrorActionPreference
    $jsonLines = @()
    $commandExitCode = -1
    try {
        $ErrorActionPreference = "Continue"
        $jsonLines = @(& $node.Source @browserArguments 2>$null)
        $commandExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    if ($commandExitCode -ne 0) {
        throw "The real-browser AudioWorklet release gate failed."
    }
    $jsonText = ($jsonLines -join [System.Environment]::NewLine).Trim()
    if ([string]::IsNullOrWhiteSpace($jsonText)) {
        throw "The real-browser AudioWorklet release gate returned no JSON."
    }
    try {
        $result = ConvertFrom-Json -InputObject $jsonText -ErrorAction Stop
    } catch {
        throw "The real-browser AudioWorklet release gate returned invalid JSON."
    }
    $resultProperties = @($result.PSObject.Properties.Name | Sort-Object)
    if (
        ($resultProperties -join ",") -cne
            "acousticCoverageWireValidated,acousticExchangeabilityValidated,directWasmGenerationIsolation,freshGenerationFrames,guestAFirstSprintSloValidated,guestQuietOnsetValidated,intentionalFastLaneValidated,manifestSha256,observationAddingValidated,provenance,quietSpectralCompensationValidated,quietSubbandEvidenceValidated,sameContextReuseFrames,sameContextReuseIsolated,sampleRateHz,senderDetachGuardPassed,sourceCommit,status,temporalVadClockValidated,wrappedFrames,zeroOutputCapture" -or
        $result.status -cne "passed" -or
        $result.provenance -cne "release" -or
        $result.sourceCommit -cne $ExpectedGitCommit -or
        $result.manifestSha256 -cne $ExpectedManifestSha256 -or
        [int] $result.sampleRateHz -ne 48000 -or
        [bool] $result.zeroOutputCapture -ne $true -or
        [bool] $result.directWasmGenerationIsolation -ne $true -or
        [bool] $result.intentionalFastLaneValidated -ne $true -or
        [bool] $result.observationAddingValidated -ne $true -or
        [bool] $result.acousticExchangeabilityValidated -ne $true -or
        [bool] $result.acousticCoverageWireValidated -ne $true -or
        [bool] $result.guestQuietOnsetValidated -ne $true -or
        [bool] $result.quietSubbandEvidenceValidated -ne $true -or
        [bool] $result.quietSpectralCompensationValidated -ne $true -or
        [bool] $result.guestAFirstSprintSloValidated -ne $true -or
        [bool] $result.temporalVadClockValidated -ne $true -or
        [int] $result.wrappedFrames -ne 5 -or
        [int] $result.freshGenerationFrames -ne 3 -or
        [int] $result.sameContextReuseFrames -ne 2 -or
        [bool] $result.sameContextReuseIsolated -ne $true -or
        [bool] $result.senderDetachGuardPassed -ne $true
    ) {
        throw "The real-browser AudioWorklet release gate did not attest the reviewed release commit."
    }
}

function Assert-PromotedBackendBoundary {
    $service = Invoke-GcloudJson `
        -Operation "checking the promoted Cloud Run service" `
        -CommandArguments @(
            "run", "services", "describe", $RunService,
            "--project=$ProjectId",
            "--region=$RunRegion",
            "--format=json",
            "--quiet",
            "--verbosity=error"
        )

    if (
        $service.metadata.name -cne $expectedRunService -or
        $service.status.url.TrimEnd("/") -cne $expectedRunUrl
    ) {
        throw "The promoted Cloud Run service does not match the reviewed service boundary."
    }

    Assert-SourceProvenanceLabels `
        -ExpectedGitCommit $ExpectedGitCommit `
        -ExpectedSourceRevision $expectedSourceRevision `
        -Labels $service.metadata.labels `
        -Boundary "The promoted Cloud Run service"
    Assert-SourceProvenanceLabels `
        -ExpectedGitCommit $ExpectedGitCommit `
        -ExpectedSourceRevision $expectedSourceRevision `
        -Labels $service.spec.template.metadata.labels `
        -Boundary "The promoted Cloud Run revision template"

    $serviceAnnotations = $service.metadata.annotations
    $buildAccountProperty = $serviceAnnotations.PSObject.Properties[
        "run.googleapis.com/build-service-account"
    ]
    $ingressProperty = $serviceAnnotations.PSObject.Properties[
        "run.googleapis.com/ingress-status"
    ]
    $minimumProperty = $serviceAnnotations.PSObject.Properties[
        "run.googleapis.com/minScale"
    ]
    $maximumProperty = $serviceAnnotations.PSObject.Properties[
        "run.googleapis.com/maxScale"
    ]
    if (
        $null -eq $buildAccountProperty -or
        [string] $buildAccountProperty.Value -cne $expectedBuildServiceAccount -or
        $null -eq $ingressProperty -or
        [string] $ingressProperty.Value -cne "all" -or
        $null -eq $minimumProperty -or
        [string] $minimumProperty.Value -cne "1" -or
        $null -eq $maximumProperty -or
        [string] $maximumProperty.Value -cne "3"
    ) {
        throw "The promoted Cloud Run service annotations are outside the reviewed boundary."
    }

    $traffic = @($service.status.traffic)
    $promotedRevisionName = Get-SolePromotedRevisionName `
        -Traffic $traffic `
        -LatestReadyRevisionName ([string] $service.status.latestReadyRevisionName) `
        -Boundary "The promoted Cloud Run traffic"
    $promotedRevision = Invoke-GcloudJson `
        -Operation "checking the revision that receives production traffic" `
        -CommandArguments @(
            "run", "revisions", "describe", $promotedRevisionName,
            "--project=$ProjectId",
            "--region=$RunRegion",
            "--format=json",
            "--quiet",
            "--verbosity=error"
        )
    Assert-SourceProvenanceLabels `
        -ExpectedGitCommit $ExpectedGitCommit `
        -ExpectedSourceRevision $expectedSourceRevision `
        -Labels $promotedRevision.metadata.labels `
        -Boundary "The revision receiving production traffic"
    $revisionServiceLabel = $promotedRevision.metadata.labels.PSObject.Properties[
        "serving.knative.dev/service"
    ]
    $revisionAnnotations = $promotedRevision.metadata.annotations
    $executionEnvironment = $revisionAnnotations.PSObject.Properties[
        "run.googleapis.com/execution-environment"
    ]
    $startupCpuBoost = $revisionAnnotations.PSObject.Properties[
        "run.googleapis.com/startup-cpu-boost"
    ]
    if (
        $promotedRevision.metadata.name -cne $promotedRevisionName -or
        $null -eq $revisionServiceLabel -or
        [string] $revisionServiceLabel.Value -cne $expectedRunService -or
        $null -eq $executionEnvironment -or
        [string] $executionEnvironment.Value -cne "gen2" -or
        $null -eq $startupCpuBoost -or
        [string] $startupCpuBoost.Value -cne "true" -or
        $promotedRevision.spec.serviceAccountName -cne $expectedRuntimeServiceAccount -or
        [int] $promotedRevision.spec.timeoutSeconds -ne 420 -or
        [int] $promotedRevision.spec.containerConcurrency -ne 4
    ) {
        throw "The revision receiving production traffic does not match the reviewed runtime boundary."
    }

    $containers = @($promotedRevision.spec.containers)
    if ($containers.Count -ne 1) {
        throw "The promoted Cloud Run service must contain exactly one container."
    }
    $container = $containers[0]
    if (
        [string] $container.resources.limits.cpu -cne "1" -or
        [string] $container.resources.limits.memory -cne "1Gi" -or
        [string] $container.image -cnotmatch '^asia-northeast1-docker\.pkg\.dev/kotae-ai-u22-2026/cloud-run-source-deploy/kotae-api@sha256:[0-9a-f]{64}$' -or
        [string] $promotedRevision.status.imageDigest -cne [string] $container.image
    ) {
        throw "The promoted Cloud Run image or resource limits are outside the reviewed boundary."
    }
    $environment = @{}
    foreach ($entry in @($container.env)) {
        $nameProperty = $entry.PSObject.Properties["name"]
        if ($null -eq $nameProperty -or [string]::IsNullOrWhiteSpace([string] $nameProperty.Value)) {
            throw "The promoted Cloud Run service contains an invalid environment entry."
        }
        $name = [string] $nameProperty.Value
        if ($environment.ContainsKey($name)) {
            throw "The promoted Cloud Run service contains a duplicate environment entry."
        }
        $valueProperty = $entry.PSObject.Properties["value"]
        if ($null -ne $valueProperty) {
            $environment[$name] = [string] $valueProperty.Value
        }
    }

    $requiredEnvironment = @{
        "KOTAE_ENV" = "production"
        "KOTAE_ALLOW_INSECURE_DEV" = "false"
        "GOOGLE_CLOUD_PROJECT" = $expectedProjectId
        "GOOGLE_CLOUD_LOCATION" = "global"
        "KOTAE_ALLOWED_APP_IDS" = $expectedAppId
        "KOTAE_STATE_V2_WRITES" = "true"
        "KOTAE_COACH_RESTATEMENT_BINDING" = "true"
        "KOTAE_ANSWER_PROOF_WRITES" = "true"
        "KOTAE_VERIFIER_PROGRESS_WRITES" = "true"
        "KOTAE_RETRIEVAL_POLICY_ENABLED" = "true"
        "KOTAE_SPEECH_LOCATION" = $expectedRunRegion
        "KOTAE_NATIVE_AUDIO_ENABLED" = "true"
        "KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED" = "true"
        "KOTAE_NATIVE_AUDIO_LOCATION" = "us-central1"
        "KOTAE_NATIVE_AUDIO_MODEL" = "gemini-live-2.5-flash-native-audio"
        "KOTAE_NATIVE_AUDIO_VOICE" = "Kore"
        "KOTAE_PASSKEY_RP_ID" = $expectedDefaultUrl.Replace("https://", "")
        "KOTAE_PASSKEY_ORIGIN" = $expectedDefaultUrl
        "KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE" = "true"
        "KOTAE_GUEST_MODE_ENABLED" = "true"
    }
    foreach ($name in $requiredEnvironment.Keys) {
        if (
            -not $environment.ContainsKey($name) -or
            [string] $environment[$name] -cne [string] $requiredEnvironment[$name]
        ) {
            throw "The promoted Cloud Run service is missing a required security setting."
        }
    }
    foreach ($legacyName in @(
            "KOTAE_PRIVACY_LOCATION",
            "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE",
            "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY",
            "KOTAE_RETRIEVAL_BELIEF_WRITES",
            "KOTAE_SPEECH_FALLBACK_MODEL",
            "KOTAE_COACHING_ROLLOUT"
        )) {
        if ($environment.ContainsKey($legacyName)) {
            throw "The promoted Cloud Run service still contains a legacy security setting."
        }
    }

    $stateSecret = @($container.env | Where-Object { $_.name -ceq "KOTAE_STATE_KEY_BASE64" })
    if ($stateSecret.Count -ne 1) {
        throw "The promoted Cloud Run service does not have one state-key binding."
    }
    $secretReference = $stateSecret[0].valueFrom.secretKeyRef
    if (
        $secretReference.name -cne "kotae-conversation-state" -or
        [string] $secretReference.key -cne $expectedStateSecretVersion
    ) {
        throw "The promoted Cloud Run state key is not bound to a pinned Secret version."
    }

    $ttlPolicies = @(
        Invoke-GcloudJson `
            -Operation "checking required Firestore TTL policies" `
            -CommandArguments @(
                "firestore", "fields", "ttls", "list",
                "--database=(default)",
                "--project=$ProjectId",
                "--format=json",
                "--quiet",
                "--verbosity=error"
            )
    )
    $activeTtlGroups = @{}
    foreach ($policy in $ttlPolicies) {
        $resourceName = [string] $policy.name
        foreach ($collectionGroup in $requiredTtlCollectionGroups) {
            $suffix = "/collectionGroups/$collectionGroup/fields/expiresAt"
            if (-not $resourceName.EndsWith($suffix, [System.StringComparison]::Ordinal)) {
                continue
            }
            if ($activeTtlGroups.ContainsKey($collectionGroup)) {
                throw "Firestore returned a duplicate required TTL policy."
            }
            $activeTtlGroups[$collectionGroup] = [string] $policy.ttlConfig.state
        }
    }
    foreach ($collectionGroup in $requiredTtlCollectionGroups) {
        if (
            -not $activeTtlGroups.ContainsKey($collectionGroup) -or
            [string] $activeTtlGroups[$collectionGroup] -cne "ACTIVE"
        ) {
            throw "A required Firestore TTL policy is not ACTIVE."
        }
    }

    $health = Invoke-RestMethod `
        -Method Get `
        -Uri "$expectedRunUrl/health" `
        -TimeoutSec 20
    if ($health.status -cne "ok" -or $health.service -cne $expectedRunService) {
        throw "The promoted Cloud Run health boundary did not validate."
    }

    $boundaryClient = [System.Net.Http.HttpClient]::new()
    $boundaryResponse = $null
    try {
        $boundaryClient.Timeout = [System.TimeSpan]::FromSeconds(20)
        $boundaryResponse = $boundaryClient.GetAsync(
            "$expectedRunUrl/api/v1/me"
        ).GetAwaiter().GetResult()
        if ($boundaryResponse.StatusCode -ne [System.Net.HttpStatusCode]::Unauthorized) {
            throw "The promoted Cloud Run unauthenticated identity boundary did not return HTTP 401."
        }
    } finally {
        if ($null -ne $boundaryResponse) {
            $boundaryResponse.Dispose()
        }
        $boundaryClient.Dispose()
    }
}

function Assert-HostingArtifact {
    param(
        [Parameter(Mandatory)]
        [string] $Root
    )

    $manifestPath = Join-Path $Root $releaseManifestName
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "Hosting release manifest is missing. Build with -ExpectedGitCommit first."
    }
    $manifestEntry = Get-Item -LiteralPath $manifestPath -Force
    if (
        ($manifestEntry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
        $manifestEntry.Length -gt 64KB
    ) {
        throw "Hosting release manifest is outside the reviewed boundary."
    }
    $strictUtf8 = [System.Text.UTF8Encoding]::new($false, $true)
    try {
        $manifestBytes = [System.IO.File]::ReadAllBytes($manifestPath)
        $manifest = $strictUtf8.GetString($manifestBytes) |
            ConvertFrom-Json -ErrorAction Stop
    } catch {
        throw "Hosting release manifest is not valid UTF-8 JSON."
    }
    $manifestSha256 = ConvertTo-Sha256Hex -Bytes $manifestBytes
    $manifestProperties = @($manifest.PSObject.Properties.Name | Sort-Object)
    if (
        ($manifestProperties -join ",") -cne "artifacts,schemaVersion,sourceCommit,toolchain" -or
        [int] $manifest.schemaVersion -ne 2 -or
        [string] $manifest.sourceCommit -cne $ExpectedGitCommit
    ) {
        throw "Hosting release manifest identity does not match the reviewed commit."
    }
    Assert-ReleaseToolchainProvenance `
        -Toolchain $manifest.toolchain `
        -Configuration $releaseToolchainConfiguration

    $requiredFiles = @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "guest-a-first-slo-policy.mjs",
        "passkey-policy.mjs",
        "temporal-vad-clock.mjs",
        "pcm-capture-worklet.js",
        "voice-session-policy.mjs",
        "voice-prepare-slo-policy.mjs",
        "voice-start-slo-policy.mjs",
        "voice-stream-policy.mjs",
        "assets\main.css",
        "wasm\kotae_client.js",
        "wasm\kotae_client_bg.wasm",
        "wasm\kotae_pcm_ring_bg.wasm"
    )
    foreach ($relativePath in $requiredFiles) {
        $requiredPath = Join-Path $Root $relativePath
        if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
            throw "Required Hosting artifact is missing: $relativePath"
        }
    }
    $pcmRingWasmArtifact = Get-Item -LiteralPath (
        Join-Path $Root "wasm\kotae_pcm_ring_bg.wasm"
    )
    if ($pcmRingWasmArtifact.Length -le 0 -or $pcmRingWasmArtifact.Length -gt 256KB) {
        throw "PCM ring Wasm artifact must stay inside its 256 KiB runtime boundary."
    }

    $manifestArtifacts = @($manifest.artifacts)
    if ($manifestArtifacts.Count -eq 0) {
        throw "Hosting release manifest contains no artifacts."
    }
    $manifestByPath = @{}
    foreach ($artifact in $manifestArtifacts) {
        $artifactProperties = @($artifact.PSObject.Properties.Name | Sort-Object)
        $path = [string] $artifact.path
        $sha256 = [string] $artifact.sha256
        $bytes = [long] $artifact.bytes
        if (
            ($artifactProperties -join ",") -cne "bytes,path,sha256" -or
            [string]::IsNullOrWhiteSpace($path) -or
            $path -ceq $releaseManifestName -or
            $path -match '(^|/)\.\.(/|$)' -or
            $path.StartsWith("/", [System.StringComparison]::Ordinal) -or
            $path.Contains("\") -or
            $sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            $bytes -lt 0 -or
            $manifestByPath.ContainsKey($path)
        ) {
            throw "Hosting release manifest contains an invalid artifact entry."
        }
        $manifestByPath[$path] = [pscustomobject]@{
            sha256 = $sha256
            bytes = $bytes
        }
    }

    $totalBytes = 0L
    $snapshot = [ordered]@{}
    $entries = @(Get-ChildItem -LiteralPath $Root -Recurse -Force)
    foreach ($entry in $entries) {
        if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Hosting artifacts must not contain reparse points: $($entry.FullName)"
        }
        if ($entry.PSIsContainer) {
            continue
        }
        $relativePath = $entry.FullName.
            Substring($Root.TrimEnd("\", "/").Length).
            TrimStart("\", "/").
            Replace("\", "/")
        $allowedPath = (
            $relativePath -in @(
                "index.html",
                "bootstrap.js",
                "firebase-bridge.js",
                "guest-a-first-slo-policy.mjs",
                "passkey-policy.mjs",
                "temporal-vad-clock.mjs",
                "pcm-capture-worklet.js",
                "voice-session-policy.mjs",
                "voice-prepare-slo-policy.mjs",
                "voice-start-slo-policy.mjs",
                "voice-stream-policy.mjs",
                "assets/main.css",
                "wasm/kotae_client.js",
                "wasm/kotae_client_bg.wasm",
                "wasm/kotae_pcm_ring_bg.wasm",
                $releaseManifestName
            ) -or
            $relativePath -match '^wasm/snippets/[A-Za-z0-9._/-]+\.js$'
        )
        if (-not $allowedPath) {
            throw "Unexpected Hosting artifact: $relativePath"
        }
        if ($entry.Length -gt 15MB) {
            throw "Hosting artifact exceeds the 15 MiB safety limit: $($entry.FullName)"
        }
        if ($entry.Length -gt ([long]::MaxValue - $totalBytes)) {
            throw "Hosting artifact size overflow."
        }
        $totalBytes += $entry.Length
        if ($relativePath -ceq $releaseManifestName) {
            continue
        }
        if (-not $manifestByPath.ContainsKey($relativePath)) {
            throw "Hosting release manifest is missing an artifact."
        }
        $bytes = [System.IO.File]::ReadAllBytes($entry.FullName)
        $expectedArtifact = $manifestByPath[$relativePath]
        if (
            [long] $bytes.Length -ne [long] $expectedArtifact.bytes -or
            (ConvertTo-Sha256Hex -Bytes $bytes) -cne [string] $expectedArtifact.sha256
        ) {
            throw "Hosting artifact does not match its release manifest: $relativePath"
        }
        $snapshot[$relativePath] = $bytes
    }
    if ($totalBytes -gt 25MB) {
        throw "Hosting artifacts exceed the 25 MiB aggregate safety limit."
    }
    if ($snapshot.Count -ne $manifestByPath.Count) {
        throw "Hosting release manifest contains a missing or unexpected artifact."
    }

    try {
        $bridge = $strictUtf8.GetString([byte[]] $snapshot["firebase-bridge.js"])
    } catch {
        throw "firebase-bridge.js is not valid UTF-8."
    }
    $siteKeyMatch = [regex]::Match(
        $bridge,
        '(?m)^\s*const\s+RECAPTCHA_SITE_KEY\s*=\s*"(?<key>[^"]+)";\s*$'
    )
    if (
        -not $siteKeyMatch.Success -or
        $siteKeyMatch.Groups["key"].Value -eq "__RECAPTCHA_SITE_KEY__" -or
        $siteKeyMatch.Groups["key"].Value.Length -lt 20
    ) {
        throw "firebase-bridge.js does not contain a configured reCAPTCHA Enterprise site key."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-session-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice session policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-start-slo-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice start SLO policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./voice-stream-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice stream policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./guest-a-first-slo-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited guest A-first SLO policy module."
    }
    if ($bridge -notmatch [regex]::Escape('from "./passkey-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited passkey policy module."
    }
    if (
        $bridge -notmatch [regex]::Escape("const EXPECTED_PROJECT_ID = `"$expectedProjectId`";") -or
        $bridge -notmatch [regex]::Escape("const EXPECTED_APP_ID = `"$expectedAppId`";") -or
        $bridge -notmatch [regex]::Escape("const EXPECTED_MESSAGING_SENDER_ID = `"$expectedProjectNumber`";")
    ) {
        throw "firebase-bridge.js is not bound to the expected Firebase project."
    }
    if ($bridge -notmatch [regex]::Escape("`"$expectedVoiceStreamUrl`"")) {
        throw "firebase-bridge.js is not bound to the expected Cloud Run voice stream endpoint."
    }
    if ($bridge -notmatch [regex]::Escape("`"$expectedVoiceLiveUrl`"")) {
        throw "firebase-bridge.js is not bound to the expected Cloud Run live voice endpoint."
    }

    try {
        $bootstrap = $strictUtf8.GetString([byte[]] $snapshot["bootstrap.js"])
    } catch {
        throw "bootstrap.js is not valid UTF-8."
    }
    $bridgeImport = $bootstrap.IndexOf('import("/firebase-bridge.js")', [System.StringComparison]::Ordinal)
    $wasmImport = $bootstrap.IndexOf('import("/wasm/kotae_client.js")', [System.StringComparison]::Ordinal)
    if ($bridgeImport -lt 0 -or $wasmImport -lt 0 -or $bridgeImport -gt $wasmImport) {
        throw "bootstrap.js must load firebase-bridge.js before the Rust/Wasm module."
    }

    try {
        $index = $strictUtf8.GetString([byte[]] $snapshot["index.html"])
    } catch {
        throw "index.html is not valid UTF-8."
    }
    if (
        [regex]::Matches($index, '<script\b', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase).Count -ne 1 -or
        $index -notmatch '<script\s+type="module"\s+src="/bootstrap\.js"></script>'
    ) {
        throw "index.html must contain only the external bootstrap.js module script."
    }
    return [pscustomobject]@{
        Snapshot       = $snapshot
        ManifestSha256 = $manifestSha256
    }
}

Assert-HostingReleaseSource
$hostingRelease = Assert-HostingArtifact -Root $publicRoot
$hostingSnapshot = $hostingRelease.Snapshot
Assert-BrowserAudioGate `
    -ExpectedManifestSha256 $hostingRelease.ManifestSha256
Assert-PromotedBackendBoundary
$token = ((& $gcloud auth print-access-token) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($token)) {
    throw "Could not obtain a Google Cloud access token."
}

$headers = @{
    Authorization         = "Bearer $token"
    "x-goog-user-project" = $ProjectId
}
Assert-FirebaseGuestAuthBoundary -Headers $headers
if ($PreflightOnly) {
    Write-Output "HOSTING_PREFLIGHT=PASS"
    return
}

$apiRoot = "https://firebasehosting.googleapis.com/v1beta1"
$versionName = $null

try {
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

    $site = Invoke-FirebaseJson `
        -Method Get `
        -Uri "$apiRoot/projects/$ProjectId/sites/$SiteId" `
        -Headers $headers
    $validSiteNames = @(
        "projects/$expectedProjectId/sites/$expectedSiteId",
        "projects/$expectedProjectNumber/sites/$expectedSiteId"
    )
    if (
        $validSiteNames -notcontains $site.name -or
        $site.defaultUrl.TrimEnd("/") -cne $expectedDefaultUrl
    ) {
        throw "Firebase Hosting site identity check failed."
    }

    $fileHashes = [ordered]@{}
    $gzipByHash = @{}
    $artifactPaths = @($hostingSnapshot.Keys | Sort-Object)
    if ($artifactPaths.Count -eq 0) {
        throw "Hosting public directory is empty."
    }

    foreach ($relative in $artifactPaths) {
        $gzipBytes = Get-GzipBytes -Bytes ([byte[]] $hostingSnapshot[$relative])
        $hash = ConvertTo-Sha256Hex -Bytes $gzipBytes
        $fileHashes["/$relative"] = $hash
        $gzipByHash[$hash] = $gzipBytes
    }

    $version = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/projects/-/sites/$SiteId/versions" `
        -Headers $headers `
        -Body @{
            status = "CREATED"
            labels = @{
                "deployment-tool" = "kotae-secure-rest"
            }
        }
    $versionName = $version.name
    if ([string]::IsNullOrWhiteSpace($versionName)) {
        throw "Firebase Hosting did not return a version name."
    }

    $populate = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/$versionName`:populateFiles" `
        -Headers $headers `
        -Body @{ files = $fileHashes }

    $requiredHashes = @()
    $requiredHashesProperty = $populate.PSObject.Properties["uploadRequiredHashes"]
    if ($null -ne $requiredHashesProperty) {
        $requiredHashes = @($requiredHashesProperty.Value)
    }
    foreach ($hash in $requiredHashes) {
        if ([string]::IsNullOrWhiteSpace($hash)) {
            continue
        }
        if (-not $gzipByHash.ContainsKey($hash)) {
            throw "Firebase requested an unknown file hash."
        }
        $uploadUri = "$($populate.uploadUrl.TrimEnd("/"))/$hash"
        Invoke-BinaryUpload `
            -Uri $uploadUri `
            -AccessToken $token `
            -QuotaProject $ProjectId `
            -Bytes $gzipByHash[$hash]
    }

    $csp = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self' 'wasm-unsafe-eval' https://www.gstatic.com/firebasejs/12.16.0/ https://www.gstatic.com/recaptcha/ https://www.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; script-src-attr 'none'; style-src 'self'; style-src-attr 'unsafe-hashes' 'sha256-biLFinpqYMtWHmXfkA1BPeCY0/fNt46SAZ+BBk5YUog=' 'sha256-aqNNdDLnnrDOnTNdkJpYlAxKVJtLt9CtFLklmInuUAE=' 'sha256-ZdHxw9eWtnxUb3mk6tBS+gIiVUPE3pGM470keHPDFlE='; img-src 'self' data:; font-src 'self'; connect-src 'self' $expectedRunUrl $expectedRunWebSocketUrl https://identitytoolkit.googleapis.com https://securetoken.googleapis.com https://content-firebaseappcheck.googleapis.com https://www.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; frame-src https://kotae-ai-u22-2026.firebaseapp.com https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/ https://www.recaptcha.net/recaptcha/; worker-src 'self'; manifest-src 'self'; upgrade-insecure-requests"
    $hostingConfig = @{
        cleanUrls = $true
        trailingSlashBehavior = "REMOVE"
        headers = @(
            @{
                glob = "**"
                headers = @{
                    "Content-Security-Policy" = $csp
                    "Cross-Origin-Opener-Policy" = "same-origin-allow-popups"
                    "Cross-Origin-Resource-Policy" = "same-origin"
                    "Permissions-Policy" = "camera=(), geolocation=(), microphone=(self), payment=(), usb=()"
                    "Referrer-Policy" = "no-referrer"
                    "X-Content-Type-Options" = "nosniff"
                    "Cache-Control" = "no-store"
                }
            }
        )
        rewrites = @(
            @{
                glob = "/api/**"
                run = @{
                    serviceId = $RunService
                    region = $RunRegion
                }
            },
            @{
                glob = "**"
                path = "/index.html"
            }
        )
    }

    $encodedMask = [System.Uri]::EscapeDataString("status,config")
    $null = Invoke-FirebaseJson `
        -Method Patch `
        -Uri "$apiRoot/$versionName`?updateMask=$encodedMask" `
        -Headers $headers `
        -Body @{
            status = "FINALIZED"
            config = $hostingConfig
        }

    $encodedVersion = [System.Uri]::EscapeDataString($versionName)
    $release = Invoke-FirebaseJson `
        -Method Post `
        -Uri "$apiRoot/projects/-/sites/$SiteId/channels/live/releases?versionName=$encodedVersion" `
        -Headers $headers `
        -Body @{
            message = "KOTAE AI secure Rust/Wasm launch"
        }

    Write-Output "HOSTING_URL=$($site.defaultUrl)"
    Write-Output "HOSTING_VERSION=$versionName"
    Write-Output "HOSTING_RELEASE=$($release.name)"
} catch {
    if (-not [string]::IsNullOrWhiteSpace($versionName)) {
        try {
            $null = Invoke-FirebaseJson `
                -Method Patch `
                -Uri "$apiRoot/$versionName`?updateMask=status" `
                -Headers $headers `
                -Body @{ status = "ABANDONED" }
        } catch {
            Write-Warning "Could not abandon failed Hosting version $versionName."
        }
    }
    throw
}
