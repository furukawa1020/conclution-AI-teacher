package identity

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"firebase.google.com/go/v4/appcheck"
	"firebase.google.com/go/v4/auth"
	"golang.org/x/sync/errgroup"
)

const (
	passkeyAuthMethod            = "passkey-v1"
	passkeyAtClaim               = "kotae_passkey_at"
	passkeyTimestampClockSkew    = 30 * time.Second
	maxPasskeyTokenExchangeDelay = time.Hour
	maxExactJSONInteger          = int64(1<<53 - 1)
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	UID             string
	AppID           string
	Provider        string
	AuthMethod      string
	AuthTime        time.Time
	PasskeyAt       time.Time
	AccountVerified bool
	Roles           map[string]bool
}

type Verifier interface {
	Verify(ctx context.Context, idToken, appCheckToken string) (Principal, error)
}

type AppVerifier interface {
	VerifyApp(context.Context, string) (string, error)
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
	if provider == "anonymous" {
		if strings.TrimSpace(authToken.UID) == "" {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{UID: authToken.UID, AppID: appToken.AppID, Provider: provider, AuthMethod: "guest-v1", AuthTime: time.Unix(authToken.AuthTime, 0).UTC()}, nil
	}
	if !verifiedAccountToken(authToken, provider) {
		// Anonymous Auth proves only possession of a temporary Firebase session.
		// It must never be promoted to an assertion about the account holder.
		return Principal{}, ErrUnauthenticated
	}

	authMethod := provider
	var passkeyAt time.Time
	if provider == "custom" {
		authMethod, _ = authToken.Claims["kotae_authn"].(string)
		authMethod = strings.TrimSpace(authMethod)
		var ok bool
		passkeyAt, ok = verifiedPasskeyTimestamp(authToken, authMethod)
		if !ok {
			return Principal{}, ErrUnauthenticated
		}
	}
	return Principal{
		UID:             authToken.UID,
		AppID:           appToken.AppID,
		Provider:        provider,
		AuthMethod:      authMethod,
		AuthTime:        time.Unix(authToken.AuthTime, 0).UTC(),
		PasskeyAt:       passkeyAt,
		AccountVerified: true,
		Roles:           extractRoles(authToken.Claims),
	}, nil
}

// IsGuest identifies the unverified, App Check-bound anonymous identity that
// may cross only the dedicated voice guest boundary.
func (p Principal) IsGuest() bool {
	return p.Provider == "anonymous" && p.AuthMethod == "guest-v1" &&
		!p.AccountVerified && strings.TrimSpace(p.UID) != "" && strings.TrimSpace(p.AppID) != ""
}

func verifiedPasskeyTimestamp(token *auth.Token, authMethod string) (time.Time, bool) {
	if token == nil || authMethod != passkeyAuthMethod ||
		!validUnixTimestamp(token.AuthTime) || !validUnixTimestamp(token.IssuedAt) {
		return time.Time{}, false
	}
	seconds, ok := exactUnixSeconds(token.Claims[passkeyAtClaim])
	if !ok || !validUnixTimestamp(seconds) {
		return time.Time{}, false
	}

	skewSeconds := int64(passkeyTimestampClockSkew / time.Second)
	if seconds > token.AuthTime+skewSeconds || seconds > token.IssuedAt+skewSeconds {
		return time.Time{}, false
	}
	maxExchangeSeconds := int64((maxPasskeyTokenExchangeDelay + passkeyTimestampClockSkew) / time.Second)
	if token.AuthTime > seconds && token.AuthTime-seconds > maxExchangeSeconds {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

func exactUnixSeconds(value any) (int64, bool) {
	secondsJSON, ok := value.(float64)
	if !ok || math.IsNaN(secondsJSON) || math.IsInf(secondsJSON, 0) ||
		math.Trunc(secondsJSON) != secondsJSON || secondsJSON < 0 ||
		secondsJSON > float64(maxExactJSONInteger) {
		return 0, false
	}
	seconds := int64(secondsJSON)
	if float64(seconds) != secondsJSON {
		return 0, false
	}
	return seconds, seconds >= 0 && seconds <= maxExactJSONInteger
}

func validUnixTimestamp(seconds int64) bool {
	return seconds > 0 && seconds <= maxExactJSONInteger
}

func (v *FirebaseVerifier) VerifyApp(ctx context.Context, appCheckToken string) (string, error) {
	if strings.TrimSpace(appCheckToken) == "" {
		return "", ErrUnauthenticated
	}
	token, err := v.appCheckClient.VerifyToken(ctx, appCheckToken)
	if err != nil || token == nil {
		return "", ErrUnauthenticated
	}
	if _, allowed := v.allowedAppIDs[token.AppID]; !allowed {
		return "", ErrUnauthenticated
	}
	return token.AppID, nil
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
		// Issued only after this service has verified a WebAuthn ceremony and
		// exchanged its short-lived custom token through Firebase Auth. Minting
		// an ordinary Firebase custom token is insufficient:
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
		AuthMethod:      "development",
		AuthTime:        time.Now().UTC(),
		AccountVerified: true,
		Roles:           map[string]bool{"user": true},
	}, nil
}

func (DevelopmentVerifier) VerifyApp(_ context.Context, _ string) (string, error) {
	return "local-development-app", nil
}
