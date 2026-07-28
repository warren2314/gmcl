package portal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

type CognitoProvisioningConfiguration struct {
	Enabled    bool
	Region     string
	UserPoolID string
	Reason     string
}

type CognitoNamedUser struct {
	Username      string
	Email         string
	UserStatus    string
	EmailVerified bool
	Created       bool
}

type CognitoUserManager interface {
	Configuration() CognitoProvisioningConfiguration
	EnsureNamedUser(
		ctx context.Context,
		displayName string,
		email string,
	) (CognitoNamedUser, error)
	ResendInvitation(
		ctx context.Context,
		username string,
	) (CognitoNamedUser, error)
}

type cognitoIdentityProviderAPI interface {
	AdminGetUser(
		context.Context,
		*cognitoidentityprovider.AdminGetUserInput,
		...func(*cognitoidentityprovider.Options),
	) (*cognitoidentityprovider.AdminGetUserOutput, error)
	AdminCreateUser(
		context.Context,
		*cognitoidentityprovider.AdminCreateUserInput,
		...func(*cognitoidentityprovider.Options),
	) (*cognitoidentityprovider.AdminCreateUserOutput, error)
	AdminUpdateUserAttributes(
		context.Context,
		*cognitoidentityprovider.AdminUpdateUserAttributesInput,
		...func(*cognitoidentityprovider.Options),
	) (*cognitoidentityprovider.AdminUpdateUserAttributesOutput, error)
}

type cognitoUserManager struct {
	configuration CognitoProvisioningConfiguration
	client        cognitoIdentityProviderAPI
}

func NewCognitoUserManagerFromEnv(
	ctx context.Context,
	oidcConfig OIDCConfig,
) (CognitoUserManager, error) {
	configuration := cognitoProvisioningConfiguration(oidcConfig)
	manager := &cognitoUserManager{configuration: configuration}
	if !configuration.Enabled {
		return manager, nil
	}
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(configuration.Region),
	}
	accessKeyID := strings.TrimSpace(
		os.Getenv("CLUB_PORTAL_COGNITO_AWS_ACCESS_KEY_ID"),
	)
	secretAccessKey := strings.TrimSpace(
		os.Getenv("CLUB_PORTAL_COGNITO_AWS_SECRET_ACCESS_KEY"),
	)
	sessionToken := strings.TrimSpace(
		os.Getenv("CLUB_PORTAL_COGNITO_AWS_SESSION_TOKEN"),
	)
	if accessKeyID != "" || secretAccessKey != "" {
		if accessKeyID == "" || secretAccessKey == "" {
			return nil, fmt.Errorf(
				"both Cognito provisioning AWS access key and secret are required",
			)
		}
		options = append(
			options,
			awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(
					accessKeyID,
					secretAccessKey,
					sessionToken,
				),
			),
		)
	} else if !envBool("CLUB_PORTAL_COGNITO_USE_DEFAULT_CREDENTIAL_CHAIN") {
		manager.configuration.Enabled = false
		manager.configuration.Reason =
			"dedicated Cognito provisioning credentials are not configured"
		return manager, nil
	}
	awsConfiguration, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load Cognito provisioning credentials: %w", err)
	}
	manager.client = cognitoidentityprovider.NewFromConfig(awsConfiguration)
	return manager, nil
}

func cognitoProvisioningConfiguration(
	oidcConfig OIDCConfig,
) CognitoProvisioningConfiguration {
	configuration := CognitoProvisioningConfiguration{}
	if !envBool("CLUB_PORTAL_COGNITO_PROVISIONING_ENABLED") {
		configuration.Reason = "automatic Cognito provisioning is disabled"
		return configuration
	}
	if oidcConfig.normalizedProviderProfile() != OIDCProviderCognito {
		configuration.Reason = "the portal identity provider is not Cognito"
		return configuration
	}
	issuer, err := url.Parse(oidcConfig.IssuerURL)
	if err != nil || !validCognitoIssuer(issuer) {
		configuration.Reason = "the Cognito issuer configuration is invalid"
		return configuration
	}
	hostParts := strings.Split(strings.ToLower(issuer.Hostname()), ".")
	configuration.Region = hostParts[1]
	configuration.UserPoolID = strings.TrimPrefix(issuer.EscapedPath(), "/")
	configuration.Enabled = true
	return configuration
}

func (manager *cognitoUserManager) Configuration() CognitoProvisioningConfiguration {
	if manager == nil {
		return CognitoProvisioningConfiguration{
			Reason: "Cognito provisioning manager is unavailable",
		}
	}
	return manager.configuration
}

func (manager *cognitoUserManager) EnsureNamedUser(
	ctx context.Context,
	displayName string,
	emailAddress string,
) (CognitoNamedUser, error) {
	if manager == nil || !manager.configuration.Enabled || manager.client == nil {
		return CognitoNamedUser{}, fmt.Errorf(
			"Cognito provisioning is not configured",
		)
	}
	emailAddress, err := normalizeOnboardingEmail(emailAddress)
	if err != nil {
		return CognitoNamedUser{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || len(displayName) > 200 {
		return CognitoNamedUser{}, fmt.Errorf("named official display name is invalid")
	}
	user, err := manager.getUser(ctx, emailAddress)
	if err == nil {
		if !strings.EqualFold(strings.TrimSpace(user.Email), emailAddress) {
			return CognitoNamedUser{}, fmt.Errorf(
				"Cognito returned a different email for the named user",
			)
		}
		if !user.EmailVerified {
			if _, updateErr := manager.client.AdminUpdateUserAttributes(
				ctx,
				&cognitoidentityprovider.AdminUpdateUserAttributesInput{
					UserPoolId: aws.String(manager.configuration.UserPoolID),
					Username:   aws.String(user.Username),
					UserAttributes: []types.AttributeType{
						{
							Name:  aws.String("email_verified"),
							Value: aws.String("true"),
						},
					},
				},
			); updateErr != nil {
				return CognitoNamedUser{}, fmt.Errorf(
					"mark Cognito email verified: %w",
					updateErr,
				)
			}
			user.EmailVerified = true
		}
		return user, nil
	}
	var notFound *types.UserNotFoundException
	if !errors.As(err, &notFound) {
		return CognitoNamedUser{}, err
	}
	output, err := manager.client.AdminCreateUser(
		ctx,
		&cognitoidentityprovider.AdminCreateUserInput{
			UserPoolId: aws.String(manager.configuration.UserPoolID),
			Username:   aws.String(emailAddress),
			DesiredDeliveryMediums: []types.DeliveryMediumType{
				types.DeliveryMediumTypeEmail,
			},
			UserAttributes: []types.AttributeType{
				{Name: aws.String("email"), Value: aws.String(emailAddress)},
				{Name: aws.String("email_verified"), Value: aws.String("true")},
				{Name: aws.String("name"), Value: aws.String(displayName)},
			},
		},
	)
	if err != nil {
		var exists *types.UsernameExistsException
		if errors.As(err, &exists) {
			user, getErr := manager.getUser(ctx, emailAddress)
			if getErr != nil {
				return CognitoNamedUser{}, getErr
			}
			return user, nil
		}
		return CognitoNamedUser{}, fmt.Errorf("create Cognito named user: %w", err)
	}
	if output.User == nil {
		return CognitoNamedUser{}, fmt.Errorf(
			"Cognito did not return the created named user",
		)
	}
	return cognitoNamedUserFromType(*output.User, true), nil
}

func (manager *cognitoUserManager) ResendInvitation(
	ctx context.Context,
	username string,
) (CognitoNamedUser, error) {
	if manager == nil || !manager.configuration.Enabled || manager.client == nil {
		return CognitoNamedUser{}, fmt.Errorf(
			"Cognito provisioning is not configured",
		)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return CognitoNamedUser{}, fmt.Errorf("Cognito username is missing")
	}
	current, err := manager.getUser(ctx, username)
	if err != nil {
		return CognitoNamedUser{}, err
	}
	if strings.EqualFold(current.UserStatus, string(types.UserStatusTypeConfirmed)) {
		return current, nil
	}
	output, err := manager.client.AdminCreateUser(
		ctx,
		&cognitoidentityprovider.AdminCreateUserInput{
			UserPoolId:    aws.String(manager.configuration.UserPoolID),
			Username:      aws.String(current.Username),
			MessageAction: types.MessageActionTypeResend,
			DesiredDeliveryMediums: []types.DeliveryMediumType{
				types.DeliveryMediumTypeEmail,
			},
		},
	)
	if err != nil {
		return CognitoNamedUser{}, fmt.Errorf(
			"resend Cognito named-user invitation: %w",
			err,
		)
	}
	if output.User == nil {
		return CognitoNamedUser{}, fmt.Errorf(
			"Cognito did not return the resent named user",
		)
	}
	user := cognitoNamedUserFromType(*output.User, false)
	user.EmailVerified = current.EmailVerified
	return user, nil
}

func (manager *cognitoUserManager) getUser(
	ctx context.Context,
	username string,
) (CognitoNamedUser, error) {
	output, err := manager.client.AdminGetUser(
		ctx,
		&cognitoidentityprovider.AdminGetUserInput{
			UserPoolId: aws.String(manager.configuration.UserPoolID),
			Username:   aws.String(strings.TrimSpace(username)),
		},
	)
	if err != nil {
		var notFound *types.UserNotFoundException
		if errors.As(err, &notFound) {
			return CognitoNamedUser{}, err
		}
		return CognitoNamedUser{}, fmt.Errorf("check Cognito named user: %w", err)
	}
	user := CognitoNamedUser{
		Username:   aws.ToString(output.Username),
		UserStatus: string(output.UserStatus),
	}
	for _, attribute := range output.UserAttributes {
		switch aws.ToString(attribute.Name) {
		case "email":
			user.Email = strings.ToLower(strings.TrimSpace(
				aws.ToString(attribute.Value),
			))
		case "email_verified":
			user.EmailVerified = strings.EqualFold(
				aws.ToString(attribute.Value),
				"true",
			)
		}
	}
	return user, nil
}

func cognitoNamedUserFromType(
	user types.UserType,
	created bool,
) CognitoNamedUser {
	result := CognitoNamedUser{
		Username:   aws.ToString(user.Username),
		UserStatus: string(user.UserStatus),
		Created:    created,
	}
	for _, attribute := range user.Attributes {
		switch aws.ToString(attribute.Name) {
		case "email":
			result.Email = strings.ToLower(strings.TrimSpace(
				aws.ToString(attribute.Value),
			))
		case "email_verified":
			result.EmailVerified = strings.EqualFold(
				aws.ToString(attribute.Value),
				"true",
			)
		}
	}
	return result
}
