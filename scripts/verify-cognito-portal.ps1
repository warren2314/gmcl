param(
    [Parameter(Mandatory = $true)]
    [string]$UserPoolId,

    [Parameter(Mandatory = $true)]
    [string]$ClientId,

    [Parameter(Mandatory = $true)]
    [string]$IssuerUrl,

    [Parameter(Mandatory = $true)]
    [string]$CallbackUrl,

    [string]$Region = "",
    [string]$Profile = "",
    [switch]$AllowCognitoDefaultEmail
)

$ErrorActionPreference = "Stop"

function Invoke-AwsJson {
    param([string[]]$Arguments)

    $awsArguments = @($Arguments)
    if ($Profile) {
        $awsArguments += @("--profile", $Profile)
    }
    $awsArguments += @("--region", $Region, "--output", "json", "--no-cli-pager")

    $output = & aws @awsArguments
    if ($LASTEXITCODE -ne 0) {
        throw "AWS CLI command failed: aws $($Arguments -join ' ')"
    }
    return $output | ConvertFrom-Json
}

function Require-Condition {
    param(
        [bool]$Condition,
        [string]$Message
    )
    if (-not $Condition) {
        throw $Message
    }
}

if (-not $Region) {
    $separator = $UserPoolId.IndexOf("_")
    Require-Condition ($separator -gt 0) "UserPoolId must begin with its AWS Region."
    $Region = $UserPoolId.Substring(0, $separator)
}

Require-Condition (
    $UserPoolId.StartsWith("$Region`_", [System.StringComparison]::Ordinal)
) "UserPoolId does not belong to Region $Region."

$issuer = [Uri]$IssuerUrl
$allowedIssuerHosts = @(
    "cognito-idp.$Region.amazonaws.com",
    "issuer-cognito-idp.$Region.amazonaws.com"
)
Require-Condition (
    $issuer.Scheme -eq "https" -and
    $allowedIssuerHosts -contains $issuer.Host -and
    $issuer.AbsolutePath -eq "/$UserPoolId" -and
    -not $issuer.Query -and
    -not $issuer.Fragment
) "IssuerUrl is not an exact Cognito issuer for $UserPoolId."

$callback = [Uri]$CallbackUrl
Require-Condition (
    $callback.Scheme -eq "https" -and
    $callback.AbsolutePath -eq "/portal/auth/callback" -and
    -not $callback.Query -and
    -not $callback.Fragment
) "CallbackUrl must be the portal HTTPS callback with no query or fragment."

$poolResponse = Invoke-AwsJson @(
    "cognito-idp", "describe-user-pool",
    "--user-pool-id", $UserPoolId
)
$pool = $poolResponse.UserPool
Require-Condition ($null -ne $pool) "Cognito did not return the requested user pool."
Require-Condition (
    $pool.AdminCreateUserConfig.AllowAdminCreateUserOnly -eq $true
) "Cognito self-registration must be disabled (AllowAdminCreateUserOnly=true)."
Require-Condition (
    $pool.MfaConfiguration -eq "ON"
) "Cognito MFA must be required (MfaConfiguration=ON)."
Require-Condition (
    $pool.DeletionProtection -eq "ACTIVE"
) "Cognito user-pool deletion protection must be active."

$firstFactors = @($pool.Policies.SignInPolicy.AllowedFirstAuthFactors)
Require-Condition (
    $firstFactors -contains "PASSWORD" -and
    $firstFactors -contains "WEB_AUTHN"
) "AllowedFirstAuthFactors must include PASSWORD and WEB_AUTHN."
Require-Condition (
    $firstFactors -notcontains "EMAIL_OTP" -and
    $firstFactors -notcontains "SMS_OTP"
) "Email and SMS OTP must not be allowed as first authentication factors."

if (-not $AllowCognitoDefaultEmail) {
    Require-Condition (
        $pool.EmailConfiguration.EmailSendingAccount -eq "DEVELOPER"
    ) "Cognito must use the existing SES identity (EmailSendingAccount=DEVELOPER)."
    Require-Condition (
        [string]$pool.EmailConfiguration.SourceArn -match
        "^arn:aws:ses:$([regex]::Escape($Region)):"
    ) "The Cognito SES source identity must be in Region $Region."
}

$mfa = Invoke-AwsJson @(
    "cognito-idp", "get-user-pool-mfa-config",
    "--user-pool-id", $UserPoolId
)
Require-Condition (
    $mfa.MfaConfiguration -eq "ON" -and
    $mfa.SoftwareTokenMfaConfiguration.Enabled -eq $true
) "Password users must have required software-token TOTP."
Require-Condition (
    $mfa.WebAuthnConfiguration.UserVerification -eq "required"
) "Passkeys must require user verification."
Require-Condition (
    -not [string]::IsNullOrWhiteSpace(
        [string]$mfa.WebAuthnConfiguration.RelyingPartyId
    )
) "Passkeys must have an explicit Cognito relying-party ID."
Require-Condition (
    $mfa.WebAuthnConfiguration.FactorConfiguration -eq
    "MULTI_FACTOR_WITH_USER_VERIFICATION"
) "Verified passkeys must be configured to satisfy the MFA requirement."

$clientResponse = Invoke-AwsJson @(
    "cognito-idp", "describe-user-pool-client",
    "--user-pool-id", $UserPoolId,
    "--client-id", $ClientId
)
$client = $clientResponse.UserPoolClient
Require-Condition ($null -ne $client) "Cognito did not return the requested app client."
Require-Condition (
    -not [string]::IsNullOrWhiteSpace([string]$client.ClientSecret)
) "The server-side portal app client must be confidential and have a client secret."
Require-Condition (
    $client.AllowedOAuthFlowsUserPoolClient -eq $true -and
    @($client.AllowedOAuthFlows) -contains "code"
) "The app client must enable the OAuth authorization-code flow."
Require-Condition (
    @($client.AllowedOAuthScopes) -contains "openid" -and
    @($client.AllowedOAuthScopes) -contains "email" -and
    @($client.AllowedOAuthScopes) -contains "profile"
) "The app client must allow the openid, email and profile scopes."
Require-Condition (
    @($client.ExplicitAuthFlows) -contains "ALLOW_USER_AUTH"
) "The app client must allow choice-based authentication (ALLOW_USER_AUTH)."
Require-Condition (
    $client.PreventUserExistenceErrors -eq "ENABLED"
) "The app client must prevent user-existence errors."
Require-Condition (
    $client.EnableTokenRevocation -eq $true
) "Cognito token revocation must be enabled for the app client."
Require-Condition (
    @($client.SupportedIdentityProviders).Count -eq 1 -and
    @($client.SupportedIdentityProviders) -contains "COGNITO"
) "The initial portal app client must use named Cognito users only."
Require-Condition (
    @($client.CallbackURLs).Count -eq 1 -and
    @($client.CallbackURLs) -contains $CallbackUrl
) "The app client must have exactly the approved portal callback URL."

Write-Host ""
Write-Host "Cognito portal authentication policy: VERIFIED"
Write-Host "User pool: $UserPoolId"
Write-Host "App client: $ClientId"
Write-Host "Region: $Region"
Write-Host ""
Write-Host "Set these application values only while this verified configuration remains unchanged:"
Write-Host "CLUB_PORTAL_OIDC_PROVIDER_PROFILE=cognito"
Write-Host "CLUB_PORTAL_OIDC_ISSUER=$IssuerUrl"
Write-Host "CLUB_PORTAL_OIDC_CLIENT_ID=$ClientId"
Write-Host "CLUB_PORTAL_OIDC_CLIENT_SECRET=<from-approved-secret-store>"
Write-Host "CLUB_PORTAL_OIDC_REDIRECT_URL=$CallbackUrl"
Write-Host "CLUB_PORTAL_COGNITO_POLICY_VERIFIED=true"
Write-Host "CLUB_PORTAL_OIDC_REQUIRED_ACR="
Write-Host "CLUB_PORTAL_OIDC_STEP_UP_ACR="
