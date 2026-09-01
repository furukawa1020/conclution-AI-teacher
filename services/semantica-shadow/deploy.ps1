[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z][a-z0-9-]{4,28}[a-z0-9]$')]
    [string]$ProjectId,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[a-z0-9.-]+-docker\.pkg\.dev/.+@sha256:[0-9a-f]{64}$')]
    [string]$ImageDigest,

    [string]$ContractPath = (Join-Path $PSScriptRoot 'deploy-contract.json')
)

$ErrorActionPreference = 'Stop'

function Invoke-Gcloud {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    & gcloud @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "gcloud command failed with exit code $LASTEXITCODE"
    }
}

if (-not (Get-Command gcloud -ErrorAction SilentlyContinue)) {
    throw 'gcloud is required'
}
if (-not (Test-Path -LiteralPath $ContractPath -PathType Leaf)) {
    throw 'deployment contract is missing'
}

$contract = Get-Content -LiteralPath $ContractPath -Raw | ConvertFrom-Json
if ($contract.schemaVersion -ne 1 -or
    $contract.service -ne 'kotae-semantica-shadow' -or
    $contract.callerService -ne 'kotae-api' -or
    $contract.ingress -ne 'internal' -or
    $contract.allowUnauthenticated -ne $false -or
    $contract.requiredCallerEgress -ne 'all-traffic') {
    throw 'deployment contract is invalid'
}

$activeAccount = (& gcloud auth list --filter=status:ACTIVE --format='value(account)' | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($activeAccount)) {
    throw 'an active gcloud account is required'
}

$callerDescription = & gcloud run services describe $contract.callerService `
    --project $ProjectId --region $contract.region --format=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $null -eq $callerDescription) {
    throw 'caller Cloud Run service cannot be verified'
}
$annotations = $callerDescription.spec.template.metadata.annotations
$connector = $annotations.'run.googleapis.com/vpc-access-connector'
$egress = $annotations.'run.googleapis.com/vpc-access-egress'
if ([string]::IsNullOrWhiteSpace($connector) -or $egress -ne $contract.requiredCallerEgress) {
    throw 'caller must route all traffic through a verified VPC connector before internal ingress deployment'
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
    --ingress internal `
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

$service = & gcloud run services describe $contract.service `
    --project $ProjectId --region $contract.region --format=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $null -eq $service) {
    throw 'deployed service cannot be verified'
}
if ($service.metadata.annotations.'run.googleapis.com/ingress' -ne 'internal') {
    throw 'deployed ingress is not internal'
}
$deployedImage = $service.spec.template.spec.containers[0].image
if ($deployedImage -ne $ImageDigest) {
    throw 'deployed image is not the requested immutable digest'
}

$policy = & gcloud run services get-iam-policy $contract.service `
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
    ingress = 'internal'
} | ConvertTo-Json -Compress
