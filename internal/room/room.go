// Package room реализует разрешение <room>/--name → token для команд
// базового CLI (cmd/nctalk) и для cmd/nctalk-call/cmd/nctalk-talk. Контракт
// exit-кодов (1/2/3) — спека 2026-07-17 §7 и 2026-07-19 §6.
//
// ВАЖНО: пакет room НЕ импортирует internal/render и internal/cli — это чистая
// логика разрешения. Форматирование вывода (печать списка кандидатов в stderr
// при неоднозначности) делает вызывающий код — cmd/nctalk через render.Candidates,
// cmd/nctalk-call/cmd/nctalk-talk своим способом. Без этого инвариант спеки
// §4 «cmd/nctalk-call без cli-роутера и render» нарушался бы (room→render
// затянул бы render в бинарник агента).
package room

import (
	"context"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// RoomLister — минимальная зависимость от клиента: нужен только FindRooms.
// Интерфейс здесь (а не cli.TalkClient), чтобы internal/room не зависел
// от internal/cli (иначе cmd/nctalk-call утянет cli-роутер и render).
// Возвращает []client.Room (тип из internal/client — это доменный тип, не
// cli-специфичный; cmd/nctalk-call и так импортирует client для других целей).
type RoomLister interface {
	FindRooms(ctx context.Context, query, actorId string) ([]client.Room, error)
}

// Status — ветка результата разрешения. Определяет, что делать вызывающему:
//
//   - StatusResolved   : Token валиден, Candidates пуст.
//   - StatusAmbiguous  : Token пуст, Candidates содержит >1 совпадение (печать
//                        списка — ответственность вызывающего кода).
//   - StatusNotFound   : 0 совпадений по --name.
//   - StatusEmptyInput : не задан ни positional, ни --name.
//
// Сетевая/OCS-ошибка возвращается через err (raw, без обёртки) — маппинг в
// exit-код делается вызывающим через exit.FromClientErr (404 → exit 2,
// прочее → exit 1).
type Status int

const (
	// StatusResolved — успешно: token определён (из positional или единственного
	// совпадения по --name).
	StatusResolved Status = iota
	// StatusAmbiguous — по --name найдено >1 комнаты; вызывающий должен
	// напечатать Result.Candidates и вернуть exit-код 3.
	StatusAmbiguous
	// StatusNotFound — по --name 0 совпадений; вызывающий возвращает exit-код 2.
	StatusNotFound
	// StatusEmptyInput — не задан ни positional, ни --name; вызывающий
	// возвращает exit-код 1 (общая ошибка пользовательского ввода).
	StatusEmptyInput
)

// Result — результат разрешения. Semantics по полям зависят от Status (см.
// комментарий к типу Status). При err != nil Result невалиден — вызывающий
// обязан сначала проверить err.
type Result struct {
	Status     Status
	Token      string        // для StatusResolved
	Candidates []client.Room // для StatusAmbiguous (для печати вызывающим)
	Query      string        // исходное --name (для диагностик вызывающего)
}

// ResolveRoom — чистая функция разрешения <room>/--name → token. НЕ пишет в
// stderr, НЕ печатает список кандидатов, НЕ маппит ошибки в exit-коды.
// Возвращает Result (для Status-ветвления) и err (raw от FindRooms — маппинг
// в exit.ExitError делает вызывающий через exit.FromClientErr).
//
// Правила (в порядке проверки):
//
//   - positional задан (это token, primary) → StatusResolved с этим token,
//     без сетевого запроса. Если при этом задан ещё и nameFlag — positional
//     всё равно выигрывает, nameFlag игнорируется; предупреждение в stderr
//     (если нужно) — ответственность вызывающего.
//   - nameFlag задан (позиционного нет) → FindRooms(ctx, nameFlag, "") — это
//     case-insensitive подстрока по DisplayName (см. client.FindRooms):
//   - ровно 1 совпадение → StatusResolved с rooms[0].Token;
//   - >1 совпадения → StatusAmbiguous с Candidates = rooms;
//   - 0 совпадений → StatusNotFound.
//   - positional и nameFlag оба пусты → StatusEmptyInput.
//
// Сетевая/клиентская ошибка FindRooms возвращается как err (raw, без
// обёртки в ExitError); текст приходит из client-слоя уже sanitized
// (без URL/userinfo), спека §5/§9.
func ResolveRoom(ctx context.Context, client RoomLister, positional, nameFlag string) (Result, error) {
	// positional = token (primary): возвращается как есть, без сетевого запроса.
	// Если задан ещё и --name — он игнорируется; предупреждение об этом (если
	// нужно) печатает вызывающий, не room.ResolveRoom.
	if positional != "" {
		return Result{Status: StatusResolved, Token: positional}, nil
	}

	// positional пуст → для --name нужен сетевой поиск подстроки.
	if nameFlag != "" {
		rooms, err := client.FindRooms(ctx, nameFlag, "")
		if err != nil {
			// Сетевая/OCS-ошибка. Возвращаем raw err — маппинг *transport.OCSError
			// (404 → exit 2, прочее → exit 1) делает вызывающий через
			// exit.FromClientErr. На практике FindRooms зовёт ListRooms, и 404
			// оттуда маловероятен, но единообразие контракта важнее.
			return Result{}, err
		}
		switch len(rooms) {
		case 0:
			return Result{Status: StatusNotFound, Query: nameFlag}, nil
		case 1:
			return Result{Status: StatusResolved, Token: rooms[0].Token, Query: nameFlag}, nil
		default:
			return Result{Status: StatusAmbiguous, Candidates: rooms, Query: nameFlag}, nil
		}
	}

	// Оба пусты — нечего разрешать; это ошибка пользователя, не сети.
	return Result{Status: StatusEmptyInput}, nil
}
