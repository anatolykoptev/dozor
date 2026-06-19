package toolutil

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// RecoverLog recovers from panics in goroutines and logs the stack trace.
// Usage: defer toolutil.RecoverLog("context description")
func RecoverLog(context string) {
	if r := recover(); r != nil {
		slog.Error("panic recovered",
			slog.String("context", context),
			slog.String("panic", fmt.Sprint(r)),
			slog.String("stack", string(debug.Stack())),
		)
	}
}
