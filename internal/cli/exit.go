// cli/exit.go — тонкий re-export контракта exit-кодов из пакета internal/exit
// (спека 2026-07-19 §4, Task 1.2). Все типы/константы/хелперы перенесены в
// internal/exit, чтобы cmd/nctalk-call и cmd/nctalk-talk могли использовать тот
// же контракт без зависимости от cli-роутера. Здесь оставлены алиасы и тонкие
// обёртки для backcompat: handler-ы (handlers_chat.go, handlers_reactions.go,
// handlers_search.go, handlers_rooms.go) и тесты продолжают ссылаться на
// cli.ExitError, cli.ExitOK и т.д. без правок.
//
//_layer direction: cli → exit (НЕ наоборот, иначе цикл импортов).
package cli

import "github.com/stas-bool/nctalk-cli/internal/exit"

// ExitError — алиас для exit.ExitError (см. комментарий выше). Аlias в Go —
// это ТОТ ЖЕ тип (не wrapper), поэтому существующий код вида
// `ExitError{Code: ExitGeneric, Err: err}` и `var ee ExitError; errors.As(...)`
// работает идентично.
type ExitError = exit.ExitError

// Exit-коды по спеке §7 — реэкспорт констант из internal/exit.
//
//	0 — успех
//	1 — общая ошибка (сеть/авторизация)
//	2 — not found (только для разрешения <room>: 0 совпадений по имени)
//	3 — ambiguous (>1 совпадение по имени, не угадываем)
//
// Константа для кода 1 названа ExitGeneric (не ExitError) — имя ExitError
// занято типом выше; подробности в internal/exit/exit.go.
const (
	ExitOK        = exit.ExitOK
	ExitGeneric   = exit.ExitGeneric
	ExitNotFound  = exit.ExitNotFound
	ExitAmbiguous = exit.ExitAmbiguous
)

// Exit — тонкая обёртка над exit.Exit для backcompat handler-ов.
func Exit(code int, err error) ExitError { return exit.Exit(code, err) }

// exitFromClientErr — тонкая обёртка над exit.FromClientErr. Делегирует в
// общий пакет internal/exit, сохраняя signatures и behavior для существующих
// вызовов из handler-ов (chat/reactions/search).
//
// Контракт маппинга (спека §7/§9):
//   - nil → ExitError{ExitOK, nil};
//   - *transport.OCSError{Code:404} → ExitNotFound (2);
//   - прочие ошибки (сеть, 401/403/400/5xx, decode-ошибки) → ExitGeneric (1).
//
// Возвращаемое значение всегда имеет конкретный тип exit.ExitError — type
// assertion безопасно (FromClientErr никогда не возвращает nil interface при
// не-nil аргументе, а при nil возвращает ExitError{ExitOK}).
func exitFromClientErr(err error) ExitError {
	return exit.FromClientErr(err).(ExitError)
}
