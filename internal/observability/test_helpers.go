package observability

import (
    "context"
    "errors"
)

var errTest = errors.New("test error")

func nilContext() context.Context {
    return context.Background()
}
