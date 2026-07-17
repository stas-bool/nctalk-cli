package cli

import (
	"context"
	"errors"
)

// reactionsGetHandler — реализация `reactions get <room> <message-id>`
// (спека §6). Stub до Task 4.5b.
func reactionsGetHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("reactions get: not implemented")}
}
