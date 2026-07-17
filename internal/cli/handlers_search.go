package cli

import (
	"context"
	"errors"
)

// searchHandler — реализация `search <term>` (спека §6): глобальный поиск
// сообщений. Special-case роутинга: для `search` НЕТ verb-уровня, поэтому
// handler получает args как `term` + флаги (--from/--limit/--all) целиком.
// Stub до Task 4.5c.
func searchHandler(_ context.Context, _ Deps, _ []string, _ bool) ExitError {
	return ExitError{Code: ExitGeneric, Err: errors.New("search: not implemented")}
}
