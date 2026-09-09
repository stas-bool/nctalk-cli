package cli

// handlers_rooms.go — реализации команд `rooms list`, `rooms find`,
// `rooms search`, `rooms participants` (спека §6, план Task 4.5a;
// participants — спека-дельта 2026-09-09). Сигнатуры handler-функций
// фиксированы схемой роутинга cli.go (handlerFn).
//
// Разбор флагов — ручной (scan args), без flag-пакета (спека §3).
// Глобальный --json обрабатывается в Run и приходит как jsonOut.

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// roomsParticipantsHandler — реализация `rooms participants <room>` (спека-
// дельта 2026-09-09 §2, §5). Read-only: без мутаций, без мутационных env-
// флагов.
//
// Порядок шагов (дизайн §5):
//  1. ручной scan флагов (--name, обе формы; неизвестный --* → exit 1);
//  2. первый позиционный = room, лишние игнорируются;
//  3. ResolveRoom — коды 1/2/3 сохраняются (оба пусты → exit 1
//     «укажите token позиционно или --name»; конфликт positional+--name →
//     предупреждение в stderr, приоритет у positional);
//  4. GetParticipants;
//  5. сортировка ParticipantType asc → итоговое имя (displayName, при
//     пустом — actorId) case-insensitive asc; SliceStable — равные
//     сохраняют порядок сервера;
//  6. вывод render.ParticipantsTable / render.ParticipantsJSON по jsonOut;
//     пустой список → пустой stdout, exit 0 (как rooms list).
func roomsParticipantsHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1-2. Разбор флагов и позиционных: ручной scan (без flag-пакета, спека §3).
	var positionals []string
	var nameFlag string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("rooms participants: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++ // пропускаем значение на следующей итерации
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms participants: неизвестный флаг %q", a)}
		default:
			positionals = append(positionals, a)
		}
	}

	// Первый позиционный — room (token, primary); лишние игнорируются.
	var positional string
	if len(positionals) > 0 {
		positional = positionals[0]
	}

	// 3. Разрешение <room>. ResolveRoom сам печатает кандидаты в stderr при
	// неоднозначности и возвращает ExitAmbiguous/ExitNotFound/ExitGeneric
	// как ExitError — пробрасываем код.
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 4. Список участников (клиент возвращает non-nil слайс для «пусто»).
	ps, err := deps.Client.GetParticipants(ctx, token)
	if err != nil {
		// OCS 404 (комната не найдена) → exit 2; прочие (401/403/5xx/сеть) → 1.
		return exitFromClientErr(err)
	}

	// 5. Клиентская сортировка (прецедент — фильтры rooms list): роль asc →
	// итоговое имя asc. Fallback actorId применяется ДО сортировки, чтобы
	// безымянные гости не скучковались как пустая строка.
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].ParticipantType != ps[j].ParticipantType {
			return ps[i].ParticipantType < ps[j].ParticipantType
		}
		return strings.ToLower(participantSortName(ps[i])) < strings.ToLower(participantSortName(ps[j]))
	})

	// 6. Пустой список → пустой stdout, exit 0 (поисковая семантика; без
	// заголовка таблицы и без [] в --json — одинаково для обоих режимов).
	if len(ps) == 0 {
		return ExitError{Code: ExitOK}
	}
	if jsonOut {
		if err := render.ParticipantsJSON(deps.Stdout, ps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.ParticipantsTable(deps.Stdout, ps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}

// participantSortName — ключ сортировки по имени: displayName, при пустом —
// actorId (дублирует render.participantName, но cli не должен зависеть от
// внутренностей render; ключ — та же логика, что в колонке ИМЯ).
func participantSortName(p client.Participant) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ActorId
}
