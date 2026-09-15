package cascade

import "context"

// JobResultSink receives mapped cascade job results written back to the hub session.
type JobResultSink interface {
	SendResult(chunk string, final bool, statusCode int) error
	SendError(message string) error
}

// JobHandler runs a spoke-side cascade job. Injected when constructing Spoke.
type JobHandler func(ctx context.Context, frame Frame, sink JobResultSink) error
