package receipt

import "context"

// Publisher sends a completed, redacted receipt to an external audit sink.
type Publisher interface {
	Publish(context.Context, []byte) error
}

type publicationTarget struct {
	context   context.Context
	publisher Publisher
}

func newPublicationTarget(ctx context.Context, publisher Publisher) *publicationTarget {
	if publisher == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &publicationTarget{context: ctx, publisher: publisher}
}

func publish(target *publicationTarget, payload []byte) error {
	if target == nil {
		return nil
	}
	return target.publisher.Publish(target.context, payload)
}
