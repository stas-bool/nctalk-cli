# Интеграционные тесты nctalk (`-tags=integration`)

Интеграционные тесты идут в отдельном файле `internal/client/integration_test.go`
за build-тегом `integration` и **НЕ входят** в обычный прогон `go test ./...`
(так задумано — их запускает человек на боевом сервере с реальными кредами).

Все сценарии читающие и безопасные, кроме мутационных `TestIntegration_SendMessage`
(отправка) и `TestIntegration_EditMessage` (правка) — они меняют состояние чата
и защищены отдельным флагом.

## Обязательные окружение

| Переменная           | Назначение                                          |
| -------------------- | --------------------------------------------------- |
| `NEXTCLOUD_URL`      | База сервера Nextcloud, напр. `https://nc.example.org` (схема и host обязательны, без userinfo и trailing slash). |
| `NEXTCLOUD_LOGIN`    | Логин пользователя Nextcloud.                       |
| `NEXTCLOUD_PASS`     | App-password пользователя (НЕ основной пароль аккаунта). |

При отсутствии любого из них тесты **скипаются** через `t.Skip` (не падают) —
это нормально для машины без кредов.

## Опциональное окружение

| Переменная             | По умолчанию | Назначение                                                      |
| ---------------------- | ------------ | -------------------------------------------------------------- |
| `NEXTCLOUD_TIMEOUT`    | `30s`        | HTTP-таймаут клиента (Go duration: `45s`, `1m`, `90s`, и т.д.). |
| `NCTALK_INTEGRATION_SEND`  | (ноль)   | `1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`. |
| `NCTALK_INTEGRATION_ROOM`  | (пусто)  | Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`. |

> Креды **никогда** не попадают в вывод/ошибки/логи тестов — санитайз на
> стороне `internal/client` (спека §5, §9). Не подсуньте их в `git`-конфиг
> или скрипты с `set -x`.

## Запуск

Все команды — с `CGO_ENABLED=0` (macOS Tahoe 26.5 + Go 1.21.4: dyld-проблема
CGO). Тег `-tags=integration` обязателен — без него файл просто не
компилируется.

### Все читающие сценарии

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(ListRooms|SearchRooms|GetChat|SearchMessages|GetReactions|GetParticipants)'
```

### По одному сценарию

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_ListRooms
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_SearchRooms
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetChat
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_SearchMessages
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetReactions
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetParticipants
```

### Мутация (отправка и правка сообщений)

**Только в тестовую комнату.** Двухслойная защита: флаг + token.

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(SendMessage|EditMessage)'
```

Без `NCTALK_INTEGRATION_SEND=1` или без `NCTALK_INTEGRATION_ROOM` тесты
молча скипаются.

### Проверка, что integration не входит в CI-прогон

```sh
CGO_ENABLED=0 go test ./...   # без -tags=integration
```

В выводе не должно быть `TestIntegration_*` — это гарантирует build-тег в
шапке `integration_test.go`.

## Безопасность сценариев

- **Читающие** (`ListRooms`, `SearchRooms`, `GetChat`, `SearchMessages`,
  `GetReactions`, `GetParticipants`) — GET-запросы, состояние сервера не меняют.
  Безопасны для прогона на production-аккаунте.
- **Мутация** (`SendMessage`/`EditMessage`) — POST/PUT в чат; выполняются только
  в токен, заданный в `NCTALK_INTEGRATION_ROOM`, и только при
  `NCTALK_INTEGRATION_SEND=1`. Удалять тестовые сообщения нужно вручную — тесты
  этого не делают (спека §12 „мутация, не проверено“).

## Что проверяет каждый тест

| Тест                        | Эндпоинт                                        | Контракт                                                              |
| --------------------------- | ----------------------------------------------- | --------------------------------------------------------------------- |
| `TestIntegration_ListRooms` | `/ocs/v2.php/apps/spreed/api/v4/room`           | `len >= 1`; для type=1 комнаты проверяется непустой `ActorId`.        |
| `TestIntegration_SearchRooms` | Unified `talk-conversations`                  | Безошибочный ответ; для каждого результата — непустой `Title`.        |
| `TestIntegration_GetChat`   | `/ocs/v2.php/apps/spreed/api/v1/chat/{token}`   | `Limit=5` → `len <= 5`; маппинг `Message.Token` и `Timestamp > 0`.    |
| `TestIntegration_SearchMessages` | Unified `talk-message`                     | Безошибочный ответ; `Attributes.Timestamp` — валидный int64 (`>= 0`). |
| `TestIntegration_GetReactions` | `/ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{id}` | Non-nil map; если реакции есть — у каждой есть актёры.      |
| `TestIntegration_GetParticipants` | `/ocs/v2.php/apps/spreed/api/v4/room/{token}/participants` | `>= 1` участник; непустые `actorId`/`actorType`; есть `participantType=1` (владелец). |
| `TestIntegration_SendMessage` | POST `/chat/{token}`                          | Возвращает `id > 0`. Мутация — под флагом.                            |
| `TestIntegration_EditMessage` | PUT `/chat/{token}/{messageId}`             | `parent.id == id`; `GetChat` видит новый текст. Мутация — под флагом. |
