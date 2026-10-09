# Ревью спеки `2026-10-09-nctalk-chat-attach-design.md`

- **Дата ревью:** 2026-10-09
- **Ревьюер:** opencode (glm-5.3)
- **Проход №2 (заземление в коде):** основные утверждения о существующей системе подтверждены — `readMessageBody` (internal/cli/handlers_chat.go:282), `DoOCS`/`Doer`/`Auth` (internal/transport/transport.go:36,48,123), `SameHostRedirectPolicy`/`SanitizeErr`, `NewTalkClientWithDoer` (internal/client/client.go:62), интерфейс `cli.TalkClient` из 10 методов (internal/cli/cli.go:19) и ровно шесть моков (mockTalkClient, chatSpyClient, searchSpyClient, roomsSpyClient, reactionsSpyClient, roomMockClient), комментарий «10 методов» (internal/cli/cli.go:16), `cmdSpecs`/`TestCmdSpecs_FlagsExactly` (internal/cli/help_test.go:55), `DefaultTimeout = 30s` (internal/config/config.go:27), guards положительных id (internal/cli/handlers_chat.go:423,431), `Message.ReferenceId`/`IsReplyable` (internal/client/types.go:23,25), `GetChat` шлёт `lookIntoFuture=0&setReadMarker=0` (internal/client/chat.go:164-168). Замечания ниже.

## 🟡 Стоит уточнить

1. **§5, порядок шагов `SendFile`: pre-check (шаг 2) идёт ДО проверки `attachments.allowed` (шаг 3).**
   При отключённых на сервере вложениях и невалидном `--thread` клиент сначала сделает два сетевых
   запроса pre-check и вернёт exit `2` («тред не найден»), хотя файл в принципе отправить нельзя —
   пользователь не увидит причину («вложения запрещены», exit `1` по таблице §2.6). Оба запроса —
   сеть, т.е. выгода «не грузить файл зря» уже достигнута, а диагностика страдает. Уточнить
   порядок: настройки вложений (дешёвый capabilities-запрос, он же нужен для `folder`) до
   pre-check-ов, либо осознанно зафиксировать текущий порядок компромиссом.

2. **§3.6, окно поиска id: потолок `Limit: 50` фиксирован и не прогрессивен.**
   `GetChat` с `Limit: 50` пагинирует до 50 собранных сообщений; если между шарингом и поиском в
   чат написали >50 сообщений (активная комната, агент-потребитель), файловое сообщение уйдёт за
   окно → `MessageId: 0`, exit `0` с предупреждением при фактически доставленном файле. Спека не
   фиксирует это как осознанный компромисс (ни «сколько максимум просматриваем», ни «почему не
   расширить окно второй страницей»). Добавить явную оговорку, чтобы план и ревью кода не
   спорили о числе.

3. **§2.2, правило терминала сформулировано через `Deps.Stdin`, но production-путь идёт мимо него.**
   В production `main` не проставляет `Deps.Stdin` (nil), и fallback на `os.Stdin` происходит
   внутри `readMessageBody` (internal/cli/handlers_chat.go:288-291). Проверка `ModeCharDevice`
   должна применяться к итоговому reader-у **после** fallback, а не к полю `Deps.Stdin`.
   Формулировка «`*os.File` с `ModeCharDevice` не читается» (§6) правильная, но §2.2 («В тестах
   `Deps.Stdin` — не `*os.File`, читается всегда») описывает только тестовый случай и оставляет
   production-случай додумывать. Уточнить в §2.2 одной фразой.

## 🟢 По красоте

1. **§5: «остальные пять — errMock-стабы».**
   Фактически остальные моки — spy-структуры (`searchSpyClient`, `roomsSpyClient`,
   `reactionsSpyClient`, `roomMockClient`, `mockTalkClient` в internal/cli/*_test.go), errMock
   возвращают лишь их неиспользуемые методы. Формулировка «errMock-стабы» вводит в заблуждение о
   типе правки (менять тела всех методов не нужно — только добавить новый). Переформулировать:
   «остальные пять получают метод, возвращающий errMock».

2. **§2.1, синтаксис-блок: `[--file <подпись> | подпись из stdin]`.**
   В блоке-схеме синтаксиса не видно, что `--file` без `--attach` — по-прежнему тело (это есть в
   §2.2 и §6, противоречия нет) — просто дублируемая семантика одного флага. Можно оставить, но
   при чтении §2.1 в отрыве возникает вопрос; одна оговорка «(семантика `--file` зависит от
   наличия `--attach`)» сняла бы его.
