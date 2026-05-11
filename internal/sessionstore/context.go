package sessionstore

import "context"

type contextKey string

const sessionIDKey contextKey = "support-session-id"

func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}
