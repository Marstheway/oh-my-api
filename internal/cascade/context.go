package cascade

import "context"

type ctxKey int

const (
	ctxKeyOrigin ctxKey = iota
	ctxKeyHop
)

// WithCascadeOrigin marks the context as a spoke-side cascade job execution.
func WithCascadeOrigin(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyOrigin, true)
}

// CascadeOrigin reports whether the context is executing a cascade job on the spoke.
func CascadeOrigin(ctx context.Context) bool {
	v, ok := ctx.Value(ctxKeyOrigin).(bool)
	return ok && v
}

// WithCascadeHop stores the cascade hop header value for anti-loop materialization.
func WithCascadeHop(ctx context.Context, hop string) context.Context {
	if hop == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyHop, hop)
}

// CascadeHop returns the hop value when present.
func CascadeHop(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyHop).(string)
	return v, ok && v != ""
}
