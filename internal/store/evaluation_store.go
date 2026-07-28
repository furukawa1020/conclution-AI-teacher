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

	storedResult := resultWithoutAnswerText(result)
	userDigest := sha256.Sum256([]byte(uid))
	userDocumentID := hex.EncodeToString(userDigest[:])
	_, err = s.client.
		Collection("users").
		Doc(userDocumentID).
		Collection("evaluations").
		Doc(attemptID).
		Create(ctx, map[string]any{
			"requestId":             requestID,
			"mode":                  input.Mode,
			"questionRunes":         len([]rune(input.Question)),
			"answerRunes":           len([]rune(input.Answer)),
			"result":                storedResult,
			"rawQuestionStored":     false,
			"rawAnswerStored":       false,
			"containsAnswerExcerpt": false,
			"storageMode":           "evaluation_without_answer_text",
			"createdAt":             firestore.ServerTimestamp,
			"schemaVersion":         1,
			"retentionPolicy":       "user_controlled",
		})
	if err != nil {
		return "", fmt.Errorf("save evaluation: %w", err)
	}
	return attemptID, nil
}

// resultWithoutAnswerText keeps the structured coaching result while removing
// fields that can contain verbatim user input. The complete result is returned
// to the active browser session, but Firestore never receives those excerpts.
func resultWithoutAnswerText(result contracts.EvaluationResult) contracts.EvaluationResult {
	result.EstimatedConclusion = ""
	result.EvidenceExcerpt = ""
	return result
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
