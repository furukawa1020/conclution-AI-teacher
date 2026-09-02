[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z][a-z0-9-]{4,28}[a-z0-9]$')]
    [string]$ProjectId,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z0-9.-]+-docker\.pkg\.dev/.+@sha256:[0-9a-f]{64}$')]
    [string]$ImageDigest,

    [string]$ContractPath = (Join-Path $PSScriptRoot 'deploy-contract.json'),

    [string]$GcloudPath = '.tools/gcloud-577.0.0/google-cloud-sdk/bin/gcloud.cmd'
)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$gcloud = if ([System.IO.Path]::IsPathRooted($GcloudPath)) {
    [System.IO.Path]::GetFullPath($GcloudPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $workspace $GcloudPath))
}

function Invoke-Gcloud {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    & $script:gcloud @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "gcloud command failed with exit code $LASTEXITCODE"
    }
}

if (-not (Test-Path -LiteralPath $gcloud -PathType Leaf)) {
    throw "fixed Google Cloud CLI is missing: $gcloud"
}
if (-not (Test-Path -LiteralPath $ContractPath -PathType Leaf)) {
    throw 'deployment contract is missing'
}

$contract = Get-Content -LiteralPath $ContractPath -Raw | ConvertFrom-Json
if ($contract.schemaVersion -ne 1 -or
    $contract.service -ne 'kotae-semantica-shadow' -or
    $contract.callerService -ne 'kotae-api' -or
    $contract.ingress -ne 'all' -or
    $contract.allowUnauthenticated -ne $false) {
    throw 'deployment contract is invalid'
}

$activeAccounts = @(& $gcloud auth list --filter=status:ACTIVE --format='value(account)')
$authExitCode = $LASTEXITCODE
$activeAccount = $activeAccounts | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1
if ($authExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($activeAccount)) {
    throw 'an active gcloud account is required'
}

$callerDescription = & $gcloud run services describe $contract.callerService `
    --project $ProjectId --region $contract.region --format=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $null -eq $callerDescription) {
    throw 'caller Cloud Run service cannot be verified'
}
$expectedCallerAccount = "$($contract.requiredInvokerAccount)@$ProjectId.iam.gserviceaccount.com"
if ($callerDescription.spec.template.spec.serviceAccountName -ne $expectedCallerAccount) {
    throw 'caller Cloud Run service does not use the required invoker identity'
}

$runtimeAccount = "$($contract.runtimeAccount)@$ProjectId.iam.gserviceaccount.com"
$invokerAccount = "$($contract.requiredInvokerAccount)@$ProjectId.iam.gserviceaccount.com"
foreach ($account in @($runtimeAccount, $invokerAccount)) {
    Invoke-Gcloud iam service-accounts describe $account --project $ProjectId --format='value(email)'
}

Invoke-Gcloud run deploy $contract.service `
    --project $ProjectId `
    --region $contract.region `
    --platform managed `
    --image $ImageDigest `
    --service-account $runtimeAccount `
    --ingress all `
    --no-allow-unauthenticated `
    --port $contract.port `
    --cpu $contract.cpu `
    --memory $contract.memory `
    --concurrency $contract.concurrency `
    --min-instances $contract.minInstances `
    --max-instances $contract.maxInstances `
    --timeout "$($contract.timeoutSeconds)s" `
    --set-env-vars 'PYTHONHASHSEED=0' `
    --quiet

Invoke-Gcloud run services add-iam-policy-binding $contract.service `
    --project $ProjectId `
    --region $contract.region `
    --member "serviceAccount:$invokerAccount" `
    --role roles/run.invoker `
    --quiet

$service = & $gcloud run services describe $contract.service `
    --project $ProjectId --region $contract.region --format=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $null -eq $service) {
    throw 'deployed service cannot be verified'
}
if ($service.metadata.annotations.'run.googleapis.com/ingress' -ne 'all') {
    throw 'deployed ingress does not match the authenticated Cloud Run route'
}
$deployedImage = $service.spec.template.spec.containers[0].image
if ($deployedImage -ne $ImageDigest) {
    throw 'deployed image is not the requested immutable digest'
}

$policy = & $gcloud run services get-iam-policy $contract.service `
    --project $ProjectId --region $contract.region --format=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $null -eq $policy) {
    throw 'deployed IAM policy cannot be verified'
}
$invokerMembers = @(
    $policy.bindings |
        Where-Object { $_.role -eq 'roles/run.invoker' } |
        ForEach-Object { $_.members } |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
)
$expectedMember = "serviceAccount:$invokerAccount"
if ($invokerMembers.Count -ne 1 -or $invokerMembers[0] -ne $expectedMember) {
    throw 'Cloud Run Invoker is not restricted to the kotae-api runtime identity'
}

[pscustomobject]@{
    service = $contract.service
    region = $contract.region
    uri = $service.status.url
    image = $deployedImage
    invoker = $expectedMember
    ingress = 'all-with-required-iam'
} | ConvertTo-Json -Compress
