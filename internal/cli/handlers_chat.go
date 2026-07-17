package cli

import (
	"context"
	"errors"
)

// chatShowHandler — реализация `chat show <room>` (спека §6). Stub до Task 4.3.
// Разрешение <room> (token positional / --name) делегируется в ResolveRoom
// (Task 4.2); парсинг --since/--limit/--last-message-id — тело handler-а.
func chatShowHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("chat show: not implemented")}
}

// chatSendHandler — реализация `chat send <room> --text <text>` (спека §6).
// Stub до Task 4.4.
func chatSendHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("chat send: not implemented")}
}
