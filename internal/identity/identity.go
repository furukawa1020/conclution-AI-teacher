package identity

import (
	"context"
	"errors"
	"strings"

	"firebase.google.com/go/v4/appcheck"
	"firebase.google.com/go/v4/auth"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	UID   string
	AppID string
	Roles map[string]bool
}

type Verifier interface {
	Verify(ctx context.Context, idToken, appCheckToken string) (Principal, error)
}

type FirebaseVerifier struct {
	authClient     *auth.Client
	appCheckClient *appcheck.Client
}

func NewFirebaseVerifier(authClient *auth.Client, appCheckClient *appcheck.Client) *FirebaseVerifier {
	return &FirebaseVerifier{
		authClient:     authClient,
		appCheckClient: appCheckClient,
	}
}

func (v *FirebaseVerifier) Verify(ctx context.Context, idToken, appCheckToken string) (Principal, error) {
	if strings.TrimSpace(idToken) == "" || strings.TrimSpace(appCheckToken) == "" {
		return Principal{}, ErrUnauthenticated
	}

	authToken, err := v.authClient.VerifyIDTokenAndCheckRevoked(ctx, idToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	appToken, err := v.appCheckClient.VerifyToken(appCheckToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}

	return Principal{
		UID:   authToken.UID,
		AppID: appToken.Subject,
		Roles: extractRoles(authToken.Claims),
	}, nil
}

func extractRoles(claims map[string]any) map[string]bool {
	roles := map[string]bool{"user": true}
	raw, ok := claims["roles"].([]any)
	if !ok {
		return roles
	}
	for _, role := range raw {
		if value, ok := role.(string); ok {
			switch value {
			case "admin", "evaluator", "support":
				roles[value] = true
			}
		}
	}
	return roles
}

type DevelopmentVerifier struct{}

func (DevelopmentVerifier) Verify(_ context.Context, _, _ string) (Principal, error) {
	return Principal{
		UID:   "local-development-user",
		AppID: "local-development-app",
		Roles: map[string]bool{"user": true},
	}, nil
}

