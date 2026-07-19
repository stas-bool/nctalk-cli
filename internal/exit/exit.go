// Package exit — единый для всех бинарников (cmd/nctalk, cmd/nctalk-call,
// cmd/nctalk-talk) контракт exit-кодов и тонкая обёртка ошибки вокруг кода.
//
// Вынесен из internal/cli (Task 1.2 спеки 2026-07-19 §4): cmd/nctalk-call и
// cmd/nctalk-talk не должны зависеть от cli-роутера и render, но им нужен тот же
// набор кодов (0/1/2/3 по §7) и тот же маппинг *transport.OCSError{Code:404}
// → exit 2. Пакет НЕ импортирует internal/cli (направление зависимости строго
// cli → exit), НЕ санитайзит текст ошибок (это задача слоя client/config,
// спека §5/§9) — здесь только перенос кода и структурная обёртка.
package exit

import (
	"errors"
	"net/http"

	"github.com/stas/nctalk/internal/transport"
)

// Exit-коды по спеке §7.
//
//	0 — успех
//	1 — общая ошибка (сеть/авторизация)
//	2 — not found (только для разрешения <room>: 0 совпадений по имени)
//	3 — ambiguous (>1 совпадение по имени, не угадываем)
//
// ВНИМАНИЕ: константа для кода 1 названа ExitGeneric, а не ExitError, как в
// плане — потому что ExitError уже занято именем типа (см. ниже) в той же
// области видимости, и Go не допускает совпадения имён константы и типа.
// Спека §7 задаёт только числа кодов (0/1/2/3), не имена; коллизия возникла
// на уровне плана. Семантика кода 1 — «общая ошибка (сеть/авторизация)» —
// сохранена.
const (
	ExitOK        = 0
	ExitGeneric   = 1 // общая ошибка: сеть/авторизация/неизвестная команда (в плане — ExitError)
	ExitNotFound  = 2
	ExitAmbiguous = 3
)

// ExitError связывает ошибку с конкретным exit-кодом процесса.
// Реализует интерфейс error, поэтому свободно проходит как обычная ошибка,
// при этом Code используется на верхнем уровне (main) для os.Exit.
//
// Сама по себе эта обёртка НЕ санитайзит текст ошибки от кредов —
// это задача слоя client/config (см. спека §5, §9). Здесь только перенос кода.
type ExitError struct {
	Code int
	Err  error
}

// Error возвращает текст обёрнутой ошибки. При nil-ошибке возвращает пустую
// строку — это позволяет использовать ExitError{ExitOK, nil} как «нет ошибки».
func (e ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// Unwrap поддерживает errors.Is/errors.As над обёрнутой ошибкой.
func (e ExitError) Unwrap() error { return e.Err }

// Exit — хелпер-конструктор: собирает ExitError из кода и ошибки как есть,
// без проверок и санитайза (вызывающий отвечает за корректный код и текст).
func Exit(code int, err error) ExitError {
	return ExitError{Code: code, Err: err}
}

// FromClientErr маппит ошибку клиентского слоя в ExitError по контракту
// спеки §7/§9:
//
//   - nil → ExitError{ExitOK, nil} (успех; для поисковых команд пустой
//     результат = успех);
//   - *transport.OCSError с Code 404 → ExitNotFound (2): комната/сообщение/
//     реакция не найдены на стороне сервера;
//   - прочие ошибки (сеть, 401 auth, 403, 5xx, невалидный replyTo, decode-ошибка,
//     OCS-статусы отличные от 404) → ExitGeneric (1).
//
// Не различает неоднозначность (exit 3): та по-прежнему рождается только в
// room.ResolveRoom (>1 совпадение по --name), и путь оттуда идёт через явный
// exit.Exit(ExitAmbiguous, …), а не через эту функцию.
//
// Возвращает error (а не ExitError), чтобы cmd/nctalk-call/cmd/nctalk-talk
// могли использовать результат в стандартных идиомах `if err != nil` и
// errors.Is/As. Реальный тип возвращаемого значения всегда ExitError —
// вызывающий при необходимости делает type assertion или errors.As.
//
// Текст ошибки не санитайзится здесь — клиентский слой уже пропустил его через
// sanitizeErr (без URL/userinfo/пароля), спека §5/§9.
func FromClientErr(err error) error {
	if err == nil {
		return ExitError{Code: ExitOK}
	}
	var ocsErr *transport.OCSError
	if errors.As(err, &ocsErr) && ocsErr.Code == http.StatusNotFound {
		return ExitError{Code: ExitNotFound, Err: err}
	}
	return ExitError{Code: ExitGeneric, Err: err}
}
