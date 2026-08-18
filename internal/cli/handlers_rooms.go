package cli

// handlers_rooms.go — реализации команд `rooms list`, `rooms find`,
// `rooms search` (спека §6, план Task 4.5a). Сигнатуры handler-функций
// фиксированы схемой роутинга cli.go (handlerFn).
//
// Разбор флагов — ручной (scan args), без flag-пакета (спека §3).
// Глобальный --json обрабатывается в Run и приходит как jsonOut.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/render"
)

// roomsListHandler — реализация `rooms list` (спека §6).
//
// Флаги:
//   - `--type <1|2|3>` — фильтр по типу комнаты (RoomType);
//   - `--unread` — только комнаты с UnreadMessages > 0;
//   - `--include-former` — показывать former-комнаты (типы 4/5/6).
//
// Поддерживаются обе формы: `--type 2` и `--type=2`. Глобальный --json
// приходит как jsonOut и ветвит вывод на render.RoomsJSON / render.RoomsTable.
//
// Пустой результат → exit 0 с пустым stdout (поисковая семантика).
func roomsListHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	var typeStr string
	var unread, includeFormer bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--type":
			// Значение в следующем arg.
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("rooms list: --type требует значение (1/2/3)")}
			}
			typeStr = args[i+1]
			i++
		case strings.HasPrefix(a, "--type="):
			typeStr = strings.TrimPrefix(a, "--type=")
		case a == "--unread":
			unread = true
		case a == "--include-former":
			includeFormer = true
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms list: неизвестный флаг %q", a)}
		default:
			// У rooms list нет позиционных аргументов; молча игнорируем
			// огрызки, чтобы не ломать пользователя случайным «не туда».
		}
	}

	// --type парсится из строки в int → RoomType, берётся адрес для *RoomType.
	var roomType *client.RoomType
	if typeStr != "" {
		n, err := strconv.Atoi(typeStr)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms list: --type ожидает число (1/2/3), получено %q", typeStr)}
		}
		rt := client.RoomType(n)
		roomType = &rt
	}

	opts := client.ListRoomsOpts{
		Type:          roomType,
		UnreadOnly:    unread,
		IncludeFormer: includeFormer,
	}

	rooms, err := deps.Client.ListRooms(ctx, opts)
	if err != nil {
		// Сетевые/OCS-ошибки приходят уже sanitized (без URL/userinfo) —
		// спека §5, §9. 404 → exit 2, прочие → exit 1 (контракт §7/§9).
		return exitFromClientErr(err)
	}
	if len(rooms) == 0 {
		// Поисковая семантика: пусто → пустой stdout, exit 0 (без заголовка
		// таблицы, чтобы вывод был действительно пустым).
		return ExitError{Code: ExitOK, Err: nil}
	}
	if jsonOut {
		if err := render.RoomsJSON(deps.Stdout, rooms); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.RoomsTable(deps.Stdout, rooms); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK, Err: nil}
}

// roomsFindHandler — реализация `rooms find <query>` (спека §6).
//
// Флаги:
//   - `--user <actorId>` — точный фильтр по Room.ActorId (для type=1 one-to-one
//     это собеседник, не текущий пользователь — спека §8, §12).
//
// Первый позиционный аргумент (не-флаг) — query: case-insensitive подстрока по
// DisplayName. Пустой результат → exit 0 с пустым stdout.
func roomsFindHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	var actorId string
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--user":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("rooms find: --user требует значение (actorId)")}
			}
			actorId = args[i+1]
			i++
		case strings.HasPrefix(a, "--user="):
			actorId = strings.TrimPrefix(a, "--user=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms find: неизвестный флаг %q", a)}
		default:
			positionals = append(positionals, a)
		}
	}

	if len(positionals) == 0 {
		return ExitError{Code: ExitGeneric, Err: errors.New("rooms find: ожидается поисковый запрос")}
	}
	// Первый позиционный — query; лишние позиционные (если есть) игнорируем,
	// чтобы не ломаться на случайной «лишней» слове. Мульти-слово → через shell quoting.
	query := positionals[0]

	rooms, err := deps.Client.FindRooms(ctx, query, actorId)
	if err != nil {
		// 404 → exit 2, прочие → exit 1 (контракт §7/§9). Пустой результат
		// FindRooms — это НЕ OCS 404 (это ListRooms 200 + 0 совпадений локально),
		// поэтому семантика «empty = exit 0» ниже по функции сохраняется.
		return exitFromClientErr(err)
	}
	if len(rooms) == 0 {
		return ExitError{Code: ExitOK, Err: nil}
	}
	if jsonOut {
		if err := render.RoomsJSON(deps.Stdout, rooms); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.RoomsTable(deps.Stdout, rooms); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK, Err: nil}
}

// roomsSearchHandler — реализация `rooms search <term>` (спека §6, §8 — Unified
// talk-conversations provider).
//
// Первый позиционный аргумент (не-флаг) — term. limit=0 → серверный дефолт
// (спека §8: «при limit <= 0 параметр не передаётся»). Пустой результат →
// exit 0 с пустым stdout.
func roomsSearchHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	var positionals []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms search: неизвестный флаг %q", a)}
		}
		positionals = append(positionals, a)
	}
	if len(positionals) == 0 {
		return ExitError{Code: ExitGeneric, Err: errors.New("rooms search: ожидается term")}
	}
	term := positionals[0]

	rs, err := deps.Client.SearchRooms(ctx, term, 0)
	if err != nil {
		return exitFromClientErr(err)
	}
	if len(rs) == 0 {
		return ExitError{Code: ExitOK, Err: nil}
	}
	if jsonOut {
		if err := render.ConversationResultsJSON(deps.Stdout, rs); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.ConversationResultsTable(deps.Stdout, rs); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK, Err: nil}
}
