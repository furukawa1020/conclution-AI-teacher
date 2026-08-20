package passkey

import (
	"context"
)

// AccountDataCleaner is the narrow boundary required to tombstone persisted
// conversation memory before the Firebase account is removed.
type AccountDataCleaner interface {
	DisableAndDelete(context.Context, string) error
}
