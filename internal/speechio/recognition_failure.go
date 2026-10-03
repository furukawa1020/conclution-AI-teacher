package speechio

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The failed direction cancels its peer. Prefer the original non-cancellation
// error so cleanup cannot relabel a provider failure as a user cancellation.
// The caller checks the parent's context before applying this selection.
func recognitionDuplexError(sendErr, receiveErr error) error {
	for _, err := range []error{sendErr, receiveErr} {
		if err != nil && !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
			return err
		}
	}
	return errors.Join(sendErr, receiveErr)
}

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
