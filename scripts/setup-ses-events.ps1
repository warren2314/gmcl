param(
    [string]$Region = "eu-west-2",
    [string]$Domain = "gmcl.co.uk",
    [string]$TopicName = "gmcl-ses-events",
    [string]$ConfigurationSetName = "gmcl-captain-reports",
    [string]$WebhookUrl = "",
    [string]$Profile = "",
    [switch]$PlanOnly
)

$ErrorActionPreference = "Stop"

function Aws-Args {
    $args = @("--region", $Region)
    if (-not [string]::IsNullOrWhiteSpace($Profile)) {
        $args += @("--profile", $Profile)
    }
    return $args
}

function Invoke-Aws {
    param([string[]]$Arguments)

    $fullArgs = @()
    $fullArgs += Aws-Args
    $fullArgs += $Arguments

    Write-Host ("aws " + ($fullArgs -join " "))
    if ($PlanOnly) {
        return $null
    }

    $output = & aws @fullArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw ($output | Out-String)
    }
    return $output
}

function Invoke-AwsJson {
    param([string[]]$Arguments)

    $output = Invoke-Aws ($Arguments + @("--output", "json"))
    if ($PlanOnly -or $null -eq $output) {
        return $null
    }
    $text = ($output | Out-String).Trim()
    if ([string]::IsNullOrWhiteSpace($text)) {
        return $null
    }
    return $text | ConvertFrom-Json
}

if ([string]::IsNullOrWhiteSpace($WebhookUrl)) {
    throw "WebhookUrl is required, e.g. https://gmcl.co.uk/webhooks/aws/ses?token=YOUR_TOKEN"
}

Write-Host ""
Write-Host "SES/SNS setup"
Write-Host "Region:             $Region"
Write-Host "Domain:             $Domain"
Write-Host "SNS topic:          $TopicName"
Write-Host "Configuration set:  $ConfigurationSetName"
Write-Host "Webhook:            $WebhookUrl"
if ($Profile) {
    Write-Host "AWS profile:        $Profile"
}
if ($PlanOnly) {
    Write-Host "Mode:               plan only"
}
Write-Host ""

Write-Host "1. Creating or checking SES domain identity..."
$identity = Invoke-AwsJson @("sesv2", "create-email-identity", "--email-identity", $Domain)

if ($identity -and $identity.DkimAttributes -and $identity.DkimAttributes.Tokens) {
    Write-Host ""
    Write-Host "Add these DKIM CNAME records to DNS, then wait for SES verification:"
    foreach ($token in $identity.DkimAttributes.Tokens) {
        Write-Host ("{0}._domainkey.{1} CNAME {0}.dkim.amazonses.com" -f $token, $Domain)
    }
}

Write-Host ""
Write-Host "2. Creating SNS topic..."
$topic = Invoke-AwsJson @("sns", "create-topic", "--name", $TopicName)
$topicArn = $null
if ($topic -and $topic.TopicArn) {
    $topicArn = $topic.TopicArn
} else {
    $account = Invoke-AwsJson @("sts", "get-caller-identity")
    if ($account -and $account.Account) {
        $topicArn = "arn:aws:sns:${Region}:$($account.Account):$TopicName"
    } else {
        $topicArn = "arn:aws:sns:${Region}:<account-id>:$TopicName"
    }
}
Write-Host "Topic ARN: $topicArn"

Write-Host ""
Write-Host "3. Subscribing app webhook to SNS topic..."
Invoke-Aws @(
    "sns", "subscribe",
    "--topic-arn", $topicArn,
    "--protocol", "https",
    "--notification-endpoint", $WebhookUrl,
    "--return-subscription-arn"
) | Out-Null
Write-Host "SNS will send a confirmation request to the app webhook."
Write-Host "Set SES_SNS_AUTO_CONFIRM=1 temporarily if you want the app to auto-confirm it."

Write-Host ""
Write-Host "4. Creating SES configuration set..."
try {
    Invoke-Aws @("sesv2", "create-configuration-set", "--configuration-set-name", $ConfigurationSetName) | Out-Null
} catch {
    if ($_.Exception.Message -match "AlreadyExists|ConfigurationSetAlreadyExists") {
        Write-Host "Configuration set already exists."
    } else {
        throw
    }
}

Write-Host ""
Write-Host "5. Creating SES event destination to SNS..."
$eventDestination = @{
    Enabled = $true
    MatchingEventTypes = @(
        "SEND",
        "REJECT",
        "BOUNCE",
        "COMPLAINT",
        "DELIVERY",
        "DELIVERY_DELAY",
        "OPEN",
        "CLICK",
        "RENDERING_FAILURE"
    )
    SnsDestination = @{
        TopicArn = $topicArn
    }
} | ConvertTo-Json -Compress -Depth 5

try {
    Invoke-Aws @(
        "sesv2", "create-configuration-set-event-destination",
        "--configuration-set-name", $ConfigurationSetName,
        "--event-destination-name", "sns-events",
        "--event-destination", $eventDestination
    ) | Out-Null
} catch {
    if ($_.Exception.Message -match "AlreadyExists|EventDestinationAlreadyExists") {
        Write-Host "Event destination already exists."
    } else {
        throw
    }
}

Write-Host ""
Write-Host "Done."
Write-Host ""
Write-Host "Production .env should include:"
Write-Host "SMTP_HOST=email-smtp.$Region.amazonaws.com"
Write-Host "SMTP_PORT=587"
Write-Host "SMTP_FROM=webmaster@$Domain"
Write-Host "SES_CONFIGURATION_SET=$ConfigurationSetName"
Write-Host "SES_SNS_WEBHOOK_TOKEN=<same token used in WebhookUrl>"
