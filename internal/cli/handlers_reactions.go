package cli

// handlers_reactions.go — реализация команды `reactions get` (спека §6,
// план Task 4.5b). Сигнатура handler-функции фиксирована схемой роутинга cli.go
// (handlerFn).
//
// Разбор флагов — ручной (scan args), без flag-пакета (спека §3). Глобальный
// --json уже вынесен слоем Run (cli.go extractJSON) и приходит как jsonOut.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stas/nctalk/internal/render"
)

// reactionsGetHandler — реализация `reactions get <room> <messageId>` (спека §6).
//
// Позиционные аргументы:
//   - <room> — token комнаты (позиционно) ИЛИ разрешается через `--name <имя>`
//     (case-insensitive подстрока по DisplayName, см. ResolveRoom);
//   - <messageId> — целочисленный id сообщения в этой комнате.
//
// Если <room> задан И позиционно, И через --name, positional выигрывает (token),
// а --name игнорируется с предупреждением в stderr — это поведение достаётся
// бесплатно через ResolveRoom.
//
// Разбор флагов: `--name <имя>` и `--name=<имя>`. Любой неизвестный флаг →
// ExitGeneric.
//
// Вывод ветвится по jsonOut (глобальный --json, спека §6): при true —
// render.ReactionsJSON (сериализация map как есть, пустая → `{}`), иначе —
// render.ReactionsText (текст «эмодзи → [авторы]», пустая → «реакций нет»).
//
// Exit-коды (спека §7): 0 — успех (включая пустую map); 1 — общая ошибка
// (сеть/авторизация/невалидный messageId); 2 — room не найден по --name;
// 3 — неоднозначное --name (кандидаты уже напечатаны в stderr через ResolveRoom).
func reactionsGetHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// Линейный разбор args: --name — со значением (-value на следующей позиции
	// ИЛИ --name=value), любой не-флаг — позиционный аргумент.
	var positionals []string
	var nameFlag string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("reactions get: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++ // пропускаем значение на следующей итерации
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("reactions get: неизвестный флаг %q", a)}
		default:
			positionals = append(positionals, a)
		}
	}

	// Распределение positionals:
	//   - ≥2 positionals → первый = room (token), второй = messageId. Если при
	//     этом задан ещё и --name, positional выигрывает (ResolveRoom предупредит
	//     в stderr и проигнорирует --name) — это соответствует контракту §7.
	//   - 1 positional + --name → room разрешается по --name, а positional это
	//     messageId.
	//   - 1 positional без --name → нехватка: ожидается и <room>, и <messageId>.
	//   - 0 positionals → нечего разбирать.
	var roomPos, msgIdRaw string
	switch {
	case len(positionals) >= 2:
		roomPos = positionals[0]
		msgIdRaw = positionals[1]
	case len(positionals) == 1 && nameFlag != "":
		msgIdRaw = positionals[0]
	case len(positionals) == 1:
		return ExitError{Code: ExitGeneric, Err: errors.New("reactions get: ожидается <room> <messageId>")}
	default:
		return ExitError{Code: ExitGeneric, Err: errors.New("reactions get: ожидается <room> <messageId> или --name <имя> <messageId>")}
	}

	// Разрешаем room: positional token (без сети) либо FindRooms по --name.
	// ResolveRoom сам напечатает кандидаты в stderr при неоднозначности и вернёт
	// ExitAmbiguous/ExitNotFound как ExitError — нам нужно лишь пробросить код.
	token, err := ResolveRoom(ctx, deps.Client, roomPos, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// messageId парсим отдельно: ошибка → понятный exit 1 (без сетевого вызова).
	messageId, err := strconv.Atoi(msgIdRaw)
	if err != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("reactions get: <messageId> ожидает целое число, получено %q", msgIdRaw)}
	}
	if messageId <= 0 {
		// Неположительный id отсекаем ДО сетевого вызова — серверный 4xx вместо
		// этого дал бы менее понятную диагностику (спека §7: exit 1).
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("reactions get: <messageId> ожидает положительное число, получено %d", messageId)}
	}

	// Получаем реакции (клиент возвращает non-nil пустую map для «реакций нет»).
	reps, err := deps.Client.GetReactions(ctx, token, messageId)
	if err != nil {
		// Сетевые/OCS-ошибки приходят уже sanitized (без URL/userinfo) — спека §5, §9.
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// Ветвление вывода по глобальному --json (спека §6). Пустая map:
	//   - текстовый режим → «реакций нет» (render.ReactionsText);
	//   - json-режим → `{}` (render.ReactionsJSON, json-маршалинг пустой map).
	// Креды в вывод не попадают — GetReactions работает по (token, messageId).
	if jsonOut {
		if err := render.ReactionsJSON(deps.Stdout, reps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.ReactionsText(deps.Stdout, reps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
