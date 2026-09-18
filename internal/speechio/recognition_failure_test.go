package speechio

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecognitionFailureClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "none"},
		{"cancelled", fmt.Errorf("wrapped: %w", context.Canceled), "cancelled"},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "deadline"},
		{"invalid argument", fmt.Errorf("wrapped: %w", status.Error(codes.InvalidArgument, "private transcript")), "grpc_InvalidArgument"},
		{"permission denied", status.Error(codes.PermissionDenied, "private detail"), "grpc_PermissionDenied"},
		{"local", errors.New("private detail"), "local_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RecognitionFailureClass(test.err); got != test.want {
				t.Fatalf("RecognitionFailureClass() = %q, want %q", got, test.want)
			}
		})
	}
}
