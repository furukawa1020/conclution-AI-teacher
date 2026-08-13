package passkey

import (
	"context"
	"errors"

	"firebase.google.com/go/v4/auth"
)

type FirebaseTokenMinter struct {
	client *auth.Client
}

func NewFirebaseTokenMinter(client *auth.Client) (*FirebaseTokenMinter, error) {
	if client == nil {
		return nil, errors.New("Firebase Auth client is required")
	}
	return &FirebaseTokenMinter{client: client}, nil
}

func (m *FirebaseTokenMinter) MintCustomToken(
	ctx context.Context,
	uid string,
	claims map[string]any,
) (string, error) {
	return m.client.CustomTokenWithClaims(ctx, uid, claims)
}

func (m *FirebaseTokenMinter) DeleteAccount(ctx context.Context, uid string) error {
	err := m.client.DeleteUser(ctx, uid)
	if auth.IsUserNotFound(err) {
		return nil
	}
	return err
}

type DevelopmentTokenMinter struct{}

func (DevelopmentTokenMinter) MintCustomToken(
	_ context.Context,
	uid string,
	_ map[string]any,
) (string, error) {
	if uid == "" {
		return "", errors.New("UID is required")
	}
	return "local-passkey-token." + uid, nil
}

func (DevelopmentTokenMinter) DeleteAccount(_ context.Context, uid string) error {
	if uid == "" {
		return errors.New("UID is required")
	}
	return nil
}
