package cli

import (
	"context"
	"errors"
	"io"
)

// ResolveRoom разрешает room для chat/reactions-команд (спека §7).
//
// Поведение (реализация — Task 4.2, сейчас stub):
//   - positional задан (это token, primary) → возвращается как есть, без
//     сетевого запроса;
//   - nameFlag задан → FindRooms(ctx, nameFlag, "") case-insensitive подстрока:
//   - ровно 1 совпадение → его token;
//   - >1 совпадения →Candidates печатает список в stderr, возвращается
//     ExitError{ExitAmbiguous, ...};
//   - 0 совпадений → ExitError{ExitNotFound, ...}.
//
// Сейчас — stub: всегда возвращает "not implemented".
func ResolveRoom(_ context.Context, _ TalkClient, _, _ string, _ io.Writer) (string, error) {
	return "", errors.New("ResolveRoom: not implemented")
}
