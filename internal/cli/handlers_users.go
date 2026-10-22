package cli

// handlers_users.go — команды ресурса users (спека-дельта 2026-10-22
// `users show`, §2, §5).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/render"
)

// usersShowHandler — реализация `users show <userId>` (спека-дельта
// 2026-10-22 §2, §5). Read-only: без мутаций, без мутационных env-флагов.
//
// Порядок шагов (дизайн §5):
//  1. ручной scan аргументов: любой --* → exit 1 «неизвестный флаг» (флагов
//     у команды нет; глобальный --json вырезан до роутинга); первый
//     позиционный = userId, лишние игнорируются;
//  2. пустой userId → exit 1 без сети. Проверку «один сегмент пути» handler
//     НЕ делает — это единственный guard в client.GetProfile (до сети), его
//     ошибка — не *OCSError → exit 1 через exitFromClientErr;
//  3. GetProfile; ошибки — usersShowError (тексты и коды спеки §2);
//  4. render.ProfileText (местное время от deps.Now) / render.ProfileJSON.
func usersShowHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1. Ручной scan (без flag-пакета, как у остальных handler-ов).
	var positionals []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("users show: неизвестный флаг %q", a)}
		}
		positionals = append(positionals, a)
	}

	// 2. Первый позиционный — userId; лишние игнорируются.
	var userId string
	if len(positionals) > 0 {
		userId = positionals[0]
	}
	if userId == "" {
		return ExitError{Code: ExitGeneric, Err: errors.New("users show: укажите userId пользователя")}
	}

	// 3. Профиль.
	p, err := deps.Client.GetProfile(ctx, userId)
	if err != nil {
		return usersShowError(userId, err)
	}

	// 4. Вывод.
	if jsonOut {
		if err := render.ProfileJSON(deps.Stdout, p); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.ProfileText(deps.Stdout, p, deps.Now()); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}

// usersShowError — тексты и exit-коды ошибок `users show` (спека-дельта §2):
//   - OCS 404 с ПУСТЫМ Message (сигнатура profile-API: нет пользователя,
//     учётка отключена или перечисление запрещено — API их не различает)
//     → exit 2 «не найден или недоступен»;
//   - 404 с НЕпустым Message (OCS 998 «Invalid query» — маршрута нет, сервер
//     старше Nextcloud 31; HTML-404 прокси) → exit 2 «profile-API недоступен»
//     с исходной ошибкой;
//   - OCS 400 (профиль отключён) → exit 1 «профиль отключён»;
//   - прочее (429, 401, 5xx, сеть, guard userId) — exitFromClientErr (exit 1).
func usersShowError(userId string, err error) ExitError {
	var oe *client.OCSError
	if errors.As(err, &oe) {
		switch {
		case oe.Code == http.StatusNotFound && oe.Message == "":
			return ExitError{Code: ExitNotFound, Err: fmt.Errorf("users show: пользователь %q не найден или недоступен", userId)}
		case oe.Code == http.StatusNotFound:
			return ExitError{Code: ExitNotFound, Err: fmt.Errorf("users show: profile-API недоступен на сервере (нужен Nextcloud 31+): %w", err)}
		case oe.Code == http.StatusBadRequest:
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("users show: профиль %q отключён", userId)}
		}
	}
	return exitFromClientErr(err)
}
