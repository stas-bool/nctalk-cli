package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/stas/nctalk/internal/render"
	"github.com/stas/nctalk/internal/room"
)

// ResolveRoom — тонкая обёртка над room.ResolveRoom для backcompat команд
// базового CLI (chat/reactions). Делает три вещи, которые чистая
// room.ResolveRoom НЕ делает (спека 2026-07-19 §4):
//
//  1. Предупреждение в stderr при конфликте positional+--name (приоритет
//     у positional, --name игнорируется).
//  2. Печать списка кандидатов через render.Candidates при неоднозначности
//     (StatusAmbiguous) — это часть контракта exit 3 для пользователя в
//     терминале.
//  3. Маппинг Result.Status и FindRooms-ошибки в ExitError (коды 0/1/2/3
//     по спеке §7).
//
// Существующие callers (handlers_chat.go, handlers_reactions.go) продолжают
// звать cli.ResolveRoom как прежде — сигнатура и семантика возврата сохранены.
//
// Правила разрешения (см. также room.ResolveRoom):
//   - positional задан → возвращается как есть, без сети; при заданном --name
//     печатается предупреждение в stderr.
//   - --name ровно 1 совпадение → token этой комнаты.
//   - --name >1 совпадения → ExitAmbiguous (3), кандидаты напечатаны в stderr.
//   - --name 0 совпадений → ExitNotFound (2).
//   - оба пусты → ExitGeneric (1), «укажите token позиционно или --name».
//   - сетевая/OCS-ошибка FindRooms → exit.FromClientErr (404 → 2, прочее → 1).
func ResolveRoom(ctx context.Context, client TalkClient, positional, nameFlag string, stderr io.Writer) (string, error) {
	// Предупреждение о конфликте positional+--name: приоритет у positional,
	// --name игнорируется. Чистая room.ResolveRoom этого не делает (она вообще
	// не пишет в stderr) — это забота CLI-обёртки для человека в терминале.
	if positional != "" && nameFlag != "" {
		fmt.Fprintf(stderr, "nctalk: задан и token %q, и --name %q; приоритет у token, --name игнорируется\n", positional, nameFlag)
	}

	result, err := room.ResolveRoom(ctx, client, positional, nameFlag)
	if err != nil {
		// Сетевая/OCS-ошибка FindRooms — маппим в ExitError через общий
		// internal/exit.FromClientErr (404 → 2, прочее → 1, контракта §7/§9).
		return "", exitFromClientErr(err)
	}

	switch result.Status {
	case room.StatusResolved:
		return result.Token, nil
	case room.StatusAmbiguous:
		// Кандидаты печатаются в stderr через render.Candidates (ТИП|имя|token);
		// это часть контракта exit 3 — вызывающий handler/invokeHandler ещё раз
		// напечатает текст ошибки, но сам список комнат только здесь.
		if perr := render.Candidates(stderr, result.Candidates); perr != nil {
			// Ошибка записи в stderr не должна перекрывать основной сигнал
			// (неоднозначность); пишем отдельной строкой и движемся дальше.
			fmt.Fprintf(stderr, "nctalk: не удалось вывести список кандидатов: %v\n", perr)
		}
		return "", Exit(ExitAmbiguous, errors.New("неоднозначное имя комнаты: "+result.Query))
	case room.StatusNotFound:
		return "", Exit(ExitNotFound, errors.New("комната не найдена: "+result.Query))
	case room.StatusEmptyInput:
		return "", Exit(ExitGeneric, errors.New("укажите token позиционно или --name"))
	}
	// Недостижимая ветка (switch покрывает все значения Status), но для
	// компилятора нужен возвращаемый путь — defensive.
	return "", Exit(ExitGeneric, errors.New("внутренняя ошибка: неизвестный статус ResolveRoom"))
}
