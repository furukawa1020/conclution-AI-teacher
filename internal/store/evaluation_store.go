package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/furukawa1020/conclution-ai-teacher/internal/contracts"
)

type EvaluationStore interface {
	Save(ctx context.Context, uid, requestID string, input contracts.EvaluationInput, result contracts.EvaluationResult) (string, error)
}

type FirestoreEvaluationStore struct {
	client *firestore.Client
}

func NewFirestoreEvaluationStore(client *firestore.Client) *FirestoreEvaluationStore {
	return &FirestoreEvaluationStore{client: client}
}

func (s *FirestoreEvaluationStore) Save(
	ctx context.Context,
	uid string,
	requestID string,
	input contracts.EvaluationInput,
	result contracts.EvaluationResult,
) (string, error) {
	attemptID, err := randomID()
	if err != nil {
		return "", fmt.Errorf("create attempt id: %w", err)
	}

	answerDigest := sha256.Sum256([]byte(input.Answer))
	_, err = s.client.Collection("evaluations").Doc(attemptID).Create(ctx, map[string]any{
		"uid":             uid,
		"requestId":       requestID,
		"mode":            input.Mode,
		"questionRunes":   len([]rune(input.Question)),
		"answerRunes":     len([]rune(input.Answer)),
		"answerSha256":    hex.EncodeToString(answerDigest[:]),
		"result":          result,
		"rawTextStored":   false,
		"storageMode":     "metrics_only",
		"createdAt":       firestore.ServerTimestamp,
		"schemaVersion":   1,
		"retentionPolicy": "user_controlled",
	})
	if err != nil {
		return "", fmt.Errorf("save evaluation: %w", err)
	}
	return attemptID, nil
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

type MemoryEvaluationStore struct{}

func (MemoryEvaluationStore) Save(
	_ context.Context,
	_ string,
	_ string,
	_ contracts.EvaluationInput,
	_ contracts.EvaluationResult,
) (string, error) {
	return fmt.Sprintf("local-%d", time.Now().UnixNano()), nil
}

