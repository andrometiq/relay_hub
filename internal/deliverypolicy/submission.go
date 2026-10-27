package deliverypolicy

import "context"

type submissionKey struct{}

// WithSubmission records the durable boundary immediately before provider I/O.
func WithSubmission(ctx context.Context, mark func() error) context.Context {
	return context.WithValue(ctx, submissionKey{}, mark)
}

func BeforeSubmission(ctx context.Context) error {
	if mark, ok := ctx.Value(submissionKey{}).(func() error); ok {
		return mark()
	}
	return nil
}
