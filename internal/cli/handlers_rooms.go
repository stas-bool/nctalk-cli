package cli

import (
	"context"
	"errors"
)

// roomsListHandler — реализация `rooms list` (спека §6). Stub до Task 4.5a;
// всегда возвращает ExitGeneric. Сигнатура фиксирована и НЕ будет меняться —
// downstream-задача заменит только тело.
func roomsListHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("rooms list: not implemented")}
}

// roomsFindHandler — реализация `rooms find <query>` (спека §6). Stub до Task 4.5a.
func roomsFindHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("rooms find: not implemented")}
}

// roomsSearchHandler — реализация `rooms search <term>` (спека §6, через OCS
// search-conversations). Stub до Task 4.5a.
func roomsSearchHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("rooms search: not implemented")}
}
