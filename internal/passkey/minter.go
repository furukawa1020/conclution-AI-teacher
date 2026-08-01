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
