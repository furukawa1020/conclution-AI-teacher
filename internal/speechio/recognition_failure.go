package speechio

import (
	"context"
	"errors"

	"google.golang.org/grpc/status"
)

// RecognitionFailureClass exposes only a finite provider status category.
// Never log the underlying error: provider messages can contain user content.
func RecognitionFailureClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if providerStatus, ok := status.FromError(err); ok {
		return "grpc_" + providerStatus.Code().String()
	}
	return "local_failure"
}
