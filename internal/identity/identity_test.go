package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"firebase.google.com/go/v4/appcheck"
	"firebase.google.com/go/v4/auth"
)

type authTokenVerifierFunc func(context.Context, string) (*auth.Token, error)

func (verify authTokenVerifierFunc) VerifyIDTokenAndCheckRevoked(
	ctx context.Context,
	token string,
) (*auth.Token, error) {
	return verify(ctx, token)
}

type appCheckTokenVerifierFunc func(
	context.Context,
	string,
) (*appcheck.DecodedAppCheckToken, error)

func (verify appCheckTokenVerifierFunc) VerifyToken(
	ctx context.Context,
	token string,
) (*appcheck.DecodedAppCheckToken, error) {
	return verify(ctx, token)
}

func testFirebaseVerifier(
	authVerifier authTokenVerifier,
	appVerifier appCheckTokenVerifier,
) *FirebaseVerifier {
	return &FirebaseVerifier{
		authClient:     authVerifier,
		appCheckClient: appVerifier,
		allowedAppIDs:  map[string]struct{}{"app-123": {}},
	}
}

func verifiedGoogleToken(uid string) *auth.Token {
	return &auth.Token{
		UID: uid,
		Firebase: auth.FirebaseInfo{
			SignInProvider: "google.com",
		},
		Claims: map[string]any{"email_verified": true},
	}
}

func TestFirebaseVerifierRunsTokenChecksConcurrently(t *testing.T) {
	t.Parallel()

	authStarted := make(chan struct{})
	appStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseChecks := func() {
		releaseOnce.Do(func() { close(release) })
	}
	defer releaseChecks()

	verifier := testFirebaseVerifier(
		authTokenVerifierFunc(func(ctx context.Context, token string) (*auth.Token, error) {
			if token != "id-token" {
				return nil, errors.New("unexpected id token")
			}
			close(authStarted)
			select {
			case <-release:
				token := verifiedGoogleToken("user-123")
				token.Claims["roles"] = []any{"evaluator"}
				return token, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
		appCheckTokenVerifierFunc(func(ctx context.Context, token string) (*appcheck.DecodedAppCheckToken, error) {
			if token != "app-check-token" {
				return nil, errors.New("unexpected app check token")
			}
			close(appStarted)
			select {
			case <-release:
				return &appcheck.DecodedAppCheckToken{AppID: "app-123"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
	)

	type verifyResult struct {
		principal Principal
		err       error
	}
	result := make(chan verifyResult, 1)
	go func() {
		principal, err := verifier.Verify(context.Background(), "id-token", "app-check-token")
		result <- verifyResult{principal: principal, err: err}
	}()

	for _, check := range []struct {
		name    string
		started <-chan struct{}
	}{
		{name: "Firebase ID token", started: authStarted},
		{name: "App Check token", started: appStarted},
	} {
		select {
		case <-check.started:
		case <-time.After(time.Second):
			t.Fatalf("%s verification did not start concurrently", check.name)
		}
	}
	releaseChecks()

	select {
	case verified := <-result:
		if verified.err != nil {
			t.Fatal(verified.err)
		}
		if verified.principal.UID != "user-123" ||
			verified.principal.AppID != "app-123" ||
			verified.principal.Provider != "google.com" ||
			!verified.principal.AccountVerified ||
			!verified.principal.Roles["user"] ||
			!verified.principal.Roles["evaluator"] {
			t.Fatalf("principal = %+v", verified.principal)
		}
	case <-time.After(time.Second):
		t.Fatal("verification did not finish")
	}
}

func TestFirebaseVerifierRejectsTemporaryOrUnverifiedAccounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token *auth.Token
	}{
		{
			name: "anonymous Firebase session",
			token: &auth.Token{
				UID:      "temporary-user",
				Firebase: auth.FirebaseInfo{SignInProvider: "anonymous"},
			},
		},
		{
			name: "Google account without verified ownership claim",
			token: &auth.Token{
				UID:      "google-user",
				Firebase: auth.FirebaseInfo{SignInProvider: "google.com"},
				Claims:   map[string]any{"email_verified": false},
			},
		},
		{
			name: "custom token without server assurance",
			token: &auth.Token{
				UID:      "custom-user",
				Firebase: auth.FirebaseInfo{SignInProvider: "custom"},
				Claims:   map[string]any{},
			},
		},
		{
			name: "unsupported password provider",
			token: &auth.Token{
				UID:      "password-user",
				Firebase: auth.FirebaseInfo{SignInProvider: "password"},
				Claims:   map[string]any{"email_verified": true},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			verifier := testFirebaseVerifier(
				authTokenVerifierFunc(func(context.Context, string) (*auth.Token, error) {
					return test.token, nil
				}),
				appCheckTokenVerifierFunc(func(context.Context, string) (*appcheck.DecodedAppCheckToken, error) {
					return &appcheck.DecodedAppCheckToken{AppID: "app-123"}, nil
				}),
			)
			principal, err := verifier.Verify(
				context.Background(),
				"id-token",
				"app-check-token",
			)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("error = %v; want ErrUnauthenticated", err)
			}
			if principal.UID != "" || principal.AppID != "" ||
				principal.Provider != "" || principal.AccountVerified ||
				len(principal.Roles) != 0 {
				t.Fatalf("principal = %+v; want empty", principal)
			}
		})
	}
}

func TestFirebaseVerifierAcceptsExplicitVerifiedCustomIdentity(t *testing.T) {
	t.Parallel()

	verifier := testFirebaseVerifier(
		authTokenVerifierFunc(func(context.Context, string) (*auth.Token, error) {
			return &auth.Token{
				UID:      "passkey-user",
				Firebase: auth.FirebaseInfo{SignInProvider: "custom"},
				Claims:   map[string]any{"kotae_account_verified": true},
			}, nil
		}),
		appCheckTokenVerifierFunc(func(context.Context, string) (*appcheck.DecodedAppCheckToken, error) {
			return &appcheck.DecodedAppCheckToken{AppID: "app-123"}, nil
		}),
	)

	principal, err := verifier.Verify(
		context.Background(),
		"id-token",
		"app-check-token",
	)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Provider != "custom" || !principal.AccountVerified {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestFirebaseVerifierFailsClosedIfEitherCheckFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		authErr error
		appErr  error
	}{
		{name: "ID token rejected", authErr: errors.New("invalid id token")},
		{name: "App Check rejected", appErr: errors.New("invalid app check token")},
		{
			name:    "both rejected",
			authErr: errors.New("invalid id token"),
			appErr:  errors.New("invalid app check token"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			verifier := testFirebaseVerifier(
				authTokenVerifierFunc(func(context.Context, string) (*auth.Token, error) {
					return &auth.Token{UID: "user-123"}, test.authErr
				}),
				appCheckTokenVerifierFunc(func(context.Context, string) (*appcheck.DecodedAppCheckToken, error) {
					return &appcheck.DecodedAppCheckToken{AppID: "app-123"}, test.appErr
				}),
			)

			principal, err := verifier.Verify(context.Background(), "id-token", "app-check-token")
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("error = %v; want ErrUnauthenticated", err)
			}
			if principal.UID != "" || principal.AppID != "" || len(principal.Roles) != 0 {
				t.Fatalf("principal = %+v; want empty", principal)
			}
		})
	}
}

func TestFirebaseVerifierCancelsBothChecksWithRequest(t *testing.T) {
	t.Parallel()

	authFinished := make(chan struct{})
	appFinished := make(chan struct{})
	waitForCancellation := func(finished chan<- struct{}) func(context.Context) error {
		return func(ctx context.Context) error {
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		}
	}
	authWait := waitForCancellation(authFinished)
	appWait := waitForCancellation(appFinished)
	verifier := testFirebaseVerifier(
		authTokenVerifierFunc(func(ctx context.Context, _ string) (*auth.Token, error) {
			return nil, authWait(ctx)
		}),
		appCheckTokenVerifierFunc(func(ctx context.Context, _ string) (*appcheck.DecodedAppCheckToken, error) {
			return nil, appWait(ctx)
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := verifier.Verify(ctx, "id-token", "app-check-token")
		result <- err
	}()
	cancel()

	for _, check := range []struct {
		name     string
		finished <-chan struct{}
	}{
		{name: "Firebase ID token", finished: authFinished},
		{name: "App Check token", finished: appFinished},
	} {
		select {
		case <-check.finished:
		case <-time.After(time.Second):
			t.Fatalf("%s verification did not observe cancellation", check.name)
		}
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("error = %v; want ErrUnauthenticated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("verification goroutines did not finish")
	}
}

func TestFirebaseVerifierRejectsMissingTokensWithoutCallingClients(t *testing.T) {
	t.Parallel()

	calls := 0
	verifier := testFirebaseVerifier(
		authTokenVerifierFunc(func(context.Context, string) (*auth.Token, error) {
			calls++
			return nil, nil
		}),
		appCheckTokenVerifierFunc(func(context.Context, string) (*appcheck.DecodedAppCheckToken, error) {
			calls++
			return nil, nil
		}),
	)

	for _, tokens := range [][2]string{
		{"", "app-check-token"},
		{"id-token", ""},
		{" ", "\t"},
	} {
		if _, err := verifier.Verify(context.Background(), tokens[0], tokens[1]); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify(%q, %q) error = %v", tokens[0], tokens[1], err)
		}
	}
	if calls != 0 {
		t.Fatalf("verification client calls = %d; want 0", calls)
	}
}
