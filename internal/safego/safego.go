package safego

import (
    "log/slog"
    "runtime/debug"
)

func Go(name string, fn func()) {
    go func() {
        defer func() {
            if r := recover(); r != nil {
                slog.Error("goroutine panic recovered",
                    "goroutine", name,
                    "panic", r,
                    "stack", string(debug.Stack()),
                )
            }
        }()
        fn()
    }()
}
