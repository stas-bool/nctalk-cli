# Code-review: поддержка тредов (feat/chat-threads)

- **Дата:** 2026-09-29
- **Ревьюер:** opencode/glm-5.3 (named-сессия `s2c-code`), автономный прогон spec-to-code
- **Диапазон:** `git diff 26d4983..HEAD` (7 коммитов: d837fd9…cd1ee33)
- **Вердикт ревьюера:** блокеров к мержу нет; I-1 и M-2 лучше поправить в этой ветке,
  остальное — полировочным коммитом

Итог: **1 Important, 5 Minor, 0 Critical**. Сборка / `go vet` / `go test ./...` — зелёные
(CGO_ENABLED=0). Креды не светятся, exit-контракт соблюдён.

## Important

**I-1. `internal/cli/handlers_chat.go:683-694` — `chat threads --last 0` / `--last -3` молча превращаются в дефолт 20.**
Нецелое (`--last abc`) → exit 1, а явное бессмысленное значение (0, отрицательное) молча игнорируется. Это несимметрично самому же guard'у `--thread` (там `0`/отрицательное → exit 1, handlers_chat.go:438). Для `--last` «0 = не задан» скопирован с `chat show`, но в `threads` нет семантики «0 → потолок 200», где 0 осмыслен, — здесь это просто потерянная опечатка пользователя.
**Фикс:** в `chatThreadsHandler` отрицательные и ноль при явном флаге отклонять (`exit 1`), либо хотя бы отрицательные. Тест `TestChatThreadsLast` (кейсы `--last 0` / `--last -3`) обновить соответственно.

## Minor

**M-1. `internal/render/render.go:311-316` — `threadLastPreview` дублирует `lastMessagePreview` (render.go:52).**
Логика «пустой текст → systemMessage + `автор: текст» — дословный повтор минус время/nil-guard. **Фикс:** `threadLastPreview` свести к переиспользованию общей helper-функции `authorPreview(m) string` (автор+фолбэк), из которой собирают обе.

**M-2. `internal/render/render.go:96-104` — system-сообщения треда получают `↩N`.**
Системное `thread_created` (фикстура `chat_thread_history.json`, id 1250: `threadId=1234`, `threadTitle` загружен) в `chat show --system` пометится `↩1234` — шум для системной строки. **Фикс:** в `threadSuffix` добавить `m.MessageType != "system"` (или `m.SystemMessage == ""`) в условие `↩`-ветки.

**M-3. `internal/client/threads.go:63` vs `internal/client/chat.go:154` — рассинхрон эскейпа токена.**
Новый код правильно кладёт token в путь без `PathEscape` (транспорт эскейпит через `url.URL.String()`), а легаси в `chat.go` делает предварительный `PathEscape` → для токена с `%`/пробелом получился бы двойной эскейп (`%2520`). Токены Nextcloud буквенно-цифровые, поэтому не стреляет, но два шаблона в одном пакете — источник будущего копирования не того. **Фикс:** отдельным коммитом унифицировать (убрать `PathEscape` из chat.go/reactions.go, оставив регресс-тест на пробел — по аналогии с `TestGetParticipants_PathEscape`).

**M-4. `internal/cli/handlers_chat.go:695-697` — клэмп `--last 500 → 50` без предупреждения.**
Пользователь просит 500, получает 50 молча. **Фикс:** `fmt.Fprintf(deps.Stderr, "chat threads: --last=%d ограничен до 50\n", n)` — дёшево и снимает «почему тредов меньше».

**M-5. `internal/render/render.go:282-292` — `ThreadsTable` не режет длинные title/preview.**
Многострочный `first.Message` (фолбэк-название) сломает построчный формат таблицы — перенос строки попадёт в вывод. **Фикс:** обрезать до одной строки и ~80-100 рун (`strings.ReplaceAll(\n → ␣)` + `[]rune`-срез), как посчитаете нужным закрепить в спеке.

## Что проверено и чисто

- Пагинация `GetChat?threadId` наследует `X-Chat-Last-Given`-курсор без изменений; `threadId` шлётся на каждой странице (тест `TestGetChat_ThreadId`).
- Guard'ы взаимоисключаемости `--reply-to/--thread/--thread-title` и пустого `--thread-title` — до сети и до чтения stdin, exit 1, ноль вызовов (тест `TestChatSendThreadConflict`).
- `omitempty` в `sendMessageReq`/`Message.ThreadTitle/ThreadReplies` — JSON-канон соблюдён (тест `TestSendMessage_ThreadFields`).
- `ListThreads` null/`[]`-data → non-nil пустой слайс; 404 → exit 2; моки интерфейса обновлены во всех шести реализациях (compile-проверка `var _ TalkClient`).
- Мутация `First/Last.Message` в handler — осознанна и локализована текстовым режимом; тест корректно берёт свежие литералы.
- Фикстуры сняты с живого сервера и отражают реальный формат `messageParameters` (`[]` при пустых) — инвариант CLAUDE.md соблюдён, спека обновлена под живое наблюдение.
- Интеграционный `TestIntegration_Threads` закрыт `NCTALK_INTEGRATION_SEND`+`NCTALK_INTEGRATION_ROOM`, новых env не вводит.
