package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stas-bool/nctalk-cli/internal/client"
	"github.com/stas-bool/nctalk-cli/internal/render"
)

// searchHandler — реализация `search <term>` (спека §6): глобальный поиск
// сообщений через Unified talk-message provider. Special-case роутинга: для
// `search` НЕТ verb-уровня, поэтому handler получает args как `term` + флаги
// (--from/--limit/--all) целиком; глобальный --json уже вынесен в jsonOut
// слоем Run (cli.go extractJSON).
//
// Разбор флагов — ручной (без flag package, спека §6). term — первый
// не-флаг аргумент (последующие не-флаги игнорируются; для multi-word запроса
// пользователь квотирует: search "foo bar").
//
// --limit НЕ нормализуется здесь: при отсутствии передаём 0, при наличии —
// сырое значение как есть; клиент SearchMessages сам подставит дефолт 10 и
// ограничит 1..25 (спека §6 search). Дублирующий guard на пустой term даёт
// понятное CLI-сообщение БЕЗ сетевого вызова (дублирует клиентский чек).
//
// Пустой результат → exit 0 с пустым stdout (поисковая семантика, спека §7).
func searchHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	var opts client.SearchMessagesOpts
	var term string

	// Линейный разбор args: --from/--limit — со значением (-value на следующей
	// позиции), --all — boolean, любой другой токен начинающийся с "--" —
	// неизвестный флаг, первый не-флаг — term.
	limitSet := false
	var limitRaw string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("search: --from требует значение")}
			}
			opts.From = args[i+1]
			i++ // пропускаем значение на следующей итерации
		case a == "--limit":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("search: --limit требует значение")}
			}
			limitRaw = args[i+1]
			limitSet = true
			i++ // пропускаем значение на следующей итерации
		case a == "--all":
			opts.All = true
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("search: неизвестный флаг %q", a)}
		default:
			// Первый не-флаг аргумент — term (спека §6). Повторные не-флаги
			// игнорируются.
			if term == "" {
				term = a
			}
		}
	}

	// --limit парсим отдельно от сканирования: передаём значение как есть,
	// клиент нормализует (спека §6). Ошибка парсинга → понятный exit 1.
	if limitSet {
		n, err := strconv.Atoi(limitRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("search: --limit ожидает целое число, получено %q", limitRaw)}
		}
		opts.Limit = n
	}

	// CLI-level guard: пустой term отсекаем ДО клиентского вызова (спека §8),
	// чтобы не делать сеть и выдать понятное сообщение. Дублирует клиентский чек.
	if term == "" {
		return ExitError{Code: ExitGeneric, Err: errors.New("term не может быть пустым")}
	}

	rs, err := deps.Client.SearchMessages(ctx, term, opts)
	if err != nil {
		return exitFromClientErr(err)
	}

	// Пустой результат — exit 0 с пустым stdout (поисковая семантика, спека §7):
	// render.MessageResultsTable даже для пустого слайса печатает заголовок, что
	// нарушило бы «пустой вывод», поэтому просто выходим.
	if len(rs) == 0 {
		return ExitError{Code: ExitOK}
	}

	// Вывод через render-слой по jsonOut. Креды в результат не попадают:
	// SearchMessages работает только по term/opts.
	if jsonOut {
		if err := render.MessageResultsJSON(deps.Stdout, rs); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.MessageResultsTable(deps.Stdout, rs); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
