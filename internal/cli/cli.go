// Этот файл — точка входа cli-слоя: интерфейс TalkClient, зависимости (Deps),
// таблица маршрутов `<resource> <verb>` и функция Run. Реализации handler-ов
// живут в handlers_*.go (по одному файлу на группу команд), ResolveRoom — в
// room.go. Сами handler-ы сейчас stub'ы (Task 4.2-4.5c заменят тела).
package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// TalkClient — минимальный интерфейс, покрывающий 7 методов реального
// *client.TalkClient. Нужен для mockability: в production в Deps.Client
// кладётся *client.TalkClient, в тестах — заглушка.
type TalkClient interface {
	ListRooms(ctx context.Context, opts client.ListRoomsOpts) ([]client.Room, error)
	FindRooms(ctx context.Context, query, actorId string) ([]client.Room, error)
	SearchRooms(ctx context.Context, term string, limit int) ([]client.ConversationResult, error)
	GetChat(ctx context.Context, token string, opts client.GetChatOpts) ([]client.Message, error)
	SendMessage(ctx context.Context, token string, opts client.SendMessageOpts) (int, error)
	GetReactions(ctx context.Context, token string, messageId int) (map[string][]client.ReactionActor, error)
	SearchMessages(ctx context.Context, term string, opts client.SearchMessagesOpts) ([]client.MessageResult, error)
}

// compile-time гарантия: *client.TalkClient реализует TalkClient. Если в client
// переименуют/изменят метод, cli не соберётся — интерфейс рассинхронизирован.
var _ TalkClient = (*client.TalkClient)(nil)

// Deps — зависимости, которые Run передаёт в handler-ы.
//
// Client — TalkClient (real или mock). Stdout/Stderr — куда писать результат и
// диагностику (main передаёт os.Stdout/os.Stderr, тесты — *bytes.Buffer).
// Now — функция текущего времени; если nil, Run подставляет time.Now — это
// нужно для парсинга относительных --since ("1h"/"2d") в тестах.
// Stdin — источник тела для `chat send` (stdin по умолчанию). Если nil,
// handler-ы сами fallback-ят на os.Stdin (так, чтобы nil был валидным
// значением для команд, не читающих stdin). Тесты кладут сюда bytes.Buffer.
type Deps struct {
	Client TalkClient
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Now    func() time.Time
}

// handlerFn — единая сигнатура всех command-handler'ов.
//
// args — аргументы команды БЕЗ resource/verb и БЕЗ глобального --json
// (например для `rooms list --json --no-system` это будет ["--no-system"]).
// jsonOut — true если в исходных args был глобальный --json (в любой позиции).
type handlerFn func(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError

// routes — таблица маршрутов `<resource>` → `<verb>` → handler.
//
// search СЮДА НЕ входит: он обрабатывается special-case без verb-уровня
// (см. searchHandlerFn). Сделано переменной (var), чтобы тесты могли
// подменять отдельные handler-ы на шпионов и проверять роутинг.
var routes = map[string]map[string]handlerFn{
	"rooms": {
		"list":   roomsListHandler,
		"find":   roomsFindHandler,
		"search": roomsSearchHandler,
	},
	"chat": {
		"show": chatShowHandler,
		"send": chatSendHandler,
	},
	"reactions": {
		"get": reactionsGetHandler,
	},
}

// searchHandlerFn — handler для `search` (спека §6), вынесен в переменную:
// search не имеет verb-уровня, поэтому не лежит в общей таблице routes, а
// вызывается напрямую из Run. Var (а не функция) — чтобы тесты могли подменять.
var searchHandlerFn handlerFn = searchHandler

// Run — точка входа cli-слоя.
//
// Разбирает args (без program name), маршрутизирует по шаблону
// `<resource> <verb>` (для search — special-case без verb-уровня), вызывает
// соответствующий handler и возвращает exit-код (для os.Exit в main).
//
// Глобальный --json может стоять в любой позиции args — он извлекается ДО
// роутинга и передаётся handler-ам как jsonOut.
//
// Все ошибки (неизвестная команда/verb, ошибка handler-а) → код ExitGeneric (1).
// Сетевые/клиентские ошибки приходят из client-слоя уже sanitized (без URL
// userinfo/query) — см. client.sanitizeErr.
func Run(args []string, deps Deps) int {
	// Now по умолчанию = time.Now; если caller задал (для тестов) — используем.
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ctx := context.Background()

	// Вырезаем --json из любой позиции; handler-ам он приходит как jsonOut.
	args, jsonOut := extractJSON(args)

	if len(args) == 0 {
		fmt.Fprintln(deps.Stderr, "nctalk: неизвестная команда; ожидается одна из: rooms, chat, reactions, search")
		return ExitGeneric
	}

	resource, rest := args[0], args[1:]

	// search — special-case: нет verb-уровня. Вся часть после `search` — это
	// term + флаги (--from/--limit/--all); передаётся в searchHandler как есть.
	if resource == "search" {
		return invokeHandler(deps, searchHandlerFn, ctx, rest, jsonOut)
	}

	// rooms/chat/reactions — двухуровневый разбор: args[0]=resource, args[1]=verb.
	if len(rest) == 0 {
		fmt.Fprintf(deps.Stderr, "nctalk %s: ожидается verb (list/find/search/show/send/get)\n", resource)
		return ExitGeneric
	}

	verb, handlerArgs := rest[0], rest[1:]

	verbMap, ok := routes[resource]
	if !ok {
		fmt.Fprintf(deps.Stderr, "nctalk: неизвестная команда %q\n", resource)
		return ExitGeneric
	}
	fn, ok := verbMap[verb]
	if !ok {
		fmt.Fprintf(deps.Stderr, "nctalk %s: неизвестный verb %q\n", resource, verb)
		return ExitGeneric
	}

	return invokeHandler(deps, fn, ctx, handlerArgs, jsonOut)
}

// invokeHandler вызывает handler-функцию и нормализует результат в exit-код.
//
// Если handler вернул ExitError с Err != nil — текст пишется в deps.Stderr
// (одна строка). Сам exit-код берётся из ExitError.Code. Эта централизация
// освобождает handler-ы от необходимости самому печатать общий текст ошибки;
// специализированный вывод (например, render.Candidates для ambiguous-room)
// handler делает сам в deps.Stderr до возврата.
func invokeHandler(deps Deps, fn handlerFn, ctx context.Context, args []string, jsonOut bool) int {
	ee := fn(ctx, deps, args, jsonOut)
	if ee.Err != nil {
		fmt.Fprintln(deps.Stderr, ee.Error())
	}
	return ee.Code
}

// extractJSON вырезает глобальный флаг --json из args (в любой позиции) и
// возвращает очищенные args + признак jsonOut. Используется до роутинга, чтобы
// downstream handler-ам не приходилось искать --json самим. Поддерживается
// только форма `--json` (boolean), не `--json=true` — спека §6 задаёт именно
// одиночный флаг.
func extractJSON(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		out = append(out, a)
	}
	return out, jsonOut
}
