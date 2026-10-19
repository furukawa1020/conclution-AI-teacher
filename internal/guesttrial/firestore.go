package guesttrial

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Collection contains only keyed trial metadata. Configure expiresAt TTL
// before production activation. Logical expiry is enforced even if TTL deletion
// is delayed. Application/browser Firestore rules must not grant access here.
const Collection = "guestTrialControls"

type FirestoreStore struct{ client *firestore.Client }

func NewFirestoreStore(client *firestore.Client) (*FirestoreStore, error) {
	if client == nil {
		return nil, ErrInvalid
	}
	return &FirestoreStore{client: client}, nil
}

func (s *FirestoreStore) Create(ctx context.Context, key string, r Record) error {
	if !validKey(key) || r.validate() != nil || r.Generation != 1 || r.Assisted || r.Status != Pending {
		return ErrInvalid
	}
	_, err := s.client.Collection(Collection).Doc(key).Create(ctx, recordData(r))
	if status.Code(err) == codes.AlreadyExists {
		return ErrConflict
	}
	return err
}

func (s *FirestoreStore) Read(ctx context.Context, key string, now time.Time) (Record, error) {
	if !validKey(key) {
		return Record{}, ErrInvalid
	}
	snapshot, err := s.client.Collection(Collection).Doc(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	r, err := decodeRecord(snapshot.Data())
	if err != nil {
		return Record{}, err
	}
	return current(r, now)
}

func (s *FirestoreStore) Apply(ctx context.Context, key string, generation int64, event Event, now time.Time) (Record, error) {
	if !validKey(key) {
		return Record{}, ErrInvalid
	}
	ref := s.client.Collection(Collection).Doc(key)
	var result Record
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		r, err := decodeRecord(snapshot.Data())
		if err != nil {
			return err
		}
		next, err := transition(r, generation, event, now)
		if err != nil {
			return err
		}
		if err := tx.Set(ref, recordData(next)); err != nil {
			return err
		}
		result = next
		return nil
	})
	// A locally computed result is not a committed result on transaction error.
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func recordData(r Record) map[string]any {
	return map[string]any{"version": int64(r.Version), "question": r.Question, "generation": r.Generation,
		"assisted": r.Assisted, "status": string(r.Status), "createdAt": r.CreatedAt, "expiresAt": r.ExpiresAt}
}

func decodeRecord(data map[string]any) (Record, error) {
	if len(data) != 7 {
		return Record{}, ErrInvalid
	}
	v, vok := data["version"].(int64)
	q, qok := data["question"].(string)
	g, gok := data["generation"].(int64)
	a, aok := data["assisted"].(bool)
	s, sok := data["status"].(string)
	c, cok := data["createdAt"].(time.Time)
	e, eok := data["expiresAt"].(time.Time)
	if !vok || v != 1 || !qok || !gok || !aok || !sok || !cok || !eok {
		return Record{}, ErrInvalid
	}
	r := Record{Version: 1, Question: q, Generation: g, Assisted: a, Status: Status(s), CreatedAt: c, ExpiresAt: e}
	if err := r.validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}
