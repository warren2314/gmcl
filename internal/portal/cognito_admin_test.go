package portal

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

type fakeCognitoIdentityProvider struct {
	getOutput    *cognitoidentityprovider.AdminGetUserOutput
	getError     error
	createOutput *cognitoidentityprovider.AdminCreateUserOutput
	createInput  *cognitoidentityprovider.AdminCreateUserInput
	updateInput  *cognitoidentityprovider.AdminUpdateUserAttributesInput
}

func (fake *fakeCognitoIdentityProvider) AdminGetUser(
	_ context.Context,
	_ *cognitoidentityprovider.AdminGetUserInput,
	_ ...func(*cognitoidentityprovider.Options),
) (*cognitoidentityprovider.AdminGetUserOutput, error) {
	return fake.getOutput, fake.getError
}

func (fake *fakeCognitoIdentityProvider) AdminCreateUser(
	_ context.Context,
	input *cognitoidentityprovider.AdminCreateUserInput,
	_ ...func(*cognitoidentityprovider.Options),
) (*cognitoidentityprovider.AdminCreateUserOutput, error) {
	fake.createInput = input
	return fake.createOutput, nil
}

func (fake *fakeCognitoIdentityProvider) AdminUpdateUserAttributes(
	_ context.Context,
	input *cognitoidentityprovider.AdminUpdateUserAttributesInput,
	_ ...func(*cognitoidentityprovider.Options),
) (*cognitoidentityprovider.AdminUpdateUserAttributesOutput, error) {
	fake.updateInput = input
	return &cognitoidentityprovider.AdminUpdateUserAttributesOutput{}, nil
}

func TestCognitoEnsureNamedUserCreatesMissingUser(t *testing.T) {
	fake := &fakeCognitoIdentityProvider{
		getError: &types.UserNotFoundException{},
		createOutput: &cognitoidentityprovider.AdminCreateUserOutput{
			User: &types.UserType{
				Username:   aws.String("official@example.test"),
				UserStatus: types.UserStatusTypeForceChangePassword,
				Attributes: []types.AttributeType{
					{Name: aws.String("email"), Value: aws.String("official@example.test")},
					{Name: aws.String("email_verified"), Value: aws.String("true")},
				},
			},
		},
	}
	manager := &cognitoUserManager{
		configuration: CognitoProvisioningConfiguration{
			Enabled:    true,
			UserPoolID: "eu-west-2_test",
		},
		client: fake,
	}

	user, err := manager.EnsureNamedUser(
		context.Background(),
		"Named Official",
		"official@example.test",
	)
	if err != nil {
		t.Fatalf("EnsureNamedUser returned error: %v", err)
	}
	if !user.Created || user.Username != "official@example.test" ||
		!user.EmailVerified {
		t.Fatalf("unexpected created user: %#v", user)
	}
	if fake.createInput == nil ||
		aws.ToString(fake.createInput.UserPoolId) != "eu-west-2_test" ||
		aws.ToString(fake.createInput.Username) != "official@example.test" {
		t.Fatalf("unexpected create input: %#v", fake.createInput)
	}
	if fake.createInput.MessageAction != "" {
		t.Fatalf("new user must use Cognito's default welcome email, got %q", fake.createInput.MessageAction)
	}
}

func TestCognitoEnsureNamedUserVerifiesExistingEmail(t *testing.T) {
	fake := &fakeCognitoIdentityProvider{
		getOutput: &cognitoidentityprovider.AdminGetUserOutput{
			Username:   aws.String("existing-id"),
			UserStatus: types.UserStatusTypeConfirmed,
			UserAttributes: []types.AttributeType{
				{Name: aws.String("email"), Value: aws.String("official@example.test")},
				{Name: aws.String("email_verified"), Value: aws.String("false")},
			},
		},
	}
	manager := &cognitoUserManager{
		configuration: CognitoProvisioningConfiguration{
			Enabled:    true,
			UserPoolID: "eu-west-2_test",
		},
		client: fake,
	}

	user, err := manager.EnsureNamedUser(
		context.Background(),
		"Named Official",
		"official@example.test",
	)
	if err != nil {
		t.Fatalf("EnsureNamedUser returned error: %v", err)
	}
	if user.Created || !user.EmailVerified {
		t.Fatalf("unexpected existing user: %#v", user)
	}
	if fake.updateInput == nil ||
		aws.ToString(fake.updateInput.Username) != "existing-id" ||
		len(fake.updateInput.UserAttributes) != 1 ||
		aws.ToString(fake.updateInput.UserAttributes[0].Value) != "true" {
		t.Fatalf("expected email verification update, got %#v", fake.updateInput)
	}
}

func TestCognitoResendInvitationUsesResendAction(t *testing.T) {
	fake := &fakeCognitoIdentityProvider{
		getOutput: &cognitoidentityprovider.AdminGetUserOutput{
			Username:   aws.String("official@example.test"),
			UserStatus: types.UserStatusTypeForceChangePassword,
			UserAttributes: []types.AttributeType{
				{Name: aws.String("email"), Value: aws.String("official@example.test")},
				{Name: aws.String("email_verified"), Value: aws.String("true")},
			},
		},
		createOutput: &cognitoidentityprovider.AdminCreateUserOutput{
			User: &types.UserType{
				Username:   aws.String("official@example.test"),
				UserStatus: types.UserStatusTypeForceChangePassword,
			},
		},
	}
	manager := &cognitoUserManager{
		configuration: CognitoProvisioningConfiguration{
			Enabled:    true,
			UserPoolID: "eu-west-2_test",
		},
		client: fake,
	}

	if _, err := manager.ResendInvitation(
		context.Background(),
		"official@example.test",
	); err != nil {
		t.Fatalf("ResendInvitation returned error: %v", err)
	}
	if fake.createInput == nil ||
		fake.createInput.MessageAction != types.MessageActionTypeResend {
		t.Fatalf("expected RESEND action, got %#v", fake.createInput)
	}
}

func TestCognitoResendDoesNothingForConfirmedUser(t *testing.T) {
	fake := &fakeCognitoIdentityProvider{
		getOutput: &cognitoidentityprovider.AdminGetUserOutput{
			Username:   aws.String("confirmed-id"),
			UserStatus: types.UserStatusTypeConfirmed,
			UserAttributes: []types.AttributeType{
				{Name: aws.String("email"), Value: aws.String("official@example.test")},
			},
		},
	}
	manager := &cognitoUserManager{
		configuration: CognitoProvisioningConfiguration{
			Enabled:    true,
			UserPoolID: "eu-west-2_test",
		},
		client: fake,
	}

	if _, err := manager.ResendInvitation(
		context.Background(),
		"confirmed-id",
	); err != nil {
		t.Fatalf("ResendInvitation returned error: %v", err)
	}
	if fake.createInput != nil {
		t.Fatal("confirmed users must not receive a temporary-password resend")
	}
}
