package identity

import (
	"context"
	"errors"
	"strings"

	"firebase.google.com/go/v4/appcheck"
	"firebase.google.com/go/v4/auth"
	"golang.org/x/sync/errgroup"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	UID             string
	AppID           string
	Provider        string
	AccountVerified bool
	Roles           map[string]bool
}

type Verifier interface {
	Verify(ctx context.Context, idToken, appCheckToken string) (Principal, error)
}

type authTokenVerifier interface {
	VerifyIDTokenAndCheckRevoked(context.Context, string) (*auth.Token, error)
}

type appCheckTokenVerifier interface {
	VerifyToken(context.Context, string) (*appcheck.DecodedAppCheckToken, error)
}

type firebaseAppCheckTokenVerifier struct {
	client *appcheck.Client
}

func (v firebaseAppCheckTokenVerifier) VerifyToken(
	ctx context.Context,
	token string,
) (*appcheck.DecodedAppCheckToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, err := v.client.VerifyToken(token)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decoded, nil
}

type FirebaseVerifier struct {
	authClient     authTokenVerifier
	appCheckClient appCheckTokenVerifier
	allowedAppIDs  map[string]struct{}
}

func NewFirebaseVerifier(authClient *auth.Client, appCheckClient *appcheck.Client, allowedAppIDs []string) *FirebaseVerifier {
	allowed := make(map[string]struct{}, len(allowedAppIDs))
	for _, appID := range allowedAppIDs {
		if appID = strings.TrimSpace(appID); appID != "" {
			allowed[appID] = struct{}{}
		}
	}
	return &FirebaseVerifier{
		authClient:     authClient,
		appCheckClient: firebaseAppCheckTokenVerifier{client: appCheckClient},
		allowedAppIDs:  allowed,
	}
}

func (v *FirebaseVerifier) Verify(ctx context.Context, idToken, appCheckToken string) (Principal, error) {
	if strings.TrimSpace(idToken) == "" || strings.TrimSpace(appCheckToken) == "" {
		return Principal{}, ErrUnauthenticated
	}

	var (
		authToken *auth.Token
		appToken  *appcheck.DecodedAppCheckToken
	)
	group, verifyCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		token, err := v.authClient.VerifyIDTokenAndCheckRevoked(verifyCtx, idToken)
		if err != nil {
			return ErrUnauthenticated
		}
		authToken = token
		return nil
	})
	group.Go(func() error {
		token, err := v.appCheckClient.VerifyToken(verifyCtx, appCheckToken)
		if err != nil {
			return ErrUnauthenticated
		}
		appToken = token
		return nil
	})
	if err := group.Wait(); err != nil || authToken == nil || appToken == nil {
		return Principal{}, ErrUnauthenticated
	}
	if _, allowed := v.allowedAppIDs[appToken.AppID]; !allowed {
		return Principal{}, ErrUnauthenticated
	}
	provider := strings.TrimSpace(authToken.Firebase.SignInProvider)
	if !verifiedAccountToken(authToken, provider) {
		// Anonymous Auth proves only possession of a temporary Firebase session.
		// It must never be promoted to an assertion about the account holder.
		return Principal{}, ErrUnauthenticated
	}

	return Principal{
		UID:             authToken.UID,
		AppID:           appToken.AppID,
		Provider:        provider,
		AccountVerified: true,
		Roles:           extractRoles(authToken.Claims),
	}, nil
}

func verifiedAccountToken(token *auth.Token, provider string) bool {
	if token == nil || strings.TrimSpace(token.UID) == "" {
		return false
	}
	switch provider {
	case "google.com":
		// Google is authoritative for Google-hosted email identities. Keep the
		// email itself out of Principal and application logs; only the boolean
		// ownership assertion crosses this boundary.
		verified, _ := token.Claims["email_verified"].(bool)
		return verified
	case "custom":
		// Reserved for a future server-verified WebAuthn or external identity
		// ceremony. Minting an ordinary Firebase custom token is insufficient:
		// the issuer must add this explicit, namespaced assurance claim.
		verified, _ := token.Claims["kotae_account_verified"].(bool)
		return verified
	default:
		return false
	}
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
		UID:             "local-development-user",
		AppID:           "local-development-app",
		Provider:        "development",
		AccountVerified: true,
		Roles:           map[string]bool{"user": true},
	}, nil
}
