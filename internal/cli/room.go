package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/stas/nctalk/internal/render"
)

// ResolveRoom разрешает room для chat/reactions-команд (спека §7).
//
// Правила (в порядке проверки):
//   - positional задан (это token, primary) → возвращается как есть, без
//     сетевого запроса. Если при этом задан ещё и nameFlag — positional всё
//     равно выигрывает, nameFlag игнорируется; в stderr пишется предупреждение.
//   - nameFlag задан (позиционного нет) → FindRooms(ctx, nameFlag, "") — это
//     case-insensitive подстрока по DisplayName (см. client.FindRooms):
//   - ровно 1 совпадение → rooms[0].Token;
//   - >1 совпадения → render.Candidates печатает список в stderr, возвращается
//     ExitError{ExitAmbiguous, "неоднозначное имя комнаты: <name>"};
//   - 0 совпадений → ExitError{ExitNotFound, "комната не найдена: <name>"}.
//   - positional и nameFlag оба пусты → ExitError{ExitGeneric,
//     "укажите token позиционно или --name"}.
//
// Сетевая/клиентская ошибка FindRooms пробрасывается как ExitError{ExitGeneric,
// err} (код 1 — «общая ошибка»: сеть/авторизация); текст приходит из client-слоя
// уже sanitized (без URL/userinfo).
func ResolveRoom(ctx context.Context, client TalkClient, positional, nameFlag string, stderr io.Writer) (string, error) {
	// positional = token (primary): возвращается как есть, без сетевого запроса.
	// Если задан ещё и --name — он игнорируется, но предупредим пользователя,
	// чтобы он не думал, что разрешение пошло по имени.
	if positional != "" {
		if nameFlag != "" {
			fmt.Fprintf(stderr, "nctalk: задан и token %q, и --name %q; приоритет у token, --name игнорируется\n", positional, nameFlag)
		}
		return positional, nil
	}

	// positional пуст → для --name нужен сетевой поиск подстроки.
	if nameFlag != "" {
		rooms, err := client.FindRooms(ctx, nameFlag, "")
		if err != nil {
			return "", Exit(ExitGeneric, err)
		}
		switch len(rooms) {
		case 0:
			return "", Exit(ExitNotFound, errors.New("комната не найдена: "+nameFlag))
		case 1:
			return rooms[0].Token, nil
		default:
			// Кандидаты печатаются в stderr через render.Candidates (ТИП|имя|token);
			// это часть контракт exit 3 — вызывающий (invokeHandler) ещё раз
			// напечатает текст ошибки, но сам список комнат только здесь.
			if perr := render.Candidates(stderr, rooms); perr != nil {
				// Ошибка записи в stderr не должна перекрывать основной сигнал
				// (неоднозначность); пишем отдельной строкой и движемся дальше.
				fmt.Fprintf(stderr, "nctalk: не удалось вывести список кандидатов: %v\n", perr)
			}
			return "", Exit(ExitAmbiguous, errors.New("неоднозначное имя комнаты: "+nameFlag))
		}
	}

	// Оба пусты — нечего разрешать; это ошибка пользователя, не сети.
	return "", Exit(ExitGeneric, errors.New("укажите token позиционно или --name"))
}
