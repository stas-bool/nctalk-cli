package cli

import (
	"errors"
	"net/http"

	"github.com/stas/nctalk/internal/client"
)

// Exit-коды по спеке §7.
//
//	0 — успех
//	1 — общая ошибка (сеть/авторизация)
//	2 — not found (только для разрешения <room>: 0 совпадений по имени)
//	3 — ambiguous (>1 совпадения по имени, не угадываем)
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

// exitFromClientErr маппит ошибку клиентского слоя в ExitError по контракту
// спеки §7/§9:
//
//   - nil → ExitOK (успех; для поисковых команд пустой результат = успех);
//   - *client.OCSError с Code 404 → ExitNotFound (2): комната/сообщение/реакция
//     не найдены на стороне сервера;
//   - прочие ошибки (сеть, 401 auth, 403, 5xx, невалидный replyTo, decode-ошибка,
//     OCS-статусы отличные от 404) → ExitGeneric (1).
//
// Не различает неоднозначность (exit 3): та по-прежнему рождается только в
// ResolveRoom (>1 совпадение по --name), и путь оттуда идёт через явный
// Exit(ExitAmbiguous, …), а не через эту функцию.
//
// Текст ошибки не санитайзится здесь — клиентский слой уже пропустил его через
// sanitizeErr (без URL/userinfo/пароля), спека §5/§9.
func exitFromClientErr(err error) ExitError {
	if err == nil {
		return ExitError{Code: ExitOK}
	}
	var ocsErr *client.OCSError
	if errors.As(err, &ocsErr) && ocsErr.Code == http.StatusNotFound {
		return ExitError{Code: ExitNotFound, Err: err}
	}
	return ExitError{Code: ExitGeneric, Err: err}
}
