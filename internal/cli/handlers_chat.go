package cli

// handlers_chat.go — реализации команд `chat show` (Task 4.3) и `chat send`
// (Task 4.4, пока stub). Сигнатуры handler-функций фиксированы схемой роутинга
// cli.go (handlerFn).
//
// Разбор флагов — ручной (scan args), без flag-пакета (спека §3). Глобальный
// --json уже вынесен в jsonOut слоем Run (cli.go extractJSON).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stas/nctalk/internal/client"
	"github.com/stas/nctalk/internal/render"
)

// chatShowHandler — реализация `chat show <room>` (спека §6, §8). Читает
// историю комнаты через GetChat с потолком выборки, фильтрует локально по
// --from (actorId) и --since (timestamp), по умолчанию скрывает system-сообщения
// (--system показывает).
//
// Правило выбора потолка выборки (Limit, до фильтров):
//   - --last N (N>0) → Limit = N;
//   - иначе при --from или --since → Limit = 200 (cap, чтобы фильтру был материал);
//   - иначе → Limit = 20 (базовый дефолт показа).
//
// При --since в GetChat передаётся StopBeforeTs — клиент обрежет страницу на
// первом сообщении старее sinceTs и прекратит пагинацию (ранний стоп, §6).
//
// Предупреждение в stderr выводится только при достижении cap 200 И пустом
// результате после фильтра — это сигнал пользователю сузить диапазон или
// использовать `search` (там серверный фильтр person=).
//
// 0 сообщений после фильтра → exit 0 с пустым stdout (команда прочитана,
// просто нет данных).
func chatShowHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1. Разбор флагов: ручной scan (без flag-пакета, спека §3).
	var (
		positionals  []string
		nameFlag     string
		lastRaw      string
		lastExplicit bool
		fromFlag     string
		sinceFlag    string
		systemFlag   bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat show: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case a == "--last":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat show: --last требует значение (целое)")}
			}
			lastRaw = args[i+1]
			lastExplicit = true
			i++
		case strings.HasPrefix(a, "--last="):
			lastRaw = strings.TrimPrefix(a, "--last=")
			lastExplicit = true
		case a == "--from":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat show: --from требует значение (actorId)")}
			}
			fromFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			fromFlag = strings.TrimPrefix(a, "--from=")
		case a == "--since":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat show: --since требует значение")}
			}
			sinceFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--since="):
			sinceFlag = strings.TrimPrefix(a, "--since=")
		case a == "--system":
			systemFlag = true
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat show: неизвестный флаг %q", a)}
		default:
			// Первый позиционный — token (primary); лишние позиционные
			// игнорируем, чтобы не ломаться на случайной «лишней» слове.
			positionals = append(positionals, a)
		}
	}

	// 2. Парсинг --last в int. lastExplicit говорит о факте присутствия флага,
	// но если значение <= 0 — потолок выбираем по общему правилу (cap 200 или
	// дефолт 20), как если бы --last не задавали.
	var last int
	if lastExplicit {
		n, err := strconv.Atoi(lastRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat show: --last ожидает целое число, получено %q", lastRaw)}
		}
		last = n
	}

	// 3. Парсинг --since. Относительные форматы (Ns/Nm/Nh/Nd) разбираем с опорой
	// на deps.Now (для детерминизма в тестах); render.ParseSince внутри берёт
	// real time.Now, что плохо для зафиксированных тестов. Абсолютные форматы
	// (дата/ISO) не зависят от now и идут через render.ParseSince.
	var sinceTs int64
	if sinceFlag != "" {
		ts, err := parseSinceAt(sinceFlag, deps.Now())
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
		sinceTs = ts
	}

	// 4. Разрешение <room>: positional token (primary) или --name (поиск).
	var positional string
	if len(positionals) > 0 {
		positional = positionals[0]
	}
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		// ResolveRoom возвращает ExitError с корректным кодом
		// (ExitAmbiguous/ExitNotFound/ExitGeneric) — сохраняем его, чтобы
		// invokeHandler не подменял код на ExitGeneric.
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 5. Выбор потолка выборки по правилу спеки §6/§8.
	var limit int
	switch {
	case last > 0:
		limit = last
	case sinceFlag != "" || fromFlag != "":
		limit = 200
	default:
		limit = 20
	}

	// 6. Запрос истории. StopBeforeTs задаётся только при --since — клиент
	// обрежет страницу и прекратит пагинацию (ранний стоп, см. client.GetChat).
	opts := client.GetChatOpts{
		Limit:        limit,
		StopBeforeTs: sinceTs,
	}
	msgs, err := deps.Client.GetChat(ctx, token, opts)
	if err != nil {
		// Сетевые/OCS-ошибки приходят sanitized (без URL/userinfo) — спека §5, §9.
		// OCS 404 (комната не найдена) → exit 2; прочие → exit 1 (спека §7/§9).
		return exitFromClientErr(err)
	}
	sampleLen := len(msgs) // размер ВЫБОРКИ до локального фильтра

	// 7. Локальный фильтр:
	//   - по умолчанию скрыты system-сообщения (messageType != "comment");
	//     --system показывает их;
	//   - --from оставляет только comment-сообщения от этого actorId
	//     (спека §8: фильтр работает только по comment, не system);
	//   - --since дублирует на стороне CLI тот же ts-фильтр, что в GetChat:
	//     гарантия, что даже если контракт клиента изменится, CLI-вывод будет
	//     корректным.
	filtered := make([]client.Message, 0, len(msgs))
	for _, m := range msgs {
		if !systemFlag && m.MessageType != "comment" {
			continue
		}
		if fromFlag != "" {
			if m.MessageType != "comment" || m.ActorId != fromFlag {
				continue
			}
		}
		if sinceTs != 0 && m.Timestamp < sinceTs {
			continue
		}
		filtered = append(filtered, m)
	}

	// 8. Предупреждение при достигнутом cap 200 И пустом результате фильтра.
	// sampleLen == 200 — простейшая аппроксимация «сервер вернул полную
	// страницу» (обычно означает, что есть ещё более старые сообщения).
	// Гасим предупреждение при явном --last: пользователь сам задал потолок и
	// знает объём выборки (иначе ложноположительно срабатывает на `--last 200`).
	if sampleLen == 200 && len(filtered) == 0 && last <= 0 {
		fmt.Fprintln(deps.Stderr,
			"выборка ограничена 200 сообщениями, совпадений не найдено; "+
				"сузьте диапазон (--since) или используйте search (серверный фильтр person=)")
	}

	// 9. Пустой результат → exit 0 с пустым stdout (команда прочитана, спека §6).
	if len(filtered) == 0 {
		return ExitError{Code: ExitOK}
	}

	// 10. Подстановка messageParameters в текст (для таблиц). JSON оставляет
	// оригинальное Message + MessageParameters — потребитель сам применит
	// подстановку при необходимости.
	if !jsonOut {
		for i := range filtered {
			filtered[i].Message = render.SubstituteParams(filtered[i].Message, filtered[i].MessageParameters)
		}
	}

	// 11. Вывод по jsonOut.
	if jsonOut {
		if err := render.MessagesJSON(deps.Stdout, filtered); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.MessagesTable(deps.Stdout, filtered); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}

// parseSinceAt — wrapper над render.ParseSince с опорой на заданное now для
// относительных форматов (Ns/Nm/Nh/Nd). render.ParseSince внутри использует
// real time.Now(), что не подходит для тестов с зафиксированным deps.Now.
// Абсолютные форматы (дата/ISO) не зависят от now и идут через render.ParseSince.
func parseSinceAt(s string, now time.Time) (int64, error) {
	if ts, ok := render.ParseRelativeAt(s, now); ok {
		return ts, nil
	}
	return render.ParseSince(s)
}

// chatSendHandler — реализация `chat send <room>` (спека §6 chat send).
//
// Отправляет сообщение в комнату <room>. Тело сообщения берётся из stdin
// (по умолчанию) либо из файла через --file <path>. Опциональные параметры:
//   - --reply-to <int> — id сообщения, на которое это ответ;
//   - --silent — отправить без уведомления получателей;
//   - --reference-id <uuid> — клиентский dedup-идентификатор.
//
// Источник тела:
//   - --file <path> → os.ReadFile(path), файл читается целиком;
//   - иначе → io.ReadAll(deps.Stdin), при deps.Stdin==nil fallback на os.Stdin.
//
// Пустое тело (пустой stdin или пустой файл) → клиентская ошибка ДО любого
// сетевого вызова (ни ResolveRoom.FindRooms, ни SendMessage не вызываются).
//
// Вывод (спека §6, ветвление по глобальному --json, который уже вынесен в
// jsonOut слоем Run):
//   - jsonOut=true → render.NewMessageIDJSON → в stdout `{"id": <int>}`;
//   - иначе → render.NewMessageID → в stdout `<int>\n`.
//
// Ошибка клиента (OCS-error от сервера, например невалидный replyTo) приходитит
// из client-слоя уже sanitized (без URL/userinfo) — пробрасывается как
// ExitError{ExitGeneric, err}.
func chatSendHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1. Разбор флагов: ручной scan (без flag-пакета, спека §3). Поддерживаем
	// обе формы — `--flag value` и `--flag=value` — единообразно с chatShow.
	var (
		positionals  []string
		nameFlag     string
		fileFlag     string
		replyToRaw   string
		replyToSet   bool
		silentFlag   bool
		referenceId  string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case a == "--file":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --file требует значение (путь)")}
			}
			fileFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--file="):
			fileFlag = strings.TrimPrefix(a, "--file=")
		case a == "--reply-to":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --reply-to требует значение (id сообщения)")}
			}
			replyToRaw = args[i+1]
			replyToSet = true
			i++
		case strings.HasPrefix(a, "--reply-to="):
			replyToRaw = strings.TrimPrefix(a, "--reply-to=")
			replyToSet = true
		case a == "--silent":
			silentFlag = true
		case a == "--reference-id":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --reference-id требует значение")}
			}
			referenceId = args[i+1]
			i++
		case strings.HasPrefix(a, "--reference-id="):
			referenceId = strings.TrimPrefix(a, "--reference-id=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: неизвестный флаг %q", a)}
		default:
			// Первый позиционный — token/room (primary). Лишние позиционные
			// игнорируем — строгость тут скорее вредна (случайный пробел).
			positionals = append(positionals, a)
		}
	}

	// 2. Парсинг --reply-to в int (если флаг присутствовал).
	var replyTo int
	if replyToSet {
		n, err := strconv.Atoi(replyToRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --reply-to ожидает целое число, получено %q", replyToRaw)}
		}
		if n <= 0 {
			// Неположительный id — явно ошибочный ввод: отсекаем ДО ResolveRoom и
			// SendMessage, не тратя сетевой запрос (сервер всё равно вернёт 4xx).
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --reply-to ожидает положительное число, получено %d", n)}
		}
		replyTo = n
	}

	// 3. Чтение тела ДО ResolveRoom: пустое тело отсекается без единого
	// сетевого вызова (ни FindRooms, ни SendMessage) — это явно требует спека
	// и DoD Task 4.4.
	var body []byte
	var readErr error
	if fileFlag != "" {
		body, readErr = os.ReadFile(fileFlag)
	} else {
		stdin := deps.Stdin
		if stdin == nil {
			// Production-путь: main не проставляет Stdin, fallback на реальный
			// os.Stdin. Тесты всегда кладут свой io.Reader.
			stdin = os.Stdin
		}
		body, readErr = io.ReadAll(stdin)
	}
	if readErr != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: не удалось прочитать тело: %w", readErr)}
	}
	// Срезаем ОДИН завершающий перевод строки: `echo "hi" | nctalk chat send`
	// отправил бы "hi\n" (с сохранением сервером \n). Многострочные тела (с \n
	// внутри) валидны — поэтому тримим ровно один trailing \n / \r\n, не пачку.
	text := string(body)
	if strings.HasSuffix(text, "\r\n") {
		text = text[:len(text)-2]
	} else if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if len(text) == 0 {
		// Пустое stdin/файл (или только перевод строки) — ошибка пользователя,
		// не сети; клиент не зовём.
		return ExitError{Code: ExitGeneric, Err: errors.New("chat send: тело сообщения пусто")}
	}

	// 4. Разрешение <room>: positional token (primary) или --name (поиск).
	var positional string
	if len(positionals) > 0 {
		positional = positionals[0]
	}
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		// ResolveRoom возвращает ExitError с корректным кодом
		// (ExitAmbiguous/ExitNotFound/ExitGeneric) — сохраняем его.
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 5. Отправка. SendMessageOpts{Message, ReplyTo, Silent, ReferenceId}.
	id, err := deps.Client.SendMessage(ctx, token, client.SendMessageOpts{
		Message:     text,
		ReplyTo:     replyTo,
		Silent:      silentFlag,
		ReferenceId: referenceId,
	})
	if err != nil {
		// OCS-error/сеть — текст уже sanitized в client-слое (спека §5, §9).
		// 404 → exit 2 (невалидный token), прочие OCS/сеть → exit 1.
		return exitFromClientErr(err)
	}

	// 6. Вывод id по ветке jsonOut (спека §6).
	if jsonOut {
		if err := render.NewMessageIDJSON(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.NewMessageID(deps.Stdout, id); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
