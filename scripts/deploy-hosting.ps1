[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")]
    [string] $ProjectId,

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
$expectedRuntimeServiceAccount = "kotae-api-runtime@$expectedProjectId.iam.gserviceaccount.com"
$expectedBuildServiceAccount = "projects/$expectedProjectId/serviceAccounts/kotae-api-builder@$expectedProjectId.iam.gserviceaccount.com"
$requiredTtlCollectionGroups = @(
    "evaluations",
    "evaluationRateLimits",
    "voiceRateLimits",
    "voiceLiveLeases",
    "passkey_ceremonies_v1",
    "passkeyClientRateLimits",
    "passkeyAppRateLimits"
)

$workspace = Split-Path -Parent $PSScriptRoot
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
        [string] $Path
    )

    $inputBytes = [System.IO.File]::ReadAllBytes($Path)
    $output = [System.IO.MemoryStream]::new()
    try {
        $gzip = [System.IO.Compression.GZipStream]::new(
            $output,
            [System.IO.Compression.CompressionLevel]::Optimal,
            $true
        )
        try {
            $gzip.Write($inputBytes, 0, $inputBytes.Length)
        } finally {
            $gzip.Dispose()
        }
        # Prevent PowerShell from unrolling byte[] into an object pipeline.
        return ,$output.ToArray()
    } finally {
        $output.Dispose()
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
        $service.status.url.TrimEnd("/") -cne $expectedRunUrl -or
        $service.spec.template.spec.serviceAccountName -cne $expectedRuntimeServiceAccount -or
        [int] $service.spec.template.spec.timeoutSeconds -ne 420 -or
        [int] $service.spec.template.spec.containerConcurrency -ne 4
    ) {
        throw "The promoted Cloud Run service does not match the reviewed runtime boundary."
    }

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
    if (
        $traffic.Count -ne 1 -or
        [int] $traffic[0].percent -ne 100 -or
        [string]::IsNullOrWhiteSpace([string] $traffic[0].revisionName) -or
        [string] $traffic[0].revisionName -cne [string] $service.status.latestReadyRevisionName
    ) {
        throw "The latest ready Cloud Run revision is not the sole promoted revision."
    }

    $containers = @($service.spec.template.spec.containers)
    if ($containers.Count -ne 1) {
        throw "The promoted Cloud Run service must contain exactly one container."
    }
    $environment = @{}
    foreach ($entry in @($containers[0].env)) {
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
        "KOTAE_SPEECH_LOCATION" = $expectedRunRegion
        "KOTAE_NATIVE_AUDIO_ENABLED" = "true"
        "KOTAE_NATIVE_AUDIO_MODEL" = "gemini-live-2.5-flash-native-audio"
        "KOTAE_NATIVE_AUDIO_VOICE" = "Kore"
        "KOTAE_PASSKEY_RP_ID" = $expectedDefaultUrl.Replace("https://", "")
        "KOTAE_PASSKEY_ORIGIN" = $expectedDefaultUrl
        "KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE" = "true"
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
            "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY"
        )) {
        if ($environment.ContainsKey($legacyName)) {
            throw "The promoted Cloud Run service still contains a legacy security setting."
        }
    }

    $stateSecret = @($containers[0].env | Where-Object { $_.name -ceq "KOTAE_STATE_KEY_BASE64" })
    if ($stateSecret.Count -ne 1) {
        throw "The promoted Cloud Run service does not have one state-key binding."
    }
    $secretReference = $stateSecret[0].valueFrom.secretKeyRef
    if (
        $secretReference.name -cne "kotae-conversation-state" -or
        [string] $secretReference.key -notmatch '^[1-9][0-9]*$'
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
}

function Assert-HostingArtifact {
    param(
        [Parameter(Mandatory)]
        [string] $Root
    )

    $requiredFiles = @(
        "index.html",
        "bootstrap.js",
        "firebase-bridge.js",
        "passkey-policy.mjs",
        "pcm-capture-worklet.js",
        "voice-session-policy.mjs",
        "voice-stream-policy.mjs",
        "assets\main.css",
        "wasm\kotae_client.js",
        "wasm\kotae_client_bg.wasm"
    )
    foreach ($relativePath in $requiredFiles) {
        $requiredPath = Join-Path $Root $relativePath
        if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
            throw "Required Hosting artifact is missing: $relativePath"
        }
    }

    $totalBytes = 0L
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
                "passkey-policy.mjs",
                "pcm-capture-worklet.js",
                "voice-session-policy.mjs",
                "voice-stream-policy.mjs",
                "assets/main.css",
                "wasm/kotae_client.js",
                "wasm/kotae_client_bg.wasm"
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
    }
    if ($totalBytes -gt 25MB) {
        throw "Hosting artifacts exceed the 25 MiB aggregate safety limit."
    }

    $bridgePath = Join-Path $Root "firebase-bridge.js"
    $bridge = [System.IO.File]::ReadAllText($bridgePath, [System.Text.Encoding]::UTF8)
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
    if ($bridge -notmatch [regex]::Escape('from "./voice-stream-policy.mjs";')) {
        throw "firebase-bridge.js must import the audited voice stream policy module."
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

    $bootstrap = [System.IO.File]::ReadAllText(
        (Join-Path $Root "bootstrap.js"),
        [System.Text.Encoding]::UTF8
    )
    $bridgeImport = $bootstrap.IndexOf('import("/firebase-bridge.js")', [System.StringComparison]::Ordinal)
    $wasmImport = $bootstrap.IndexOf('import("/wasm/kotae_client.js")', [System.StringComparison]::Ordinal)
    if ($bridgeImport -lt 0 -or $wasmImport -lt 0 -or $bridgeImport -gt $wasmImport) {
        throw "bootstrap.js must load firebase-bridge.js before the Rust/Wasm module."
    }

    $index = [System.IO.File]::ReadAllText(
        (Join-Path $Root "index.html"),
        [System.Text.Encoding]::UTF8
    )
    if (
        [regex]::Matches($index, '<script\b', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase).Count -ne 1 -or
        $index -notmatch '<script\s+type="module"\s+src="/bootstrap\.js"></script>'
    ) {
        throw "index.html must contain only the external bootstrap.js module script."
    }
}

Assert-HostingArtifact -Root $publicRoot
Assert-PromotedBackendBoundary
if ($PreflightOnly) {
    Write-Output "HOSTING_PREFLIGHT=PASS"
    return
}

$token = ((& $gcloud auth print-access-token) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($token)) {
    throw "Could not obtain a Google Cloud access token."
}

$headers = @{
    Authorization         = "Bearer $token"
    "x-goog-user-project" = $ProjectId
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
    $files = @(Get-ChildItem -LiteralPath $publicRoot -Recurse -File -Force | Sort-Object FullName)
    if ($files.Count -eq 0) {
        throw "Hosting public directory is empty."
    }

    foreach ($file in $files) {
        $relative = $file.FullName.Substring($publicRoot.Length).TrimStart("\", "/").Replace("\", "/")
        $gzipBytes = Get-GzipBytes -Path $file.FullName
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
