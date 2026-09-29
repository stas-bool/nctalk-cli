# `nctalk` — поддержка тредов (`chat threads`, `--thread`, `--thread-title`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Дать `nctalk` читать и писать треды Nextcloud Talk: десятая команда `chat threads <room>`, флаг `--thread` у `chat show`, флаги `--thread`/`--thread-title` у `chat send`, аннотации `⟲N`/`↩id` в общем `chat show`.

**Architecture:** Дельта к базовому CLI по спеке `docs/superpowers/specs/2026-09-29-nctalk-threads-design.md` (дизайн утверждён; API сверен по исходникам spreed v24.0.4, capability `threads` подтверждена живым запросом 2026-09-29). Слои как у всех предыдущих дельт: `internal/client` (новый файл `threads.go`: `Thread`/`ThreadAttendee`/`ThreadInfo` + `ListThreads`; расширение `GetChatOpts`/`SendMessageOpts`/`Message` в `chat.go`/`types.go`), `internal/render` (`ThreadsTable`/`ThreadsJSON`, параметр показа `↩` у `MessagesTable`), `internal/cli` (новый `chatThreadsHandler`, флаги у show/send, метод в интерфейсе `TalkClient` + правка шести моков, роутинг, help-декларация). Фикстуры — ТОЛЬКО живые, снимаются curl-ом с боевого сервера ДО написания тестов (Task 1). Интеграционный мутационный `TestIntegration_Threads` за build-тегом `integration`.

**Tech Stack:** Go 1.21+, только stdlib. Новых зависимостей нет.

## Global Constraints

- **`CGO_ENABLED=0` обязательно** для всех `go build` / `go test` / `go vet` (macOS Tahoe 26.5 + Go 1.21.4: без него dyld-падение `missing LC_UUID`).
- Только stdlib; новых импортов вне stdlib не вводить.
- **Кириллические комментарии в существующем коде сохранять**; новые комментарии писать на русском в стиле окружающего файла (конвенция всего репозитория).
- **Коммиты: Conventional Commits обычным текстом, БЕЗ emoji** (`feat: ...`, не `✨ feat: ...`) и **БЕЗ какой-либо AI-атрибуции** — никакого `Co-Authored-By: Claude` (юридическое требование владельца репозитория, перекрывает дефолты ассистента).
- Секреты/личные URL в код, тесты, фикстуры и коммиты не допускать (публичный репозиторий `stas-bool/nctalk-cli`); фикстуры — обезличенные, реального формата эндпоинта. Пароль `NEXTCLOUD_PASS` никогда не выводить (не `echo`, не `set -x`).
- Эталон контракта — спека `2026-09-29-nctalk-threads-design.md`; базовый контракт exit-кодов — `2026-07-17-nctalk-cli-design.md` §7, без новых кодов: `0` успех (в т.ч. пустой список тредов), `1` общая (нецелое/неположительное `--thread`, пустой `--thread-title`, конфликт флагов, неизвестный флаг, сеть, 401, 400, 5xx), `2` not found (OCS 404, 0 совпадений `--name`), `3` неоднозначное `--name`.
- API-контракт (спека §3, сверено по исходникам spreed v24.0.4): список тредов `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/threads/recent?limit=<1..50>`; чтение треда — тот же `GET /chat/{token}` + query `threadId`; отправка — `POST /chat/{token}` с `threadId` (ответ без цитаты; несуществующий тред → **HTTP 400** `{"error":"reply-to"}`, НЕ 404) или `threadTitle` (создание треда). Заголовки OCS через существующий `doOCS`.
- Инварианты сервера (спека §1): id треда = id корневого сообщения; Thread-запись создаётся ТОЛЬКО непустым `threadTitle`; `replyTo` на сообщение треда кладёт ответ в тред; выдача `?threadId` включает корень; общая история не фильтрует ответы тредов.
- **Токен в путь БЕЗ предварительного `url.PathEscape`** в новом коде (`ListThreads`): эскейп path-сегмента выполняет транспорт, предварительный даёт двойной эскейп (инвариант `GetParticipants`, регресс `TestGetParticipants_PathEscape`). PathEscape в `chat.go`/`reactions.go` — легаси, НЕ копировать.
- `messageParameters` у chat-API — всегда **объект** (в `threads/recent` у `first`/`last` тоже); формат массива бывает только в `/v4/room` `lastMessage` (инвариант CLAUDE.md, тип `MsgParams` принимает оба).
- Правка каждого файла — с сохранением его стиля: ручной scan флагов без flag-пакета, handler-ы возвращают `ExitError`, текстовые таблицы через `newTabwriter` (кроме построчных в стиле `MessagesTable`).

## Риски и альтернативы

**Риски (по спеке; снятие — в задачах):**

1. **Формат `threads/recent` не документирован в официальных docs** (только исходники spreed v24.0.4, спека §1). Снятие — Task 1: живые фикстуры ДО тестов; jq-сверка фактических полей со спекой §3.1; при материальном расхождении (другие имена полей, нет `attendee`) — СТОП и пересогласование спеки, не подгонять фикстуры молча.
2. **`data:null` обнуляет слайс** (`json.Unmarshal("null")` → nil, а `RawMessage("null")` проходит len-guard doOCS) — прецедент nil-map у `GetReactions`/`GetParticipants`. Guard `out == nil → []ThreadInfo{}` обязателен в `ListThreads` (Task 2).
3. **Расширение интерфейса `cli.TalkClient` (`ListThreads`) ломает все шесть тестовых моков** — пакет `cli` не соберётся с тестами, пока метод не добавлен в каждый. Compile-правка — отдельный шаг Task 3 (Step 3), атомарным куском.
4. **Новый параметр сигнатуры `MessagesTable` роняет render-тесты** (`TestMessagesTable`, `TestMessagesTableNoReactions` зовут её напрямую) — механическая правка двух call-site-ов в том же шаге, что и смена сигнатуры (Task 3 Step 4). Отдельных render-кейсов на `↩`-параметр не заводится — поведение покрывается cli-тестами (спека §6).
5. **Префикс-коллизия `--thread`/`--thread-title`** в ручном scan: точные совпадения не конфликтуют, а `--thread=` НЕ матчит `--thread-title=X` (9-й символ `-`, не `=`), но кейсы `--thread-title` ставим ВЫШЕ кейсов `--thread` в switch — защитный порядок; `=`-формы покрыты тестами (Task 3 Step 1).
6. **Молчаливое игнорирование `threadId` серверами без capability `threads`** (show вернёт общую историю, send уйдёт не в тред) — осознанно НЕ проверяется клиентом (тонкий, out of scope спеки §1); документируется в CLAUDE.md (Task 6).
7. **Send в несуществующий тред = HTTP 400, не 404** (сверено по исходникам, спека §3.3) — ложится на существующий маппинг `exitFromClientErr` → exit 1; спецобработки нет.
8. **`--last` у `chat threads`: `N <= 0` = «не задан»** (дефолт 20), а не ошибка — отличие от интуиции «отрицательное = ошибка»; покрыто тестом (Task 3 Step 1, `TestChatThreadsLast`).

**Альтернативы (отклонены при брейншторме, спека §1 — не реализовать):**

- Отдельный ресурс `threads …` (вариант B) — дробит chat-команды по двум местам; принята надстройка над `chat`.
- «Не трогать вывод `chat show`» — теряется видимость обсуждений; приняты лёгкие пометки `⟲N`/`↩id`.
- Rename треда / notify-level / subscribed-threads (вариант C) — out of scope, дёшево добавить отдельной дельтой.
- Unread-индикация тредов — сервер не отдаёт per-thread read-marker.

---

### Task 1: Живые фикстуры с боевого сервера (curl, ДО написания тестов)

Спека §6 требует снять фикстуры с живого API ДО тестов (урок CLAUDE.md: самопальные фикстуры однажды скрыли баг). Env `NEXTCLOUD_URL`/`NEXTCLOUD_LOGIN`/`NEXTCLOUD_PASS` уже в окружении; комната — `NCTALK_INTEGRATION_ROOM` (integration-комната, та же, где работают мутационные тесты). **Пароль не выводить**: не `echo "$NEXTCLOUD_PASS"`, не `set -x` (docs/integration-run.md).

**Files:**
- Create: `testdata/threads_recent.json` (ответ `GET …/chat/{token}/threads/recent`, обезличенный)
- Create: `testdata/chat_thread_history.json` (ответ `GET …/chat/{token}?threadId=N`, обезличенный)
- Create: `testdata/chat_send_thread_reply.json` (ответ `POST …/chat/{token}` с `threadId`, обезличенный)

**Interfaces:**
- Consumes: env `NEXTCLOUD_URL`/`NEXTCLOUD_LOGIN`/`NEXTCLOUD_PASS`/`NCTALK_INTEGRATION_ROOM`; спека §3.1 (ожидаемая форма `TalkThreadInfo`).
- Produces (Task 2 завязан): три фикстуры с нормализованными значениями — корень id `1234`, ответ id `1300`, `lastMessageId` `1300`, `roomToken` `tok-team`, title `Фикстурный тред`, актёры `anna.s`/`Анна Смирнова` и `boris.k`/`Борис Крылов`; в `data` `threads_recent.json` — ровно ОДИН элемент, тред текущего прогона (чужие удалены, см. Step 3); `numReplies`/`lastActivity`/`timestamp` — фактические из захвата (тесты ассертят `>= 1`/структурные инварианты, не точные числа).

- [ ] **Step 1: Захват сырых ответов в /tmp** (одна сессия shell; скрипт НЕ сохранять в репо):

```sh
cd /Users/stas/Projects/My/NCCliClient
TOKEN="$NCTALK_INTEGRATION_ROOM"
API="$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/chat/$TOKEN"
# Заголовки OCS; -u берёт креды из env — вывод curl не содержит пароль.
H=(-u "$NEXTCLOUD_LOGIN:$NEXTCLOUD_PASS" -H 'OCS-APIRequest: true' -H 'Accept: application/json')

# 1. Создать тред: новое сообщение с threadTitle (Thread-запись создаётся ТОЛЬКО так).
curl -s "${H[@]}" -H 'Content-Type: application/json' \
  -d '{"message":"nctalk threads fixture — корень","threadTitle":"Фикстурный тред"}' \
  "$API" | jq '.ocs.data.id'
# → записать как ROOT_ID (использовать в следующих командах вместо плейсхолдера)

# 2. Ответ в тред без цитаты (threadId = id корня).
curl -s "${H[@]}" -H 'Content-Type: application/json' \
  -d "{\"message\":\"nctalk threads fixture — ответ\",\"threadId\":ROOT_ID}" \
  "$API" > /tmp/threads_send_reply.raw.json
jq '.ocs.data.id' /tmp/threads_send_reply.raw.json   # → REPLY_ID

# 3. Список тредов (фикстура 1).
curl -s "${H[@]}" "$API/threads/recent?limit=5" > /tmp/threads_recent.raw.json

# 4. Выдача треда (фикстура 2): корень + ответ.
curl -s "${H[@]}" "$API?threadId=ROOT_ID&limit=50" > /tmp/threads_history.raw.json

# 5. Общая история (для сверки §6: у корня threadId==id + threadReplies, у ответа threadId != id).
curl -s "${H[@]}" "$API?limit=20" > /tmp/threads_general.raw.json
```

- [ ] **Step 2: jq-сверка формата со спекой §3.1 (СТОП при расхождении)**:

```sh
# Структура элемента threads/recent: thread{id,roomToken,lastMessageId,numReplies,lastActivity,title},
# attendee{notificationLevel}, first, last; порядок по lastActivity убыванию.
jq '.ocs.data[0] | keys' /tmp/threads_recent.raw.json
jq '.ocs.data[0].thread | keys' /tmp/threads_recent.raw.json

# messageParameters у first/last — ОБЪЕКТ (инвариант CLAUDE.md: chat-API всегда объект).
jq '[.ocs.data[] | .first.messageParameters, .last.messageParameters] | map(type)' /tmp/threads_recent.raw.json
# → ["object","object",...] — если "array" где-то: СТОП, формат отличается от свереного.

# Общая история: у корня threadId == id и есть threadReplies/threadTitle; у ответа threadId != id.
jq '[.ocs.data[] | select(.id == ROOT_ID or .id == REPLY_ID) | {id, threadId, threadReplies, threadTitle}]' /tmp/threads_general.raw.json

# Выдача треда включает корень: id == ROOT_ID присутствует в данных.
jq '[.ocs.data[].id] | index(ROOT_ID)' /tmp/threads_history.raw.json
```

Если фактические имена полей отличаются от спеки §3.1 — остановиться и поднять вопрос (пересогласование спеки), самопальные «исправления» фикстур запрещены.

- [ ] **Step 3: Обезличить и нормализовать → три фикстуры в корневом `testdata/`** (конвенция репо; правка руками или jq-фильтром, структура и типы каждого поля — из сырого ответа без изменений):

Правила нормализации (значения — подменить, структуру — сохранить):

| Сырое значение | В фикстуре |
|---|---|
| id корневого сообщения | `1234` |
| id ответа | `1300` (везде: id сообщения, `lastMessageId`, `parent`/reference-поля если есть) |
| roomToken / token | `tok-team` |
| actorId автора корня | `anna.s` (displayName `Анна Смирнова`) |
| actorId автора ответа | `boris.k` (displayName `Борис Крылов`) |
| thread.title | `Фикстурный тред` |
| тексты сообщений | `nctalk threads fixture — корень` / `… — ответ` (уже свои) |
| numReplies, lastActivity, timestamp, notificationLevel, session-подобные id | фактические из захвата (не выдумывать) |

**В массиве `data` фикстуры `threads/recent` остаётся ТОЛЬКО элемент треда текущего прогона** (id 1234 — он первый по `lastActivity`, только что создан и получил ответ): `threads/recent?limit=5` вернёт до пяти последних тредов ОБЩЕЙ integration-комнаты, где работают и чужие прогоны (`TestIntegration_SendMessage`/`EditMessage` и будущие прогоны самого `TestIntegration_Threads`) — чужие названия/тексты/актёры в публичный репозиторий (`stas-bool/nctalk-cli`) попадать НЕ ДОЛЖНЫ, а grep Step 4 их не ловит (проверяет только URL-паттерны). Остальные элементы массива удалить целиком, не обезличивать. Побочный эффект — фикстура детерминированна (один элемент, наш).

Скелет ожидаемой формы `testdata/threads_recent.json` (полные объекты `first`/`last` — из захвата, это обычные сообщения chat-API со всеми их полями; `attendee` присутствует):

```json
{
  "ocs": {
    "meta": { "status": "ok", "statuscode": 200, "message": "OK" },
    "data": [
      {
        "thread": {
          "id": 1234,
          "roomToken": "tok-team",
          "lastMessageId": 1300,
          "numReplies": 1,
          "lastActivity": 1760000000,
          "title": "Фикстурный тред"
        },
        "attendee": { "notificationLevel": 0 },
        "first": { "...": "полное сообщение chat-API из захвата, обезличено" },
        "last": { "...": "полное сообщение chat-API из захвата, обезличено" }
      }
    ]
  }
}
```

`testdata/chat_thread_history.json` — OCS-конверт, `data` = массив из двух сообщений (корень 1234 и ответ 1300, оба с `threadId`/`threadReplies`/`threadTitle` как отдал сервер). `testdata/chat_send_thread_reply.json` — OCS-конверт, `data` = отправленный ответ (id 1300) как отдал сервер.

- [ ] **Step 4: Проверить валидность и чистоту фикстур**:

```sh
jq empty testdata/threads_recent.json testdata/chat_thread_history.json testdata/chat_send_thread_reply.json && echo OK
# В data — ТОЛЬКО тред текущего прогона (чужие треды integration-комнаты удалены,
# правило Step 3; защита от утечки чужих обсуждений в публичный репозиторий):
jq -e '.ocs.data | length == 1 and .[0].thread.id == 1234' testdata/threads_recent.json >/dev/null && echo OK
# Личные данные не просочились (имена/логины/URL сервера кроме nc-заглушек):
grep -RniE 'nextcloud\.|https?://[a-z0-9.-]+\.(org|com|ru|io)' testdata/threads_recent.json testdata/chat_thread_history.json testdata/chat_send_thread_reply.json || echo clean
# Сырые ответы удалить:
rm /tmp/threads_*.raw.json
```

Expected: два `OK` и `clean` (второй `OK` — именно jq-проверка единственного элемента; её отсутствие = чужие треды не вычищены).

- [ ] **Step 5: Commit**

```sh
git add testdata/threads_recent.json testdata/chat_thread_history.json testdata/chat_send_thread_reply.json
git commit -m "test(client): живые фикстуры тредов (threads/recent, chat?threadId, send-ответ)

Сняты curl-ом с боевого сервера (spreed 24.0.4) ДО написания тестов,
обезличены и нормализованы (корень 1234, ответ 1300). Сверено:
messageParameters у first/last — объект; в общей истории у корня
threadId==id + threadReplies, у ответа threadId корня. Спека-дельта
2026-09-29 §6."
```

---
### Task 2: client — модели тредов, `ListThreads`, `ThreadId`/`ThreadTitle`/`ThreadReplies` в GetChat/SendMessage

**Files:**
- Create: `internal/client/threads.go`
- Create: `internal/client/threads_test.go`
- Modify: `internal/client/chat.go` (`GetChatOpts` + `getChatPage` + `SendMessageOpts`/`sendMessageReq`/`SendMessage`; строки 37–50, 142–171, 173–247)
- Modify: `internal/client/types.go` (`Message` + два поля после `ThreadId`, строка 27)
- Modify: `internal/client/chat_test.go` (`chatReq`/`chatPagesServer` + два новых теста в конец)

**Interfaces:**
- Consumes: `c.doOCS(ctx, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error)` (`internal/client/client.go:80`); константа `pathChat` (`chat.go:22`); фикстуры Task 1; хелперы тестов `testCfg`/`wantAuth`/`newOCSServer`/`ocsBody` (`client_test.go`, `ocs_test.go`), `chatPagesServer`/`chatSendServer`/`genMessages` (`chat_test.go`).
- Produces (Task 3 завязан на эти имена):
  - `client.Thread` (json-теги `id, roomToken, lastMessageId, numReplies, lastActivity, title`), `client.ThreadAttendee` (`notificationLevel`), `client.ThreadInfo` (`thread, attendee, first, last`; `First`/`Last` — `*Message`, nullable);
  - `func (c *TalkClient) ListThreads(ctx context.Context, token string, limit int) ([]ThreadInfo, error)` — по пустоте non-nil пустой слайс, по ошибке `*client.OCSError`;
  - `client.GetChatOpts.ThreadId int` (0 = параметр не шлётся; >0 → query `threadId`);
  - `client.SendMessageOpts.ThreadId int`, `client.SendMessageOpts.ThreadTitle string` (в JSON-тело `omitempty`);
  - `client.Message.ThreadTitle string` (`json:"threadTitle,omitempty"`), `client.Message.ThreadReplies int` (`json:"threadReplies,omitempty"`).

- [ ] **Step 1: Написать падающие тесты** — новый файл `internal/client/threads_test.go` целиком:

```go
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// threadsTestServer поднимает httptest-сервер, отдавая содержимое указанной
// фикстуры на любой запрос (по образцу participantsTestServer). Фикстуры лежат
// в корневом testdata/ — путь ../../testdata/<fixture>.
func threadsTestServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// TestListThreads — фикстура реального формата (снята с живого сервера,
// Task 1): метод/путь/заголовки/query limit, декод всех полей Thread и
// first/last. Нормализованные значения фикстуры: корень 1234, ответ 1300,
// title «Фикстурный тред», roomToken tok-team.
func TestListThreads(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotOCS, gotAccept, gotLimit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		gotLimit = r.URL.Query().Get("limit")
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "threads_recent.json"))
		if err != nil {
			t.Errorf("read fixture: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}

	// Запрос: GET chat/{token}/threads/recent?limit=N + OCS-заголовки.
	if gotMethod != http.MethodGet {
		t.Errorf("method: got %q, want GET", gotMethod)
	}
	if want := "/ocs/v2.php/apps/spreed/api/v1/chat/tok-team/threads/recent"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
	}
	if gotLimit != "20" {
		t.Errorf("limit: got %q, want %q", gotLimit, "20")
	}
	if gotAuth != wantAuth() {
		t.Errorf("Authorization: got %q, want %q", gotAuth, wantAuth())
	}
	if gotOCS != "true" {
		t.Errorf("OCS-APIRequest: got %q, want \"true\"", gotOCS)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: got %q, want \"application/json\"", gotAccept)
	}

	// Декод: хотя бы один тред; первый — с нормализованными значениями фикстуры.
	if len(tsInfo) < 1 {
		t.Fatalf("len(tsInfo): got %d, want >= 1", len(tsInfo))
	}
	first := tsInfo[0]
	if first.Thread.Id != 1234 || first.Thread.RoomToken != "tok-team" ||
		first.Thread.LastMessageId != 1300 || first.Thread.Title != "Фикстурный тред" {
		t.Errorf("tsInfo[0].Thread: got %+v, want 1234/tok-team/1300/Фикстурный тред", first.Thread)
	}
	if first.Thread.NumReplies < 1 {
		t.Errorf("tsInfo[0].Thread.NumReplies: got %d, want >= 1 (фикстура снята после ответа в тред)", first.Thread.NumReplies)
	}
	if first.Thread.LastActivity <= 0 {
		t.Errorf("tsInfo[0].Thread.LastActivity: got %d, want > 0 (Unix-секунды)", first.Thread.LastActivity)
	}
	if first.Attendee.NotificationLevel < 0 || first.Attendee.NotificationLevel > 3 {
		t.Errorf("tsInfo[0].Attendee.NotificationLevel: got %d, want 0..3", first.Attendee.NotificationLevel)
	}
	if first.First == nil || first.Last == nil {
		t.Fatalf("tsInfo[0]: first/last должны декодироваться (в фикстуре не null); got first=%v last=%v", first.First, first.Last)
	}
	if first.First.Id != 1234 || first.Last.Id != 1300 {
		t.Errorf("tsInfo[0]: first.Id=%d last.Id=%d, want 1234/1300", first.First.Id, first.Last.Id)
	}
}

// TestListThreads_NullableFirstLast — first/last == null (корень истёк/удалён,
// пустой тред) → nil-указатели, НЕ ошибка декодирования. Контракт nullable из
// §3.1; живой захват null-случая не воспроизводим (корень не истёк) — тело
// собрано по этому контракту, структура элемента идентична живой фикстуре.
func TestListThreads_NullableFirstLast(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[` +
		`{"thread":{"id":1234,"roomToken":"tok-team","lastMessageId":1300,"numReplies":1,` +
		`"lastActivity":1760000000,"title":"Фикстурный тред"},` +
		`"attendee":{"notificationLevel":0},"first":null,"last":null}]}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(tsInfo) != 1 {
		t.Fatalf("len: got %d, want 1", len(tsInfo))
	}
	if tsInfo[0].First != nil || tsInfo[0].Last != nil {
		t.Errorf("first/last: got %v/%v, want nil-указатели при null", tsInfo[0].First, tsInfo[0].Last)
	}
}

// TestListThreads_PathEscape — token с пробелом эскейпится в path-сегменте
// при сериализации URL в transport.DoOCS (клиент НЕ делает предварительный
// url.PathEscape — тот дал бы двойной эскейп, инвариант GetParticipants):
// запрос уходит как /chat/tok%20team/threads/recent.
func TestListThreads_PathEscape(t *testing.T) {
	var gotEscaped string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscaped = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.ListThreads(context.Background(), "tok team", 20); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if want := "/chat/tok%20team/threads/recent"; !strings.HasSuffix(gotEscaped, want) {
		t.Errorf("escaped path: got %q, want suffix %q", gotEscaped, want)
	}
}

// TestListThreads_EmptyData — data:[] И data:null → пустой non-nil слайс,
// nil error (спека §4; «null» проходит len-guard doOCS и обнуляет слайс —
// guard переинициализирует, прецедент GetParticipants).
func TestListThreads_EmptyData(t *testing.T) {
	bodies := []string{
		`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`,
		`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":null}}`,
	}
	for _, body := range bodies {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		c := NewTalkClient(testCfg(ts.URL))
		tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
		if err != nil {
			t.Fatalf("data %q: got err %v, want nil (пусто — НЕ ошибка)", body, err)
		}
		if tsInfo == nil {
			t.Fatalf("data %q: got nil слайс, want non-nil пустой", body)
		}
		if len(tsInfo) != 0 {
			t.Errorf("data %q: len = %d, want 0", body, len(tsInfo))
		}
		ts.Close()
	}
}

// TestListThreads_NotFound — OCS 404 → *client.OCSError{Code:404}
// (cli-слой маппит в exit 2).
func TestListThreads_NotFound(t *testing.T) {
	ts := newOCSServer(t, 404, "room not found")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	_, err := c.ListThreads(context.Background(), "no-such", 20)
	if err == nil {
		t.Fatal("ListThreads: got nil err, want *OCSError{Code:404}")
	}
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != 404 {
		t.Fatalf("err: got %v, want *OCSError{Code:404}", err)
	}
}

// TestListThreads_MessageParamsObject — инвариант CLAUDE.md: у chat-API
// messageParameters — ОБЪЕКТ (в т.ч. у first/last в threads/recent); формат
// массива бывает только в /v4/room lastMessage. Декодер оба трактует без
// ошибки, поэтому проверяем по сырому JSON живой фикстуры (drift-guard).
func TestListThreads_MessageParamsObject(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "threads_recent.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Проверка нечувствительна к форматированию фикстуры: пробел после
	// двоеточия не гарантирован (ручная правка или jq-compact дадут
	// "messageParameters":{ без пробела) — сравниваем по строке без пробелов.
	s := strings.ReplaceAll(string(raw), " ", "")
	if strings.Contains(s, `"messageParameters":[]`) {
		t.Errorf("фикстура threads_recent: messageParameters-массив в chat-API запрещён (инвариант CLAUDE.md)")
	}
	if !strings.Contains(s, `"messageParameters":{`) {
		t.Errorf("фикстура threads_recent: ожидается объектный messageParameters у first/last (инвариант chat-API)")
	}
}

// TestChatThreadHistory_Decode — фикстура реального формата chat?threadId=N
// (спека §3.2/§6): корень в выдаче (threadId == id, threadReplies >= 1,
// threadTitle непуст), ответ несёт threadId корня (инварианты §1).
func TestChatThreadHistory_Decode(t *testing.T) {
	ts := threadsTestServer(t, "chat_thread_history.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok-team", GetChatOpts{Limit: 50, ThreadId: 1234})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	var root, reply *Message
	for i := range msgs {
		if msgs[i].ThreadId == msgs[i].Id && msgs[i].ThreadReplies >= 1 && root == nil {
			root = &msgs[i]
		}
		if msgs[i].ThreadId != msgs[i].Id && reply == nil {
			reply = &msgs[i]
		}
	}
	if root == nil {
		t.Fatal("в выдаче треда нет корня (threadId == id && threadReplies >= 1) — инвариант §1 нарушен")
	}
	if reply == nil {
		t.Fatal("в выдаче треда нет ответа (threadId != id)")
	}
	if reply.ThreadId != root.Id {
		t.Errorf("ответ: ThreadId=%d, want id корня %d", reply.ThreadId, root.Id)
	}
	if root.ThreadTitle == "" {
		t.Errorf("корень: ThreadTitle пуст (сервер должен загрузить объект треда, §3.4)")
	}
}

// TestMessage_ThreadFieldsOptional — threadTitle/threadReplies отсутствуют в
// JSON (quote-ответ без треда, §3.4) → нулевые значения; присутствуют →
// декодируются. omitempty — про сериализацию, тут проверяем десериализацию.
func TestMessage_ThreadFieldsOptional(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"id":7,"threadId":7,"message":"x"}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.ThreadTitle != "" || m.ThreadReplies != 0 {
		t.Errorf("поля отсутствуют: got ThreadTitle=%q ThreadReplies=%d, want \"\"/0", m.ThreadTitle, m.ThreadReplies)
	}

	var m2 Message
	if err := json.Unmarshal([]byte(`{"id":7,"threadId":7,"threadTitle":"Тема","threadReplies":3}`), &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m2.ThreadTitle != "Тема" || m2.ThreadReplies != 3 {
		t.Errorf("поля присутствуют: got ThreadTitle=%q ThreadReplies=%d, want Тема/3", m2.ThreadTitle, m2.ThreadReplies)
	}
}

// TestSendMessage_ThreadReplyResponse — фикстура реального ответа POST chat
// с threadId (Task 1): клиент разбирает только id, существующий разбор не
// меняется (§3.3 — новых полей разбирать не нужно).
func TestSendMessage_ThreadReplyResponse(t *testing.T) {
	ts := threadsTestServer(t, "chat_send_thread_reply.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	id, err := c.SendMessage(context.Background(), "tok-team", SendMessageOpts{
		Message:  "nctalk threads fixture — ответ",
		ThreadId: 1234,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id != 1300 {
		t.Errorf("id: got %d, want 1300 (нормализованный id ответа фикстуры)", id)
	}
}
```

- [ ] **Step 2: Дописать падающие тесты в `internal/client/chat_test.go`** — сначала расширить слепок запроса `chatReq` (строки 39–46) и его запись в `chatPagesServer`:

```go
// chatReq — слепок query+path одного запроса к chat-эндпоинту, для asserts.
type chatReq struct {
	path               string
	limit              string
	lookIntoFuture     string
	lastKnownMessageId string
	setReadMarker      string
	threadId           string
}
```

и в handler-е `chatPagesServer` (после `setReadMarker: q.Get("setReadMarker"),`):

```go
			threadId:          q.Get("threadId"),
```

Затем два теста в конец файла:

```go
// TestGetChat_ThreadId — ThreadId>0 → каждый запрос несёт query threadId;
// ThreadId=0 → параметр не шлётся вовсе (спека-дельта 2026-09-29 §3.2).
func TestGetChat_ThreadId(t *testing.T) {
	page := genMessages(1000, 991)

	ts, log := chatPagesServer(t, page, nil)
	defer ts.Close()
	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.GetChat(context.Background(), "tok", GetChatOpts{Limit: 20, ThreadId: 1234}); err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	for i, r := range log.snapshot() {
		if r.threadId != "1234" {
			t.Errorf("req[%d].threadId: got %q, want %q", i, r.threadId, "1234")
		}
	}

	ts2, log2 := chatPagesServer(t, page, nil)
	defer ts2.Close()
	c2 := NewTalkClient(testCfg(ts2.URL))
	if _, err := c2.GetChat(context.Background(), "tok", GetChatOpts{Limit: 20}); err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	for i, r := range log2.snapshot() {
		if r.threadId != "" {
			t.Errorf("req[%d].threadId: got %q, want пусто (параметр не шлётся при 0)", i, r.threadId)
		}
	}
}

// TestSendMessage_ThreadFields — тело POST содержит threadId/threadTitle
// только когда заданы (omitempty); комбинации флагов — НЕ здесь, это cli
// (спека-дельта §6).
func TestSendMessage_ThreadFields(t *testing.T) {
	ts, log := chatSendServer(t, 200, "OK", 1)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.SendMessage(context.Background(), "tok", SendMessageOpts{
		Message:     "m",
		ThreadId:    1234,
		ThreadTitle: "Тема",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	body := log.snapshot()[0].body
	if !strings.Contains(body, `"threadId":1234`) {
		t.Errorf("body: нет \"threadId\":1234: %q", body)
	}
	if !strings.Contains(body, `"threadTitle":"Тема"`) {
		t.Errorf("body: нет \"threadTitle\":\"Тема\": %q", body)
	}

	// Оба нулевые/пустые → поля отсутствуют (omitempty).
	ts2, log2 := chatSendServer(t, 200, "OK", 1)
	defer ts2.Close()
	c2 := NewTalkClient(testCfg(ts2.URL))
	if _, err := c2.SendMessage(context.Background(), "tok", SendMessageOpts{Message: "m"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	body2 := log2.snapshot()[0].body
	if strings.Contains(body2, "threadId") || strings.Contains(body2, "threadTitle") {
		t.Errorf("body: thread-поля должны отсутствовать при нулях: %q", body2)
	}
}
```

- [ ] **Step 3: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestListThreads|TestChatThreadHistory|TestMessage_ThreadFields|TestSendMessage_ThreadReplyResponse|TestGetChat_ThreadId|TestSendMessage_ThreadFields' -v`
Expected: FAIL на компиляции — `unknown field ThreadId in struct literal` (GetChatOpts/SendMessageOpts), `undefined: c.ListThreads`, `unknown field ThreadTitle` (Message). Это красная фаза.

- [ ] **Step 4: Реализовать `internal/client/threads.go`** — новый файл целиком:

```go
package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// threads.go — треды комнаты (спека-дельта 2026-09-29 §3–4):
// GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}/threads/recent.
// Своя группа эндпоинта — по аналогии с reactions.go/participants.go.
// Чтение треда (GET chat?threadId=) и отправка (threadId/threadTitle в теле
// POST chat) — те же эндпоинты, что GetChat/SendMessage, живут в chat.go.

// Thread — каноническое представление треда (спека-дельта §3.1). Id треда =
// id корневого сообщения: им же читают (--thread), на него же отвечают.
type Thread struct {
	Id            int    `json:"id"`            // = id корневого сообщения
	RoomToken     string `json:"roomToken"`
	LastMessageId int    `json:"lastMessageId"`
	NumReplies    int    `json:"numReplies"`
	LastActivity  int64  `json:"lastActivity"` // СЕКУНДЫ Unix
	Title         string `json:"title"`        // может быть пустым → fallback в render
}

// ThreadAttendee — подписка запросившего на тред (§3.1). В текстовый вывод не
// идёт (notify-level — out of scope), в --json — канонически.
type ThreadAttendee struct {
	NotificationLevel int `json:"notificationLevel"` // 0 default, 1 always, 2 mention, 3 never
}

// ThreadInfo — элемент выдачи threads/recent; first/last nullable (корень
// истёк/удалён). first/last — сообщения chat-API: у них messageParameters —
// ОБЪЕКТ (инвариант CLAUDE.md; формат массива бывает только в /v4/room
// lastMessage — тип MsgParams принимает оба).
type ThreadInfo struct {
	Thread   Thread         `json:"thread"`
	Attendee ThreadAttendee `json:"attendee"`
	First    *Message       `json:"first"`
	Last     *Message       `json:"last"`
}

// ListThreads возвращает список недавних тредов комнаты token (спека-дельта
// §3.1), порядок сервера — по lastActivity убыванию. limit — [1,50], клэмп
// выполняет cli-слой; метод не валидирует — тонкий клиент.
//
// Запрос: GET pathChat + "/" + token + "/threads/recent" — token кладётся в
// путь КАК ЕСТЬ, без предварительного url.PathEscape: эскейп path-сегмента
// выполняет транспорт при сборке URL (инвариант GetParticipants,
// participants.go: предварительный PathEscape даёт двойной эскейп, пробел →
// %2520, регресс-паттерн TestGetParticipants_PathEscape; PathEscape в
// chat.go/reactions.go — легаси-шаблон, новый код его не копирует).
//
// Пустой ответ (data:[] ИЛИ data:null) → пустой non-nil слайс, nil error
// (guard-прецедент GetParticipants: «null» проходит len-guard doOCS и
// обнуляет слайс — переинициализируем). OCS-ошибка — стандартная обработка
// doOCS → *OCSError (404 → exit 2 в cli).
func (c *TalkClient) ListThreads(ctx context.Context, token string, limit int) ([]ThreadInfo, error) {
	p := pathChat + "/" + token + "/threads/recent"
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))

	var out []ThreadInfo
	if _, err := c.doOCS(ctx, http.MethodGet, p, q, nil, false, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ThreadInfo{}
	}
	return out, nil
}
```

- [ ] **Step 5: Расширить `chat.go` и `types.go`**

`internal/client/types.go` — в `Message` после строки `ThreadId int` добавить два поля:

```go
	ThreadId            int           `json:"threadId"`
	ThreadTitle         string        `json:"threadTitle,omitempty"`   // только когда сервер загрузил объект треда (спека-дельта 2026-09-29 §3.4)
	ThreadReplies       int           `json:"threadReplies,omitempty"` // только когда сервер загрузил объект треда (§3.4)
	ExpirationTimestamp int           `json:"expirationTimestamp"`
```

`internal/client/chat.go`:

1. `GetChatOpts` — после `StopBeforeTs` добавить поле:

```go
	// ThreadId — фильтр «только тред» (id корневого сообщения, спека-дельта
	// 2026-09-29 §3.2): >0 → query-параметр threadId, выдача включает корень;
	// 0 = общая история (параметр не шлётся). Вся механика пагинации
	// наследуется без изменений.
	ThreadId int
```

2. `GetChat` — вызов `getChatPage` (строка 80) сменить на:

```go
		page, lastGiven, err := c.getChatPage(ctx, token, lastKnown, serverLimit, opts.ThreadId)
```

3. `getChatPage` — сигнатура и query:

```go
func (c *TalkClient) getChatPage(ctx context.Context, token string, lastKnown, serverLimit, threadId int) ([]Message, string, error) {
```

в теле, после `q.Set("setReadMarker", "0")` и блока `lastKnown`, добавить:

```go
	// threadId>0 — чтение конкретного треда (спека-дельта 2026-09-29 §3.2):
	// сервер фильтрует «id = threadId OR topmost_parent_id = threadId», корень
	// включён. При 0 параметр не шлётся — общая история.
	if threadId > 0 {
		q.Set("threadId", strconv.Itoa(threadId))
	}
```

(и дополнить doc-комментарий `getChatPage` строкой про `threadId>0 → query threadId`).

4. `SendMessageOpts` — после `ReferenceId` добавить:

```go
	// ThreadId — ответ в существующий тред БЕЗ цитаты (серверный параметр
	// threadId, спека-дельта 2026-09-29 §3.3; несуществующий тред → HTTP 400
	// {"error":"reply-to"}). 0 = не задан (omitempty).
	ThreadId int
	// ThreadTitle — создать тред: текст сообщения станет корневым, тема —
	// названием. Действует только при ReplyTo=0 и ThreadId=0 (клиент
	// гарантирует взаимоисключаемость сам, cli-слой). Пустая строка → поле
	// не попадает в JSON: сервер пустое значение игнорирует (тред не
	// создаётся), непустоту проверяет cli до сети.
	ThreadTitle string
```

5. `sendMessageReq` — после `ReferenceId`:

```go
	ThreadId    int    `json:"threadId,omitempty"`
	ThreadTitle string `json:"threadTitle,omitempty"`
```

(в doc-комментарий структуры дополнить: `threadId/threadTitle — только при заданных значениях (omitempty)`, спека-дельта §3.3).

6. `SendMessage` — маршал тела дополнить двумя полями:

```go
	body, err := json.Marshal(sendMessageReq{
		Message:     opts.Message,
		ReplyTo:     opts.ReplyTo,
		Silent:      opts.Silent,
		ReferenceId: opts.ReferenceId,
		ThreadId:    opts.ThreadId,
		ThreadTitle: opts.ThreadTitle,
	})
```

- [ ] **Step 6: Прогнать тесты пакета + vet, убедиться в зелёном**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestListThreads|TestChatThreadHistory|TestMessage_ThreadFields|TestSendMessage_ThreadReplyResponse|TestGetChat_ThreadId|TestSendMessage_ThreadFields' -v && CGO_ENABLED=0 go test ./internal/client/ && CGO_ENABLED=0 go vet ./internal/client/`
Expected: все PASS, vet чист. Существующие `TestGetChat_*` не задеты (новый параметр `getChatPage` прокинут, query не меняется при ThreadId=0).

- [ ] **Step 7: Commit**

```sh
git add internal/client/threads.go internal/client/threads_test.go internal/client/chat.go internal/client/chat_test.go internal/client/types.go
git commit -m "feat(client): ListThreads + ThreadId/ThreadTitle/ThreadReplies

Новый файл threads.go: Thread/ThreadAttendee/ThreadInfo и ListThreads
(GET chat/{token}/threads/recent; токен в путь без PathEscape — инвариант
GetParticipants; data:null → пустой non-nil слайс). GetChatOpts.ThreadId →
query threadId (выдача включает корень); SendMessageOpts.ThreadId/
ThreadTitle в тело omitempty; Message + threadTitle/threadReplies
omitempty. Тесты на живых фикстурах Task 1. Спека-дельта 2026-09-29 §3-4."
```

---
### Task 3: render + cli — `chatThreadsHandler`, флаги `--thread`/`--thread-title`, аннотации, интерфейс `TalkClient`, моки, handler-тесты

**Files:**
- Modify: `internal/render/render.go` (`ThreadsTable` + хелперы в конец; `MessagesTable` — новый параметр + суффикс тредов, строки 63–79)
- Modify: `internal/render/render_json.go` (`ThreadsJSON` в конец)
- Modify: `internal/render/render_test.go` (два call-site-а `MessagesTable`, механически)
- Modify: `internal/cli/cli.go` (интерфейс `TalkClient` + счётчик «9 методов» → «10», строки 16–29)
- Modify: `internal/cli/handlers_chat.go` (новый `chatThreadsHandler` в конец; `chatShowHandler` + `--thread`; `chatSendHandler` + `--thread`/`--thread-title`; шапка файла)
- Modify (моки): `internal/cli/cli_test.go` (`mockTalkClient`), `internal/cli/handlers_reactions_test.go` (`reactionsSpyClient`), `internal/cli/handlers_search_test.go` (`searchSpyClient`), `internal/cli/room_test.go` (`roomMockClient`), `internal/cli/handlers_rooms_test.go` (`roomsSpyClient`)
- Modify: `internal/cli/handlers_chat_test.go` (spy у `chatSpyClient` + новые тесты)

**Interfaces:**
- Consumes: `client.ThreadInfo`/`client.GetChatOpts.ThreadId`/`client.SendMessageOpts.ThreadId`/`ThreadTitle`/`client.Message.ThreadTitle`/`ThreadReplies` (Task 2); `ResolveRoom(ctx, client, positional, nameFlag, stderr) (string, error)` (`internal/cli/room.go`); `exitFromClientErr(err)`; `render.SubstituteParams(msg, params)` (`internal/render/params.go`); `render.FormatTime`.
- Produces (Task 4/5 завязаны):
  - `render.ThreadsTable(w io.Writer, ts []client.ThreadInfo) error`;
  - `render.ThreadsJSON(w io.Writer, ts []client.ThreadInfo) error`;
  - `render.MessagesTable(w io.Writer, msgs []client.Message, showThreadRefs bool) error` — сигнатура меняется (рендер `↩` только в общей истории);
  - `chatThreadsHandler(ctx, deps, args, jsonOut) ExitError` — имя для `routes["chat"]["threads"]` (Task 4);
  - метод `ListThreads` в интерфейсе `cli.TalkClient`.

- [ ] **Step 1: Написать падающие тесты** — правки `internal/cli/handlers_chat_test.go`.

Сначала расширить spy (поля — в struct `chatSpyClient` после EditMessage-блоков, метод — рядом с `EditMessage`):

```go
	// ListThreads-запись (chat threads, спека-дельта 2026-09-29)
	gotThreadsToken string
	gotThreadsLimit  int
	threadsCalls     int

	// ListThreads-результат
	threadsResult []client.ThreadInfo
	threadsErr    error
```

```go
func (m *chatSpyClient) ListThreads(_ context.Context, token string, limit int) ([]client.ThreadInfo, error) {
	m.threadsCalls++
	m.gotThreadsToken = token
	m.gotThreadsLimit = limit
	return m.threadsResult, m.threadsErr
}
```

Затем тесты — в конец файла:

```go
// -----------------------------------------------------------------------------
// chat threads (спека-дельта 2026-09-29)
// -----------------------------------------------------------------------------

// makeThreadInfo — конструктор ThreadInfo: full-вариант с first/last.
func makeThreadInfo(id int, title string, numReplies int, lastActivity int64,
	firstText, lastActor, lastText string) client.ThreadInfo {
	return client.ThreadInfo{
		Thread: client.Thread{
			Id:           id,
			RoomToken:    "TOK",
			NumReplies:   numReplies,
			LastActivity: lastActivity,
			Title:        title,
		},
		Attendee: client.ThreadAttendee{NotificationLevel: 0},
		First:    &client.Message{Id: id, ActorDisplayName: "Анна", Message: firstText, MessageType: "comment"},
		Last:     &client.Message{Id: id + 1, ActorDisplayName: lastActor, Message: lastText, MessageType: "comment"},
	}
}

// TestChatThreadsText — формат строки текстового вывода (§2.1):
// [id] время название ⟲N автор: превью; дефолт --last 20; один вызов клиента.
func TestChatThreadsText(t *testing.T) {
	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{
		makeThreadInfo(1234, "Отчёт за сентябрь", 5, 1760000000, "текст корня", "Вася", "цифры сверил"),
	}}
	deps := newChatDeps(spy)

	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.threadsCalls != 1 || spy.gotThreadsToken != "TOK" {
		t.Fatalf("ListThreads: got calls=%d token=%q, want 1 вызов с TOK", spy.threadsCalls, spy.gotThreadsToken)
	}
	if spy.gotThreadsLimit != 20 {
		t.Errorf("limit: got %d, want 20 (дефолт)", spy.gotThreadsLimit)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{"[1234]", "Отчёт за сентябрь", "⟲5", "Вася: цифры сверил"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout не содержит %q\nвывод=\n%s", want, out)
		}
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("строк: got %d, want 1\nвывод=\n%s", got, out)
	}
}

// TestChatThreadsFallbacks — фолбэки §2.1: пустой title → текст корневого
// сообщения; first == nil → «—»; last == nil → без превью (строка
// заканчивается ⟲N); ⟲0 печатается всегда (недавно созданный тред).
func TestChatThreadsFallbacks(t *testing.T) {
	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{
		{ // пустой title, есть first, нет last
			Thread: client.Thread{Id: 1100, NumReplies: 0, LastActivity: 1760000000, Title: ""},
			First:  &client.Message{Id: 1100, ActorDisplayName: "Анна", Message: "текст корня как название"},
		},
		{ // нет ни first, ни last
			Thread: client.Thread{Id: 1107, NumReplies: 2, LastActivity: 1760000001, Title: "Есть название"},
		},
	}}
	deps := newChatDeps(spy)

	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк: got %d, want 2\nвывод=\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "текст корня как название") || !strings.Contains(lines[0], "⟲0") {
		t.Errorf("пустой title → first.message; ⟲0 печатается; got %q", lines[0])
	}
	if !strings.Contains(lines[1], "Есть название") || !strings.Contains(lines[1], "—") {
		t.Errorf("nil first → «—»; got %q", lines[1])
	}
	if !strings.HasSuffix(lines[0], "⟲0") || !strings.HasSuffix(lines[1], "⟲2") {
		t.Errorf("нет last → строка заканчивается ⟲N без превью; got %q / %q", lines[0], lines[1])
	}
}

// TestChatThreadsSubstituteParams — плейсхолдеры {actor}/{file}/{mention-*}
// подставляются в текстовом режиме (фолбэк-название и превью); --json —
// оригинальный message (канонический ответ, §2.1).
//
// ВАЖНО: каждая ветка берёт СВЕЖИЙ литерал (newResult), не общий слайс.
// Handler в текстовом режиме мутирует First/Last ЧЕРЕЗ УКАЗАТЕЛИ (шаг 6:
// ts[i].First.Message = SubstituteParams(...)), поэтому слайс, переиспользованный
// вторым шпионом, уже нёс бы подставленные строки — JSON-ассерт ловил бы
// подмену вместо оригинала (ложное падение на ПРАВИЛЬНОЙ реализации; красная
// фаза Step 2 этого не видит — падение на компиляции). У chat show этот
// паттерн переиспользования безопасен: там подстановка идёт по value-копиям
// (filtered в chatShowHandler), а First/Last в ThreadInfo — указатели.
func TestChatThreadsSubstituteParams(t *testing.T) {
	newResult := func() []client.ThreadInfo {
		return []client.ThreadInfo{{
			Thread: client.Thread{Id: 1, NumReplies: 1, LastActivity: 1760000000, Title: ""},
			First: &client.Message{
				Id:      1,
				Message: "{actor} поделился файлом {file}",
				MessageParameters: client.MsgParams{
					"actor": client.MsgParam{Type: "user", Name: "Анна"},
					"file":  client.MsgParam{Type: "file", Name: "report.pdf"},
				},
			},
			Last: &client.Message{
				Id:               2,
				ActorDisplayName: "Борис",
				Message:          "упомянул {mention-user1}",
				MessageParameters: client.MsgParams{
					"mention-user1": client.MsgParam{Type: "user", Name: "Вера"},
				},
			},
		}}
	}

	spy := &chatSpyClient{threadsResult: newResult()}
	deps := newChatDeps(spy)
	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{"Анна поделился файлом report.pdf", "Борис: упомянул Вера"} {
		if !strings.Contains(out, want) {
			t.Errorf("подстановка: нет %q\nвывод=\n%s", want, out)
		}
	}

	// JSON — БЕЗ подстановки (свежий шпион со СВОИМИ First/Last — см. комментарий теста).
	deps2 := newChatDeps(&chatSpyClient{threadsResult: newResult()})
	ee = chatThreadsHandler(context.Background(), deps2, []string{"TOK"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("json: code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if raw := deps2.Stdout.(*bytes.Buffer).String(); !strings.Contains(raw, "{actor} поделился файлом {file}") {
		t.Errorf("JSON хранит оригинальный message; raw=%s", raw)
	}
}

// TestChatThreadsJSON — --json: канонический массив ThreadInfo (порядок
// сервера), first/last и attendee сохраняются.
func TestChatThreadsJSON(t *testing.T) {
	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{
		makeThreadInfo(1234, "Отчёт", 5, 1760000000, "корень", "Вася", "ответ"),
	}}
	deps := newChatDeps(spy)

	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).String()
	var got []client.ThreadInfo
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	if len(got) != 1 || got[0].Thread.Id != 1234 || got[0].Thread.NumReplies != 5 || got[0].Thread.Title != "Отчёт" {
		t.Fatalf("JSON: got %+v, want тред 1234/⟲5/Отчёт", got)
	}
	if got[0].First == nil || got[0].Last == nil {
		t.Fatalf("JSON: first/last должны сохраниться; got %+v", got[0])
	}
	if got[0].Attendee.NotificationLevel != 0 {
		t.Errorf("JSON: attendee.notificationLevel: got %d, want 0", got[0].Attendee.NotificationLevel)
	}
}

// TestChatThreadsEmpty — пустой список: текст → пустой stdout exit 0;
// JSON → канонический [] (не null) — отличие от rooms participants, §2.1.
func TestChatThreadsEmpty(t *testing.T) {
	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{}}
	deps := newChatDeps(spy)
	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("текст: code: got %d, want %d", ee.Code, ExitOK)
	}
	if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("текст: stdout: got %q, want пусто", buf.String())
	}

	spy2 := &chatSpyClient{threadsResult: []client.ThreadInfo{}}
	deps2 := newChatDeps(spy2)
	ee = chatThreadsHandler(context.Background(), deps2, []string{"TOK"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("json: code: got %d, want %d", ee.Code, ExitOK)
	}
	if raw := deps2.Stdout.(*bytes.Buffer).String(); strings.TrimSpace(raw) != "[]" {
		t.Fatalf("json: stdout: got %q, want []", raw)
	}
}

// TestChatThreadsLast — --last: дефолт 20; явное N; клэмп 50; N<=0 → не задан
// (дефолт 20, НЕ ошибка); нецелое → exit 1 (§2.1).
func TestChatThreadsLast(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantLimit int
		wantCode  int
	}{
		{"без --last → 20", []string{"TOK"}, 20, ExitOK},
		{"--last 5", []string{"TOK", "--last", "5"}, 5, ExitOK},
		{"--last=7 (форма =)", []string{"TOK", "--last=7"}, 7, ExitOK},
		{"--last 500 → клэмп 50", []string{"TOK", "--last", "500"}, 50, ExitOK},
		{"--last 0 → не задан → 20", []string{"TOK", "--last", "0"}, 20, ExitOK},
		{"--last -3 → не задан → 20", []string{"TOK", "--last", "-3"}, 20, ExitOK},
		{"--last abc → exit 1", []string{"TOK", "--last", "abc"}, 20, ExitGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{threadsResult: []client.ThreadInfo{}}
			deps := newChatDeps(spy)
			ee := chatThreadsHandler(context.Background(), deps, tc.args, false)
			if ee.Code != tc.wantCode {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantCode, ee.Err)
			}
			if tc.wantCode == ExitOK && spy.gotThreadsLimit != tc.wantLimit {
				t.Errorf("limit: got %d, want %d", spy.gotThreadsLimit, tc.wantLimit)
			}
		})
	}
}

// TestChatThreadsNameResolution — --name ветки: 1 совпадение → 0; >1 → 3
// (ListThreads не звался); 0 → 2 (тоже без вызова). Позиционный+--name —
// приоритет у позиционного с предупреждением.
func TestChatThreadsNameResolution(t *testing.T) {
	cases := []struct {
		name       string
		findResult []client.Room
		wantExit   int
		wantCalls  int
	}{
		{"1 совпадение → 0", []client.Room{{Type: 2, Token: "tok-1", DisplayName: "Команда"}}, ExitOK, 1},
		{">1 → 3", []client.Room{
			{Type: 2, Token: "tokA", DisplayName: "Команда X"},
			{Type: 2, Token: "tokB", DisplayName: "Команда Y"},
		}, ExitAmbiguous, 0},
		{"0 → 2", []client.Room{}, ExitNotFound, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{findRooms: tc.findResult, threadsResult: []client.ThreadInfo{}}
			deps := newChatDeps(spy)
			ee := chatThreadsHandler(context.Background(), deps, []string{"--name", "Команда"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantExit, ee.Err)
			}
			if spy.threadsCalls != tc.wantCalls {
				t.Fatalf("ListThreads вызовов: got %d, want %d", spy.threadsCalls, tc.wantCalls)
			}
		})
	}

	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{}}
	deps := newChatDeps(spy)
	ee := chatThreadsHandler(context.Background(), deps, []string{"tok-pos", "--name", "Команда"}, false)
	if ee.Code != ExitOK || spy.gotThreadsToken != "tok-pos" {
		t.Fatalf("конфликт: code=%d token=%q, want 0/tok-pos", ee.Code, spy.gotThreadsToken)
	}
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "приоритет у token") {
		t.Errorf("stderr: должен содержать предупреждение о конфликте; got %q", stderr)
	}
}

// TestChatThreadsErrors — OCS 404 → exit 2 с текстом сервера; неизвестный
// флаг → exit 1 без вызова клиента; пустой ввод → exit 1; лишние позиционные
// игнорируются.
func TestChatThreadsErrors(t *testing.T) {
	spy := &chatSpyClient{threadsErr: &client.OCSError{Code: 404, Message: "thread"}}
	deps := newChatDeps(spy)
	ee := chatThreadsHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitNotFound {
		t.Fatalf("404: code: got %d, want %d", ee.Code, ExitNotFound)
	}
	if ee.Err == nil || !strings.Contains(ee.Err.Error(), "thread") {
		t.Fatalf("404: err: got %v, want текст сервера", ee.Err)
	}

	spy2 := &chatSpyClient{}
	deps2 := newChatDeps(spy2)
	ee = chatThreadsHandler(context.Background(), deps2, []string{"TOK", "--no-such"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("unknown flag: code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if spy2.threadsCalls != 0 {
		t.Errorf("ListThreads не должен зваться; got %d", spy2.threadsCalls)
	}

	deps3 := newChatDeps(&chatSpyClient{})
	ee = chatThreadsHandler(context.Background(), deps3, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("empty input: code: got %d, want %d", ee.Code, ExitGeneric)
	}

	spy4 := &chatSpyClient{threadsResult: []client.ThreadInfo{}}
	deps4 := newChatDeps(spy4)
	ee = chatThreadsHandler(context.Background(), deps4, []string{"tok-main", "extra"}, false)
	if ee.Code != ExitOK || spy4.gotThreadsToken != "tok-main" {
		t.Fatalf("лишние позиционные: code=%d token=%q, want 0/tok-main", ee.Code, spy4.gotThreadsToken)
	}
}

// TestChatShowThread — --thread прокидывается в GetChatOpts.ThreadId (обе
// формы); без флага → 0; нецелое/0/отрицательное → exit 1 ДО сети (§2.2).
func TestChatShowThread(t *testing.T) {
	msgs := []client.Message{makeChatMsg(1234, "alice", "comment", "корень", chatFixedNow.Unix())}

	spy := &chatSpyClient{chatMsgs: msgs}
	deps := newChatDeps(spy)
	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--thread", "1234"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotOpts.ThreadId != 1234 {
		t.Errorf("ThreadId: got %d, want 1234", spy.gotOpts.ThreadId)
	}

	spy2 := &chatSpyClient{chatMsgs: msgs}
	deps2 := newChatDeps(spy2)
	ee = chatShowHandler(context.Background(), deps2, []string{"TOK", "--thread=77"}, false)
	if ee.Code != ExitOK || spy2.gotOpts.ThreadId != 77 {
		t.Fatalf("=форма: code=%d ThreadId=%d, want 0/77", ee.Code, spy2.gotOpts.ThreadId)
	}

	spy3 := &chatSpyClient{chatMsgs: msgs}
	deps3 := newChatDeps(spy3)
	chatShowHandler(context.Background(), deps3, []string{"TOK"}, false)
	if spy3.gotOpts.ThreadId != 0 {
		t.Errorf("без --thread: ThreadId: got %d, want 0", spy3.gotOpts.ThreadId)
	}

	for _, raw := range []string{"abc", "0", "-5"} {
		spyN := &chatSpyClient{chatMsgs: msgs}
		depsN := newChatDeps(spyN)
		ee := chatShowHandler(context.Background(), depsN, []string{"TOK", "--thread", raw}, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("--thread %s: code: got %d, want %d", raw, ee.Code, ExitGeneric)
		}
		if spyN.chatCalls != 0 {
			t.Errorf("--thread %s: GetChat не должен зваться; got %d calls", raw, spyN.chatCalls)
		}
	}
}

// TestChatShowThreadRender — режим --thread: ↩ НЕ печатается; ⟲N на корне
// при ThreadReplies > 0; ⟲0 подавляется (§2.2).
func TestChatShowThreadRender(t *testing.T) {
	root := makeChatMsg(1234, "alice", "comment", "корень", chatFixedNow.Unix())
	root.ThreadId = 1234
	root.ThreadReplies = 5
	root.ThreadTitle = "Отчёт"
	reply := makeChatMsg(1300, "bob", "comment", "ответ", chatFixedNow.Unix())
	reply.ThreadId = 1234
	reply.ThreadTitle = "Отчёт"
	spy := &chatSpyClient{chatMsgs: []client.Message{root, reply}}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK", "--thread", "1234"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "⟲5") {
		t.Errorf("корень должен нести ⟲5; вывод=\n%s", out)
	}
	if strings.Contains(out, "↩") {
		t.Errorf("режим --thread не должен печатать ↩; вывод=\n%s", out)
	}

	fresh := makeChatMsg(1400, "alice", "comment", "корень без ответов", chatFixedNow.Unix())
	fresh.ThreadId = 1400
	fresh.ThreadReplies = 0
	spy2 := &chatSpyClient{chatMsgs: []client.Message{fresh}}
	deps2 := newChatDeps(spy2)
	chatShowHandler(context.Background(), deps2, []string{"TOK", "--thread", "1400"}, false)
	if out2 := deps2.Stdout.(*bytes.Buffer).String(); strings.Contains(out2, "⟲") {
		t.Errorf("⟲0 должен подавляться; вывод=\n%s", out2)
	}
}

// TestChatShowThreadAnnotations — общая история (без --thread): ⟲N у корня
// (ThreadId==Id && ThreadReplies>0), ↩id у ответа (ThreadId!=Id &&
// ThreadTitle!=""), quote-ответ без треда и нетредовые — без пометок;
// суффикс после реакций тем же разделителем (§2.4).
func TestChatShowThreadAnnotations(t *testing.T) {
	root := makeChatMsg(1234, "alice", "comment", "Отчёт за сентябрь", chatFixedNow.Unix())
	root.ThreadId = 1234
	root.ThreadReplies = 5
	root.ThreadTitle = "Отчёт за сентябрь"
	root.Reactions = map[string]int{"👍": 2}

	reply := makeChatMsg(1300, "bob", "comment", "цифры сверил", chatFixedNow.Unix())
	reply.ThreadId = 1234
	reply.ThreadTitle = "Отчёт за сентябрь"

	quote := makeChatMsg(1310, "carol", "comment", "цитата без треда", chatFixedNow.Unix())
	quote.ThreadId = 1310 // нетредовое: threadId == собственный id; ThreadTitle пуст (Thread-записи нет)

	plain := makeChatMsg(1320, "dave", "comment", "просто сообщение", chatFixedNow.Unix())
	plain.ThreadId = 1320

	spy := &chatSpyClient{chatMsgs: []client.Message{root, reply, quote, plain}}
	deps := newChatDeps(spy)

	ee := chatShowHandler(context.Background(), deps, []string{"TOK"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "👍×2   ⟲5") {
		t.Errorf("корень: реакции затем ⟲5 тем же разделителем; вывод=\n%s", out)
	}
	if !strings.Contains(out, "↩1234") {
		t.Errorf("ответ треда должен нести ↩1234; вывод=\n%s", out)
	}
	if strings.Contains(out, "↩1310") || strings.Contains(out, "↩1320") {
		t.Errorf("quote-ответ/нетредовое сообщение не помечаются ↩; вывод=\n%s", out)
	}
	if !strings.Contains(out, "просто сообщение") {
		t.Errorf("нетредовое сообщение выводится; вывод=\n%s", out)
	}
}

// TestChatSendThreadFlags — --thread → SendMessageOpts.ThreadId;
// --thread-title → ThreadTitle; обе =формы; невалидный --thread → exit 1
// ДО сети (§2.3).
func TestChatSendThreadFlags(t *testing.T) {
	spy := &chatSpyClient{sendId: 77}
	deps := newChatSendDeps(spy, strings.NewReader("ответ в тред"))
	ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--thread", "1234"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotSendOpts.ThreadId != 1234 || spy.gotSendOpts.ThreadTitle != "" {
		t.Errorf("opts: got ThreadId=%d ThreadTitle=%q, want 1234/пусто", spy.gotSendOpts.ThreadId, spy.gotSendOpts.ThreadTitle)
	}

	spy2 := &chatSpyClient{sendId: 78}
	deps2 := newChatSendDeps(spy2, strings.NewReader("корень треда"))
	ee = chatSendHandler(context.Background(), deps2, []string{"TOK", "--thread-title", "Тема"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy2.gotSendOpts.ThreadTitle != "Тема" || spy2.gotSendOpts.ThreadId != 0 {
		t.Errorf("opts: got ThreadTitle=%q ThreadId=%d, want Тема/0", spy2.gotSendOpts.ThreadTitle, spy2.gotSendOpts.ThreadId)
	}

	spy3 := &chatSpyClient{sendId: 79}
	deps3 := newChatSendDeps(spy3, strings.NewReader("x"))
	chatSendHandler(context.Background(), deps3, []string{"TOK", "--thread=55"}, false)
	if spy3.gotSendOpts.ThreadId != 55 {
		t.Errorf("=форма --thread: got %d, want 55", spy3.gotSendOpts.ThreadId)
	}

	spy4 := &chatSpyClient{sendId: 80}
	deps4 := newChatSendDeps(spy4, strings.NewReader("x"))
	chatSendHandler(context.Background(), deps4, []string{"TOK", "--thread-title=Тема2"}, false)
	if spy4.gotSendOpts.ThreadTitle != "Тема2" {
		t.Errorf("=форма --thread-title: got %q, want Тема2", spy4.gotSendOpts.ThreadTitle)
	}

	for _, raw := range []string{"abc", "0", "-5"} {
		spyN := &chatSpyClient{sendId: 1}
		depsN := newChatSendDeps(spyN, strings.NewReader("x"))
		ee := chatSendHandler(context.Background(), depsN, []string{"TOK", "--thread", raw}, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("--thread %s: code: got %d, want %d", raw, ee.Code, ExitGeneric)
		}
		if spyN.sendCalls != 0 {
			t.Errorf("--thread %s: SendMessage не должен зваться; got %d calls", raw, spyN.sendCalls)
		}
	}
}

// TestChatSendThreadConflict — любые два из --reply-to/--thread/
// --thread-title → exit 1 ДО сети (spy: ноль вызовов SendMessage);
// пустой --thread-title → exit 1 до сети (§2.3).
func TestChatSendThreadConflict(t *testing.T) {
	conflicts := [][]string{
		{"--reply-to", "10", "--thread", "1234"},
		{"--reply-to", "10", "--thread-title", "Тема"},
		{"--thread", "1234", "--thread-title", "Тема"},
		{"--reply-to=10", "--thread=1234"},
	}
	for _, flags := range conflicts {
		spy := &chatSpyClient{sendId: 1}
		deps := newChatSendDeps(spy, strings.NewReader("x"))
		ee := chatSendHandler(context.Background(), deps, append([]string{"TOK"}, flags...), false)
		if ee.Code != ExitGeneric {
			t.Fatalf("%v: code: got %d, want %d", flags, ee.Code, ExitGeneric)
		}
		if ee.Err == nil || !strings.Contains(ee.Err.Error(), "взаимоисключаемы") {
			t.Fatalf("%v: err: got %v, want «взаимоисключаемы»", flags, ee.Err)
		}
		if spy.sendCalls != 0 {
			t.Fatalf("%v: SendMessage не должен зваться; got %d calls", flags, spy.sendCalls)
		}
	}

	for _, args := range [][]string{
		{"TOK", "--thread-title", ""},
		{"TOK", "--thread-title="},
	} {
		spy := &chatSpyClient{sendId: 1}
		deps := newChatSendDeps(spy, strings.NewReader("x"))
		ee := chatSendHandler(context.Background(), deps, args, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("%v: code: got %d, want %d", args, ee.Code, ExitGeneric)
		}
		if spy.sendCalls != 0 {
			t.Fatalf("%v: SendMessage не должен зваться; got %d calls", args, spy.sendCalls)
		}
	}
}
```

- [ ] **Step 2: Прогнать, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestChatThreads|TestChatShowThread|TestChatSendThread' -v`
Expected: FAIL на компиляции — `undefined: chatThreadsHandler` (метод-шпион `ListThreads` на `chatSpyClient` уже добавлен Step 1 и компилируется: `client.ThreadInfo` существует с Task 2; лишний метод у мока — легален). Это красная фаза.

- [ ] **Step 3: Добавить `ListThreads` в интерфейс `TalkClient` и стабы в пять моков** (без этого пакет `cli` не соберётся с тестами после Step 5, спека §6).

`internal/cli/cli.go` — в интерфейсе после `GetParticipants` добавить метод и поправить счётчик в комментарии (строка 16, «9 методов» → «10 методов» — правка из §5 спеки, по месту):

```go
// TalkClient — минимальный интерфейс, покрывающий 10 методов реального
// *client.TalkClient. Нужен для mockability: в production в Deps.Client
// кладётся *client.TalkClient, в тестах — заглушка.
type TalkClient interface {
	ListRooms(ctx context.Context, opts client.ListRoomsOpts) ([]client.Room, error)
	FindRooms(ctx context.Context, query, actorId string) ([]client.Room, error)
	SearchRooms(ctx context.Context, term string, limit int) ([]client.ConversationResult, error)
	GetChat(ctx context.Context, token string, opts client.GetChatOpts) ([]client.Message, error)
	SendMessage(ctx context.Context, token string, opts client.SendMessageOpts) (int, error)
	EditMessage(ctx context.Context, token string, messageId int, opts client.EditMessageOpts) (int, error)
	GetReactions(ctx context.Context, token string, messageId int) (map[string][]client.ReactionActor, error)
	GetParticipants(ctx context.Context, token string) ([]client.Participant, error)
	ListThreads(ctx context.Context, token string, limit int) ([]client.ThreadInfo, error)
	SearchMessages(ctx context.Context, term string, opts client.SearchMessagesOpts) ([]client.MessageResult, error)
}
```

Стаб `errMock` — в пять моков (у `chatSpyClient` — полноценный spy из Step 1). В каждый файл добавить по одному методу рядом с `GetParticipants`:

`internal/cli/cli_test.go` (`mockTalkClient`):

```go
func (m *mockTalkClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

`internal/cli/handlers_reactions_test.go` (`reactionsSpyClient`):

```go
func (m *reactionsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

`internal/cli/handlers_search_test.go` (`searchSpyClient`):

```go
func (m *searchSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

`internal/cli/room_test.go` (`roomMockClient`):

```go
func (m *roomMockClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

`internal/cli/handlers_rooms_test.go` (`roomsSpyClient`):

```go
func (m *roomsSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
```

Compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` (cli.go) остаётся как есть — она и ловит рассинхрон (Task 2 уже добавил метод реальному клиенту).

- [ ] **Step 4: Реализовать render-функции и сменить сигнатуру `MessagesTable`**

`internal/render/render.go`:

1. `MessagesTable` (строки 63–79) — заменить на версию с параметром + аннотациями:

```go
// MessagesTable выводит сообщения построчно (спека §6 chat show):
//
//	[id] время автор: текст   👍×2 ✅×1   ⟲5 / ↩1234
//
// Реакции-счётчики берутся из Message.Reactions БЕЗ N+1 (печать через range).
// Подстановка messageParameters НЕ выполняется здесь — это слой CLI;
// рендерится поле Message как есть.
//
// Аннотации тредов (спека-дельта 2026-09-29 §2.4), после реакций тем же
// разделителем "   ":
//   - корень треда (ThreadId == Id && ThreadReplies > 0) → ⟲<ThreadReplies>
//     — в ОБОИХ режимах (общая история и --thread); при 0 не печатается:
//     это ещё не обсуждение;
//   - ответ в треде (ThreadId != Id && ThreadTitle != "") → ↩<ThreadId> —
//     только в режиме общей истории (showThreadRefs=true); в выдаче --thread
//     все сообщения из одного треда, ссылка бессмысленна. Условие
//     ThreadTitle != "" = «Thread-запись загружена» (§3.4): quote-ответ без
//     треда не помечается (↩ вёл бы на 404).
//
// showThreadRefs — показывать ли ↩-ссылки (false в режиме --thread).
func MessagesTable(w io.Writer, msgs []client.Message, showThreadRefs bool) error {
	for _, m := range msgs {
		if _, err := fmt.Fprintf(w, "[%d] %s %s: %s%s\n",
			m.Id, FormatTime(m.Timestamp), m.ActorDisplayName, m.Message,
			threadSuffix(m, showThreadRefs)); err != nil {
			return err
		}
	}
	return nil
}

// threadSuffix — суффикс строки сообщения: реакции, затем аннотация треда
// (§2.4). ⟲ (U+27F2), ↩ (U+21A9) — в духе существующих ×/эмодзи реакций.
func threadSuffix(m client.Message, showThreadRefs bool) string {
	s := reactionsSuffix(m.Reactions)
	switch {
	case m.ThreadId == m.Id && m.ThreadReplies > 0:
		s += "   ⟲" + strconv.Itoa(m.ThreadReplies)
	case showThreadRefs && m.ThreadId != m.Id && m.ThreadTitle != "":
		s += "   ↩" + strconv.Itoa(m.ThreadId)
	}
	return s
}
```

2. В конец файла — `ThreadsTable` с хелперами:

```go
// ThreadsTable выводит список тредов построчно, без tabwriter — в стиле
// MessagesTable (спека-дельта 2026-09-29 §2.1), порядок сервера (по
// lastActivity убыванию). Разделители — ДВОЙНОЙ пробел, точно по образцу §2.1
// (эталон контракта; широкие разделители — визуальное сканирование колонок):
//
//	[1234] 2026-09-29 14:02:05  Отчёт за сентябрь  ⟲5  Вася: цифры сверил
//
// Подстановка плейсхолдеров НЕ выполняется здесь — это слой cli (симметрия
// шага 10 chatShowHandler); рендер получает готовый текст.
func ThreadsTable(w io.Writer, ts []client.ThreadInfo) error {
	for _, t := range ts {
		line := fmt.Sprintf("[%d] %s  %s  ⟲%d",
			t.Thread.Id, FormatTime(t.Thread.LastActivity), threadTitle(t), t.Thread.NumReplies)
		if t.Last != nil {
			line += "  " + threadLastPreview(t.Last)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// threadTitle — название треда для вывода: thread.title; при пустом (старые
// данные) — текст корневого сообщения (плейсхолдеры уже подставлены слоем
// cli); при nil-корне (истёк/удалён) — прочерк.
func threadTitle(t client.ThreadInfo) string {
	if t.Thread.Title != "" {
		return t.Thread.Title
	}
	if t.First != nil {
		return t.First.Message
	}
	return "—"
}

// threadLastPreview — превью последнего сообщения: "автор: текст"
// (system-фолбэк как в lastMessagePreview: пустой текст → systemMessage).
func threadLastPreview(m *client.Message) string {
	preview := m.Message
	if preview == "" {
		preview = m.SystemMessage
	}
	return fmt.Sprintf("%s: %s", m.ActorDisplayName, preview)
}
```

`internal/render/render_json.go` — в конец файла:

```go
// ThreadsJSON выводит []ThreadInfo как JSON-массив (спека-дельта
// 2026-09-29 §2.1, --json). Канонический ответ: оригинальные message +
// messageParameters (подстановку cli делает только в текстовом режиме).
// Пустой non-nil слайс печатается как [], не null.
func ThreadsJSON(w io.Writer, ts []client.ThreadInfo) error {
	return writeJSON(w, ts)
}
```

3. `internal/render/render_test.go` — два call-site-а `MessagesTable` (тесты `TestMessagesTable`, `TestMessagesTableNoReactions`), механически добавить `false`:

```go
	if err := MessagesTable(&buf, msgs, false); err != nil {
```

(поведение параметра `↩` покрывается cli-тестами Step 1, отдельные render-кейсы не заводятся — спека §6).

- [ ] **Step 5: Реализовать handler-ы** — правки `internal/cli/handlers_chat.go` (в шапке файла дополнить перечисление: «команд `chat show` …, `chat threads` (спека-дельта 2026-09-29) и `chat edit`»).

1. `chatThreadsHandler` — в конец файла:

```go
// chatThreadsHandler — реализация `chat threads <room> [--last N]` (спека-
// дельта 2026-09-29 §2.1, §5): список недавних тредов комнаты.
//
// Порядок шагов:
//  1. ручной scan флагов (--name, --last, обе формы; неизвестный --* → exit 1);
//  2. первый позиционный = room, лишние игнорируются — единообразно с chat show;
//  3. --last: нецелое → exit 1; N<=0 → не задан → дефолт 20; N>50 → клэмп 50
//     (серверный потолок int<1,50>; дефолт сервера 50 — наш ниже для
//     читаемого вывода, симметрия с chat show --last 20);
//  4. ResolveRoom — коды 1/2/3 сохраняются (positional приоритетен, при
//     конфликте предупреждение в stderr);
//  5. ListThreads(token, limit) — клэмп уже применён, клиент не валидирует;
//  6. в текстовом режиме подстановка плейсхолдеров {actor}/{file}/{mention-*}
//     в First/Last ДО рендера (render.SubstituteParams — тот же конвейер,
//     что у chat show шаг 10; JSON — оригинал, канонический ответ);
//  7. вывод render.ThreadsTable / render.ThreadsJSON по jsonOut; пустой
//     список → пустой stdout в тексте (поисковая семантика rooms list) и []
//     в JSON.
func chatThreadsHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1-2. Разбор флагов и позиционных: ручной scan (без flag-пакета, спека §3).
	var (
		positionals  []string
		nameFlag     string
		lastRaw      string
		lastExplicit bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat threads: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case a == "--last":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat threads: --last требует значение (целое)")}
			}
			lastRaw = args[i+1]
			lastExplicit = true
			i++
		case strings.HasPrefix(a, "--last="):
			lastRaw = strings.TrimPrefix(a, "--last=")
			lastExplicit = true
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat threads: неизвестный флаг %q", a)}
		default:
			// Первый позиционный — room (token, primary); лишние игнорируются.
			positionals = append(positionals, a)
		}
	}

	// 3. --last → limit.
	limit := 20
	if lastExplicit {
		n, err := strconv.Atoi(lastRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat threads: --last ожидает целое число, получено %q", lastRaw)}
		}
		if n > 0 {
			limit = n
			if limit > 50 {
				limit = 50
			}
		}
	}

	// 4. Разрешение <room>. ResolveRoom сам печатает кандидаты при
	// неоднозначности и возвращает ExitAmbiguous/ExitNotFound как ExitError.
	var positional string
	if len(positionals) > 0 {
		positional = positionals[0]
	}
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 5. Список тредов (клиент по контракту возвращает non-nil слайс для
	// «пусто»; guard — страховка детерминированного --json).
	ts, err := deps.Client.ListThreads(ctx, token, limit)
	if err != nil {
		// OCS 404 (комната не найдена) → exit 2; прочие (401/403/5xx/сеть) → 1.
		return exitFromClientErr(err)
	}
	if ts == nil {
		ts = []client.ThreadInfo{}
	}

	// 6. Подстановка плейсхолдеров в текстовом режиме: фолбэк-название
	// (first.message) и превью (last.message) рендерятся готовым текстом.
	if !jsonOut {
		for i := range ts {
			if ts[i].First != nil {
				ts[i].First.Message = render.SubstituteParams(ts[i].First.Message, ts[i].First.MessageParameters)
			}
			if ts[i].Last != nil {
				ts[i].Last.Message = render.SubstituteParams(ts[i].Last.Message, ts[i].Last.MessageParameters)
			}
		}
	}

	// 7. Вывод. Пустой список: текст → пустой stdout, exit 0; JSON → [].
	if jsonOut {
		if err := render.ThreadsJSON(deps.Stdout, ts); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if len(ts) == 0 {
			return ExitError{Code: ExitOK}
		}
		if err := render.ThreadsTable(deps.Stdout, ts); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}
```

2. `chatShowHandler` — флаг `--thread`. В блок var (шаг 1) добавить:

```go
		threadRaw      string
		threadSet      bool
```

В switch — после кейсов `--since`, ДО `case a == "--system":` (точные совпадения и `--thread=`-префикс не конфликтуют с `--thread-title`, которого здесь нет; порядок — защитный):

```go
		case a == "--thread":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat show: --thread требует значение (id треда)")}
			}
			threadRaw = args[i+1]
			threadSet = true
			i++
		case strings.HasPrefix(a, "--thread="):
			threadRaw = strings.TrimPrefix(a, "--thread=")
			threadSet = true
```

После парсинга `--last` (шаг 2) — парсинг и валидация (до сети, спека §2.2):

```go
	// 2.5. Парсинг --thread (спека-дельта 2026-09-29 §2.2): положительное
	// целое = id корневого сообщения; нецелое/0/отрицательное при явном
	// флаге → exit 1 ДО сети.
	var threadId int
	if threadSet {
		n, err := strconv.Atoi(threadRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat show: --thread ожидает целое число, получено %q", threadRaw)}
		}
		if n <= 0 {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat show: --thread ожидает положительное число, получено %d", n)}
		}
		threadId = n
	}
```

В шаге 6 — прокидывание в opts:

```go
	opts := client.GetChatOpts{
		Limit:        limit,
		StopBeforeTs: sinceTs,
		ThreadId:     threadId,
	}
```

В шаге 11 — третий аргумент `MessagesTable` (`↩` только в общей истории):

```go
		if err := render.MessagesTable(deps.Stdout, filtered, threadId == 0); err != nil {
```

3. `chatSendHandler` — флаги `--thread`/`--thread-title`. В блок var добавить:

```go
		threadRaw      string
		threadSet      bool
		threadTitle    string
		threadTitleSet bool
```

В switch — после кейсов `--reply-to=`, ДО `case a == "--silent":`; кейсы `--thread-title` — ВЫШЕ кейсов `--thread` (защитный порядок префиксов):

```go
		case a == "--thread-title":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --thread-title требует значение (тема)")}
			}
			threadTitle = args[i+1]
			threadTitleSet = true
			i++
		case strings.HasPrefix(a, "--thread-title="):
			threadTitle = strings.TrimPrefix(a, "--thread-title=")
			threadTitleSet = true
		case a == "--thread":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --thread требует значение (id треда)")}
			}
			threadRaw = args[i+1]
			threadSet = true
			i++
		case strings.HasPrefix(a, "--thread="):
			threadRaw = strings.TrimPrefix(a, "--thread=")
			threadSet = true
```

После парсинга `--reply-to` (шаг 2), ДО чтения тела (шаг 3) — парсинг и guard-ы (спека §2.3):

```go
	// 2.5. Парсинг --thread: положительное целое, иначе exit 1 ДО сети.
	var threadId int
	if threadSet {
		n, err := strconv.Atoi(threadRaw)
		if err != nil {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --thread ожидает целое число, получено %q", threadRaw)}
		}
		if n <= 0 {
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat send: --thread ожидает положительное число, получено %d", n)}
		}
		threadId = n
	}

	// 2.6. Guard'ы тредов ДО сети (спека-дельта §2.3): сервер молча
	// игнорирует лишние параметры — нельзя допустить молчаливой подмены
	// действия. Взаимоисключаемость --reply-to / --thread / --thread-title:
	// любые два одновременно → exit 1 (сообщение перечисляет конфликт).
	// Пустой --thread-title → exit 1 (сервер безымянные треды не создаёт,
	// пустое значение он игнорирует).
	var threadFlags []string
	if replyToSet {
		threadFlags = append(threadFlags, "--reply-to")
	}
	if threadSet {
		threadFlags = append(threadFlags, "--thread")
	}
	if threadTitleSet {
		threadFlags = append(threadFlags, "--thread-title")
	}
	if len(threadFlags) > 1 {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf(
			"chat send: %s взаимоисключаемы — укажите ровно один",
			strings.Join(threadFlags, " + "))}
	}
	if threadTitleSet && threadTitle == "" {
		return ExitError{Code: ExitGeneric, Err: errors.New("chat send: --thread-title не может быть пустым (сервер безымянные треды не создаёт)")}
	}
```

В шаге 5 — прокидывание в opts:

```go
	id, err := deps.Client.SendMessage(ctx, token, client.SendMessageOpts{
		Message:     text,
		ReplyTo:     replyTo,
		Silent:      silentFlag,
		ReferenceId: referenceId,
		ThreadId:    threadId,
		ThreadTitle: threadTitle,
	})
```

- [ ] **Step 6: Прогнать тесты — зелёный, затем весь репозиторий**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestChatThreads|TestChatShowThread|TestChatSendThread|TestChatShow|TestChatSend' -v`
Expected: все PASS (включая СУЩЕСТВУЮЩИЕ `TestChatShow*`/`TestChatSend*` — регресс на смену сигнатуры `MessagesTable` и порядок guard-ов).

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: все пакеты PASS, vet чист. Роутинг/help пока не знают о команде — `chat threads` ещё не в `routes`/`cmdSpecs`, это Task 4 (поэтому `TestRunRoutes*`/`TestCmdSpecs_*` не задеты).

- [ ] **Step 7: Commit**

```sh
git add internal/render/render.go internal/render/render_json.go internal/render/render_test.go \
  internal/cli/cli.go internal/cli/handlers_chat.go internal/cli/handlers_chat_test.go \
  internal/cli/cli_test.go internal/cli/handlers_reactions_test.go internal/cli/handlers_search_test.go \
  internal/cli/room_test.go internal/cli/handlers_rooms_test.go
git commit -m "feat(cli): chatThreadsHandler + --thread/--thread-title + аннотации тредов

chatThreadsHandler: scan флагов (--name/--last, клэмп 50), ResolveRoom,
ListThreads, подстановка плейсхолдеров в тексте (JSON — канонический),
пустой список -> пусто/exit 0 (текст) или [] (JSON). chat show: --thread
(валидация до сети, рендер без ↩, ⟲N на корне при >0). chat send:
--thread/--thread-title + guard взаимоисключаемости с --reply-to до сети.
render: ThreadsTable/ThreadsJSON; MessagesTable + параметр showThreadRefs
(⟲N после реакций у корня, ↩id у ответов в общей истории). ListThreads в
интерфейсе TalkClient (10 методов), стабы во все 6 моков. Спека-дельта
2026-09-29 §2, §4-6."
```

---
### Task 4: cli — роутинг + help-декларация + канон-тесты

**Files:**
- Modify: `internal/cli/cli.go` (`routes` + подсказка verb в `Run`)
- Modify: `internal/cli/help.go` (запись `cmdSpecs` после `chat send`; флаги у `chat show`/`chat send`; счётчик «9 команд» → «10»)
- Modify: `internal/cli/cli_test.go` (кейсы в `TestRunRoutesAllStubs`/`TestRunRoutesToCorrectHandler`, счётчик)
- Modify: `internal/cli/help_test.go` (`TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`, `TestIsValueFlag`, `valueForFlag`, `buildPositionalArgs`, счётчики)
- Test: `internal/cli/handlers_chat_test.go` (Run-level тест новой команды)

**Interfaces:**
- Consumes: `chatThreadsHandler` (Task 3); `installSpy(t, "chat/threads")` — существующий хелпер `cli_test.go` (работает для любого `resource/verb` из `routes`).
- Produces: `routes["chat"]["threads"]`; `cmdSpecs`-запись `["chat","threads"]`; флаги `--thread`/`--thread-title` в декларациях `chat show`/`chat send`; подсказка verb с `threads`; счётчики «10 команд/10 методов» честные.

- [ ] **Step 1: Обновить канон-тесты (сначала красные)**

`internal/cli/help_test.go`:

1. `TestCmdSpecs_OrderMatchesHelpOrder` — want-список, вставить `"chat threads"` после `"chat send"` (спека §5: позиция — после `chat send`, до `chat edit`):

```go
	want := []string{
		"rooms list", "rooms find", "rooms search", "rooms participants",
		"chat show", "chat send", "chat threads", "chat edit",
		"reactions get",
		"search",
	}
```

2. `TestCmdSpecs_FlagsExactly` — в map `want` добавить ключ и дополнить два существующих:

```go
		"chat threads":       {"--name", "--last"},
		"chat show":          {"--name", "--last", "--from", "--since", "--system", "--thread"},
		"chat send":          {"--name", "--reply-to", "--reference-id", "--silent", "--file", "--thread", "--thread-title"},
```

3. `TestIsValueFlag` — `--thread`/`--thread-title` принимают значение, дополнить список:

```go
	valueFlags := []string{"--name", "--type", "--last", "--from", "--since",
		"--user", "--file", "--reply-to", "--reference-id", "--limit",
		"--thread", "--thread-title"}
```

4. `valueForFlag` — кейс для `--thread` (гигиена валидного значения, НЕ условие зелёного: prong 1 проваливается только на «неизвестный флаг», а ошибка парсинга «x» как int тест проходит — но с валидным «1» handler доходит до ResolveRoom, семантика проверяется глубже; `--thread-title` проходит default-веткой «x» — непустое):

```go
	case "--thread":
		return "1"
```

5. `buildPositionalArgs` — кейс (консистентность с остальными командами, НЕ условие зелёного: scan флагов идёт до ResolveRoom, так что «неизвестный флаг» срабатывает раньше «укажите token» и prong-и зелёные даже без позиционных — но с валидным позиционным семантическая проверка handler-а не вырождается):

```go
	case "chat threads":
		return []string{"tok123"}
```

(строку `case "chat show", "chat send":` не трогать — у threads свой кейс).

6. Комментарии-счётчики (§5 спеки): строка 12 `TestCmdSpecs_CoverAllRoutes` — «ровно те же 9 команд» → «ровно те же 10 команд»; строка 323 `TestHandleHelp_DetailedContent` — «для каждой из 9 команд» → «для каждой из 10 команд».

`internal/cli/cli_test.go`:

1. `TestRunRoutesAllStubs` (строка 64) — комментарий «все 9 команд» → «все 10 команд» и кейс в таблицу после `{"chat send", ...}`:

```go
		{"chat threads", []string{"chat", "threads", "tok"}},
```

(mockTalkClient вернёт errMock → ExitGeneric=1 и текст в stderr — инвариант теста; handler реальный с Task 3.)

2. `TestRunRoutesToCorrectHandler` (строка 145) — кейс после `{"chat/send", ...}`:

```go
		{"chat/threads", []string{"chat", "threads", "TOK123"}, []string{"TOK123"}, ""},
```

`internal/cli/handlers_chat_test.go` — Run-level e2e в конец (в блок chat threads после `TestChatThreadsErrors`):

```go
// TestChatThreadsViaRun — `chat threads TOK123` через Run: роутинг, handler,
// вывод (end-to-end без сети).
func TestChatThreadsViaRun(t *testing.T) {
	spy := &chatSpyClient{threadsResult: []client.ThreadInfo{
		makeThreadInfo(1234, "Отчёт", 5, 1760000000, "корень", "Вася", "ответ"),
	}}
	deps := newChatDeps(spy)

	code := Run([]string{"chat", "threads", "TOK123"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	if spy.threadsCalls != 1 || spy.gotThreadsToken != "TOK123" {
		t.Fatalf("ListThreads: got calls=%d token=%q", spy.threadsCalls, spy.gotThreadsToken)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "[1234]") || !strings.Contains(out, "⟲5") {
		t.Fatalf("stdout: ожидается строка треда; got %q", out)
	}
}
```

- [ ] **Step 2: Прогнать, убедиться в падении канонов**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestCmdSpecs_OrderMatchesHelpOrder|TestCmdSpecs_FlagsExactly|TestRunRoutesAllStubs|TestRunRoutesToCorrectHandler|TestChatThreadsViaRun' -v`
Expected: FAIL. Прогон роняет панику в `TestChatThreadsViaRun` (nil-handler): restore у `installSpy` (кейс `chat/threads` в `TestRunRoutesToCorrectHandler`, файл `cli_test.go` идёт раньше `handlers_chat_test.go`) присваивает `routes["chat"]["threads"] = nil`, не удалив ключ, и `Run` зовёт nil-функцию; паника завершает тест-бинарник. При отдельном запуске (`-run TestCmdSpecs_OrderMatchesHelpOrder`) красный только он: `cmdSpecs len: got 9, want 10`. Паника и nil-остаток уйдут в Step 3 (роутинг появится). Явление то же, что было в плане rooms participants (Task 3 Step 2) — прецедент задокументирован.

- [ ] **Step 3: Добавить роутинг, подсказку verb и декларацию**

`internal/cli/cli.go`:

1. `routes` — в группу `"chat"` после `"send"`:

```go
	"chat": {
		"show":    chatShowHandler,
		"send":    chatSendHandler,
		"threads": chatThreadsHandler,
		"edit":    chatEditHandler,
	},
```

2. Подсказка verb в `Run` (строка 123) — дополнить список `threads` (§5 спеки):

```go
		fmt.Fprintf(deps.Stderr, "nctalk %s: ожидается verb (list/find/search/participants/show/send/threads/edit/get)\n", resource)
```

`internal/cli/help.go`:

1. Запись в `cmdSpecs` — после блока `chat send` (строки 108–123), перед `chat edit` (порядок слайса = порядок общего help):

```go
	{
		Path:        []string{"chat", "threads"},
		Short:       "список тредов комнаты",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--last", Value: "N", Desc: "количество тредов (по умолчанию 20, максимум 50)"},
		},
		Examples: []string{
			"nctalk chat threads abc123",
			"nctalk chat threads --name \"Команда\" --last 10",
		},
	},
```

2. `chat show` — флаг в конец списка Flags:

```go
			{Name: "--thread", Value: "<id>", Desc: "показать только тред (id корневого сообщения)"},
```

3. `chat send` — два флага после `--reply-to`:

```go
			{Name: "--thread", Value: "<id>", Desc: "ответ в тред без цитаты (взаимоисключаем с --reply-to/--thread-title)"},
			{Name: "--thread-title", Value: "\"<тема>\"", Desc: "создать тред: сообщение станет корневым"},
```

4. Комментарий-счётчик (строка 37, §5 спеки): «единый источник правды: 9 команд базового CLI» → «единый источник правды: 10 команд базового CLI».

- [ ] **Step 4: Прогнать cli-пакет и весь репозиторий**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v`
Expected: все PASS. `TestCmdSpecs_CoverAllRoutes`, `TestHandleHelp_DetailedContent`, `TestHandleHelp_ResourceContent`, `TestAntiDrift_HelpMatchesDeclaration`, `TestAntiDrift_HandlerMatchesDeclaration` подхватывают новую команду и флаги автоматически по декларации (§5 спеки). Если `TestAntiDrift_HandlerMatchesDeclaration` падает: prong 1 — проверить, что handler-ы действительно парсят заявленные флаги (Task 3 Step 5); prong 3 — что `--thread-title` отвергается `chat show`/`chat threads`, `--thread` — `chat threads` (ошибка «неизвестный флаг»; кейсы `--thread-title` стоят выше `--thread` в switch chatSendHandler, а в chatShowHandler/chatThreadsHandler `--thread*` не парсится вовсе).

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: всё PASS, vet чист.

- [ ] **Step 5: Ручная smoke-проверка help (верификация, не тест)**

```sh
CGO_ENABLED=0 go build -o /tmp/nctalk-smoke ./cmd/nctalk
/tmp/nctalk-smoke chat threads --help
/tmp/nctalk-smoke chat show --help | grep -A1 thread
/tmp/nctalk-smoke chat send --help | grep -A1 thread
/tmp/nctalk-smoke --help | grep threads
/tmp/nctalk-smoke chat 2>&1 || true
rm /tmp/nctalk-smoke
```

Expected: детальный help `nctalk chat threads — список тредов комнаты`, `Использование: nctalk chat threads <room> [flags]`, флаги `--name`/`--last`, `--json`, примеры; `chat show`/`chat send` показывают `--thread`/`--thread-title`; общий help содержит `chat threads` после `chat send`; подсказка без verb — `(list/find/search/participants/show/send/threads/edit/get)`.

- [ ] **Step 6: Commit**

```sh
git add internal/cli/cli.go internal/cli/help.go internal/cli/cli_test.go internal/cli/help_test.go internal/cli/handlers_chat_test.go
git commit -m "feat(cli): роутинг и help-декларация chat threads + каноны

routes[chat][threads], verb-подсказка, cmdSpecs после chat send; chat show
+= --thread, chat send += --thread/--thread-title. Каноны Order/
FlagsExactly/IsValueFlag/valueForFlag/buildPositionalArgs и Run-level
кейсы; счётчики 9->10. Спека-дельта 2026-09-29 §5-6."
```

---

### Task 5: Интеграционный `TestIntegration_Threads` (мутационный)

**Files:**
- Modify: `internal/client/integration_test.go` (новый тест после `TestIntegration_EditMessage`)

**Interfaces:**
- Consumes: `integrationClient(t)`, `integrationTimeout` (существующие хелперы файла); `c.SendMessage` с `ThreadId`/`ThreadTitle`, `c.ListThreads`, `c.GetChat` с `ThreadId` (Task 2).
- Produces: `TestIntegration_Threads` — мутационный сценарий e2e (создаёт сообщения в integration-комнате), защита та же, что у `TestIntegration_EditMessage`: `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM` (новых env не вводим).

- [ ] **Step 1: Написать тест** — дописать в конец `internal/client/integration_test.go` (сценарий из спеки §6):

```go
// TestIntegration_Threads — мутационный e2e-сценарий тредов (дизайн
// 2026-09-29 §6): создать тред (threadTitle) → ответить (threadId) →
// ListThreads (numReplies >= 1, title) → GetChat(threadId) видит корень и
// ответ (инвариант «выдача включает корень», §1) → общая история: корень
// несёт threadReplies >= 1, ответ несёт threadId корня (инвариант §1 на
// живом). Защита та же, что у TestIntegration_EditMessage:
// NCTALK_INTEGRATION_SEND=1 + NCTALK_INTEGRATION_ROOM (новых env не вводим).
// Мусор (корень + ответ) остаётся в тестовой комнате, убирается вручную.
func TestIntegration_Threads(t *testing.T) {
	if got := os.Getenv("NCTALK_INTEGRATION_SEND"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_SEND != 1 — пропускаю мутационный тест Threads")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропускаю Threads (нет целевой тестовой комнаты)")
	}

	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	uniq := strconv.FormatInt(time.Now().Unix(), 10)
	title := "Тред интеграции " + uniq

	// 1. Создание треда: новое сообщение с threadTitle → id корня = id треда.
	rootId, err := c.SendMessage(ctx, token, SendMessageOpts{
		Message:     "nctalk threads root " + uniq,
		ThreadTitle: title,
	})
	if err != nil {
		t.Fatalf("SendMessage(threadTitle): %v", err)
	}
	t.Logf("создан тред: корень id=%d, title=%q", rootId, title)

	// 2. Ответ в тред без цитаты (threadId = id корня).
	replyId, err := c.SendMessage(ctx, token, SendMessageOpts{
		Message:  "nctalk threads reply " + uniq,
		ThreadId: rootId,
	})
	if err != nil {
		t.Fatalf("SendMessage(threadId=%d): %v", rootId, err)
	}
	t.Logf("ответ в тред id=%d", replyId)

	// 3. ListThreads: тред присутствует, numReplies >= 1, title совпадает.
	threads, err := c.ListThreads(ctx, token, 50)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	found := false
	for _, ti := range threads {
		if ti.Thread.Id == rootId {
			found = true
			if ti.Thread.NumReplies < 1 {
				t.Errorf("тред %d: NumReplies=%d, want >= 1", rootId, ti.Thread.NumReplies)
			}
			if ti.Thread.Title != title {
				t.Errorf("тред %d: Title=%q, want %q", rootId, ti.Thread.Title, title)
			}
			break
		}
	}
	if !found {
		t.Errorf("тред %d не найден в threads/recent (len=%d)", rootId, len(threads))
	}

	// 4. Чтение треда: выдача включает корень и ответ (§1-инвариант).
	threadMsgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 200, ThreadId: rootId})
	if err != nil {
		t.Fatalf("GetChat(threadId=%d): %v", rootId, err)
	}
	var hasRoot, hasReply bool
	for _, m := range threadMsgs {
		switch m.Id {
		case rootId:
			hasRoot = true
		case replyId:
			hasReply = true
		}
	}
	if !hasRoot || !hasReply {
		t.Errorf("GetChat(threadId): корень=%v ответ=%v, want оба true (инвариант «выдача включает корень»)", hasRoot, hasReply)
	}

	// 5. Общая история: корень несёт threadReplies >= 1, ответ — threadId корня.
	history, err := c.GetChat(ctx, token, GetChatOpts{Limit: 200})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	var (
		seenRoot, seenReply bool
		rootReplies, replyTh int
	)
	for _, m := range history {
		switch m.Id {
		case rootId:
			seenRoot = true
			rootReplies = m.ThreadReplies
		case replyId:
			seenReply = true
			replyTh = m.ThreadId
		}
	}
	if !seenRoot || !seenReply {
		t.Fatalf("общая история: корень=%v ответ=%v, want оба true (Limit=200 мал?)", seenRoot, seenReply)
	}
	if rootReplies < 1 {
		t.Errorf("корень %d: ThreadReplies=%d, want >= 1", rootId, rootReplies)
	}
	if replyTh != rootId {
		t.Errorf("ответ %d: ThreadId=%d, want %d", replyId, replyTh, rootId)
	}
}
```

- [ ] **Step 2: Компиляция integration-сьюта + отсутствие в обычном прогоне**

Run: `CGO_ENABLED=0 go test -tags=integration ./internal/client/... -run TestIntegration_Threads -v` (без env-кредов — ожидаем SKIP, это доказывает компиляцию и корректный gate)
Expected: `--- SKIP: TestIntegration_Threads` (не FAIL).

Run: `CGO_ENABLED=0 go test ./...`
Expected: в выводе нет `TestIntegration_*` (build-тег работает).

- [ ] **Step 3: (При наличии живых кредов) Живой прогон**

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run TestIntegration_Threads
```

Expected: PASS. Тест воспроизводит шаги Task 1 (создание треда + ответ) — заодно сверить, что инварианты живого ответа совпадают с фикстурами (§1: корень в выдаче, threadId у ответа). Если сверка вскрывает расхождение с фикстурой — исправить фикстуру обезличенным реальным ответом (не тест подгонять под фикстуру).

- [ ] **Step 4: Commit**

```sh
git add internal/client/integration_test.go
git commit -m "test(client): интеграционный TestIntegration_Threads (мутационный)

Создание треда (threadTitle) -> ответ (threadId) -> ListThreads ->
GetChat(threadId) видит корень+ответ -> общая история несёт threadReplies
корня и threadId у ответа. Флаги те же: NCTALK_INTEGRATION_SEND=1 +
NCTALK_INTEGRATION_ROOM. Спека-дельта 2026-09-29 §6."
```

---

### Task 6: Сопроводительная документация (§5 спеки)

**Files:**
- Modify: `README.md` (секция `chat threads` после `chat send`; флаги в секциях `chat show`/`chat send`)
- Modify: `CLAUDE.md` (первый абзац: «9 команд» → «10 команд», дельта в перечне спек; перечень методов client; блок «Неочевидные инварианты» пополняется)
- Modify: `docs/integration-run.md` (env-таблица, batch-регулярка мутации, перечень мутационных, таблица тестов)
- Modify (файл ВНЕ репозитория, НЕ коммитится): `~/.claude/skills/nctalk/SKILL.md`

**Interfaces:**
- Consumes: контракт команд из Task 2–5 (вывод, флаги, exit-коды).
- Produces: документация, синхронная с кодом; счётчики 9→10 полностью вычищены.

- [ ] **Step 1: README — секция `chat threads`** после секции `chat send` (после строки «Вывод: `id` отправленного сообщения.», перед `### chat edit`):

```markdown
### `chat threads <room>` — список тредов

```sh
nctalk chat threads kytxaiyc               # последние треды (до 20)
nctalk chat threads --name "review team"   # комнату по имени
nctalk chat threads kytxaiyc --last 50     # до 50 (потолок сервера)
nctalk chat threads kytxaiyc --json        # канонический массив (thread/attendee/first/last)
```

Строки: `[id] время название ⟲N автор: превью`. `[id]` — id треда = id корневого сообщения: им же читают (`chat show --thread <id>`) и в нём отвечают (`chat send --thread <id>`). Пустое название (старые данные) → текст корневого сообщения. Пусто → exit `0` (текст) / `[]` (`--json`).
```

В секцию `chat show` — пример и строку описания (после строки с `--system`):

```sh
nctalk chat show kytxaiyc --thread 2927    # только тред 2927 (включая корневое сообщение)
```

```markdown
`--thread <id>` — выдача одного треда (id корневого сообщения, из `chat threads` или пометки `⟲N`); корень включён, пагинация/фильтры `--last`/`--from`/`--since`/`--system` работают как обычно.
```

В секцию `chat send` — примеры (после строки `--silent`) и абзац:

```sh
echo "ответ" | nctalk chat send kytxaiyc --thread 2927          # ответ в тред (без цитаты)
echo "текст" | nctalk chat send kytxaiyc --thread-title "Тема"  # создать тред (вывод — id корня)
```

```markdown
`--reply-to`/`--thread`/`--thread-title` взаимоисключаемы (конфликт → exit `1` до отправки). При создании треда вывод — id корневого сообщения = будущий id треда. `--reply-to` на сообщение треда кладёт ответ в тред с цитатой (сервер).
```

- [ ] **Step 2: CLAUDE.md — три правки**

1. Первый абзац (строка 5): «только примитивы (9 команд)» → «только примитивы (10 команд)»; в перечень дельт добавить: `2026-09-29-nctalk-threads-design.md` (`chat threads` + `--thread`/`--thread-title`; читай при любой правке поведения).

2. Секция «Архитектура», bullet `internal/client` — перечень методов дополнить честно: `ListRooms/FindRooms/SearchRooms/GetChat/SendMessage/EditMessage/GetReactions/GetParticipants/ListThreads/SearchMessages`.

3. Блок «Неочевидные инварианты» — добавить bullet (стиль блока):

```markdown
- **Треды** (спека-дельта 2026-09-29): id треда = id корневого сообщения — читать (`--thread`), отвечать (`--thread`) и ссылаться одним числом; Thread-запись создаётся ТОЛЬКО отправкой нового сообщения с непустым `threadTitle` (конвертировать существующее API не умеет); `replyTo` на сообщение треда кладёт ответ в тред (с цитатой) — сервер резолвит сам; выдача `chat?threadId=N` ВКЛЮЧАЕТ корень; общая история НЕ фильтрует ответы тредов (аннотации `⟲N`/`↩id` в `chat show` осмысленны; `↩` подавляется в `--thread`-режиме, условие `threadTitle != ""` = «Thread-запись загружена»); сервер без capability `threads` молча игнорирует `threadId` — клиент не проверяет (тонкий); `--reply-to`/`--thread`/`--thread-title` взаимоисключаемы (guard в cli до сети); send в несуществующий тред = HTTP 400 (`{"error":"reply-to"}`), НЕ 404 → exit `1`.
```

- [ ] **Step 3: `docs/integration-run.md` — четыре правки**

1. Таблица «Опциональное окружение»: строка `NCTALK_INTEGRATION_SEND` — «`1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`/`TestIntegration_Threads`.»; строка `NCTALK_INTEGRATION_ROOM` — «Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`/`Threads`.».

2. Первый абзац файла: «…кроме мутационных `TestIntegration_SendMessage` (отправка) и `TestIntegration_EditMessage` (правка)» → дополнить «…и `TestIntegration_Threads` (создание треда и ответа)».

3. Секция «Мутация»: регулярка и текст:

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(SendMessage|EditMessage|Threads)'
```

4. Таблица «Что проверяет каждый тест» — строка после `TestIntegration_EditMessage`:

```markdown
| `TestIntegration_Threads` | `POST /chat/{token}` + `threads/recent` + `GET /chat?threadId` | Создание треда (`threadTitle`) → ответ (`threadId`) → `ListThreads`: `numReplies >= 1`, title совпадает → `GetChat(threadId)`: корень и ответ в выдаче → общая история: `threadReplies >= 1` у корня, `threadId` корня у ответа. Мутация — под флагом. |
```

- [ ] **Step 4: SKILL.md — skill-обёртка `nctalk`** (файл `~/.claude/skills/nctalk/SKILL.md`, ВНЕ репозитория — не коммитить; без правки skill не узнает о новых командах, спека §5). Правки по якорям:

1. Первый абзац «Тонкий CLI: 9 примитивов (комнаты, чтение, отправка, правка, реакции, поиск, участники).» → «Тонкий CLI: 10 примитивов (комнаты, чтение, отправка, правка, реакции, поиск, участники, треды).»

2. Шпаргалка — новая строка после `| chat send <room> | ...` и правки двух соседних:

```markdown
| `chat show <room>` | история | `--last N` (≤200), `--from actorId`, `--since`, `--system`, `--thread id` (дочитать обсуждение) |
| `chat send <room>` | отправить | тело stdin/`--file`; `--reply-to id`, `--silent`, `--thread id` (ответ в тред), `--thread-title "Тема"` (создать) — взаимоисключаемы |
| `chat threads <room>` | треды комнаты | `[id] название ⟲N превью`; id = id корня → им читай (`--thread`) и отвечай (`send --thread`) |
```

3. Сценарий «2. Непрочитанные / дайджест» — после блока про активные за период добавить:

```markdown
Треды: пометки `⟲N` (у корня) и `↩id` (у ответов) в `chat show` — живые обсуждения. Дочитать: `nctalk chat show <token> --thread <id> --json`; список тредов: `nctalk chat threads <token> --json`. Ответ в тред: `printf '%s' "..." | nctalk chat send <token> --thread <id> --json`.
```

- [ ] **Step 5: Проверить отсутствие оставшихся счётчиков «9»**

```sh
grep -rn "9 команд\|9 методов\|9 примитивов" --include="*.go" --include="*.md" --exclude-dir=superpowers . ~/.claude/skills/nctalk/SKILL.md
```

Expected: пусто (счётчики обновлены: cli.go — Task 3 Step 3; help.go, cli_test.go, help_test.go — Task 4; CLAUDE.md, SKILL.md — этот таск; строки в docs/superpowers — исторические спеки/планы, не трогать). Каталог исключается по ПУТИ (`--exclude-dir`, работает на BSD/GNU grep и ugrep): контентный фильтр `grep -v "docs/superpowers"` давал ложное «пусто» — строка CLAUDE.md:5 сама упоминает `docs/superpowers/specs/…` (перечень спек) и отфильтровывалась бы вместе с забытым счётчиком «9 команд»; якорный `grep -v '^\./docs/superpowers/'` тоже не годится — grep на этой машине ugrep, пути без префикса `./` (проверено прогоном на дереве).

- [ ] **Step 6: Финальный полный прогон**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: всё зелёное. Плюс compile-проверка integration-сьюта: `CGO_ENABLED=0 go test -tags=integration ./internal/client/... -run TestIntegration_Threads` → SKIP без env-кредов.

- [ ] **Step 7: Commit** (только файлы репозитория — SKILL.md вне git)

```sh
git add README.md CLAUDE.md docs/integration-run.md
git commit -m "docs: поддержка тредов — README/CLAUDE/integration-run

README: секция chat threads + --thread в chat show + --thread/
--thread-title в chat send. CLAUDE.md: 10 команд, инварианты тредов.
integration-run: TestIntegration_Threads в мутационные и таблицу.
Спека-дельта 2026-09-29 §5."
```

---

## Definition of Done (сводка проверок)

- `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` — зелёные; integration-сьюта компилируется (`-tags=integration`), без тега в прогон не попадает.
- `chat threads <token>` / `--name` / `--last` (дефолт 20, клэмп 50) / `--json` работают; пустой список → пусто/exit 0 (текст), `[]` (JSON); `chat show --thread <id>` фильтрует тред с корнем; `chat send --thread`/`--thread-title` работают, конфликт с `--reply-to` → exit 1 до сети; аннотации `⟲N`/`↩id` в общем `chat show`, `↩` подавлен в `--thread`-режиме; exit-коды 0/1/2/3 по таблице спеки §2.
- Все канон-тесты (`TestCmdSpecs_*`, `TestRunRoutes*`, `TestAntiDrift_*`, `TestHandleHelp_*`) зелёные; счётчики «10 команд/10 методов» честные; `nctalk chat threads --help` показывает команду на месте (после `chat send`).
- Фикстуры `testdata/threads_recent.json`, `testdata/chat_thread_history.json`, `testdata/chat_send_thread_reply.json` — обезличенные живые ответы (Task 1, ДО тестов), в testdata-тестах используется реальный формат (messageParameters — объект).
- `TestIntegration_Threads` проходит на живом сервере с `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM` (мутация — мусор в integration-комнате убирается вручную).
- README/CLAUDE.md/docs/integration-run.md синхронны; `~/.claude/skills/nctalk/SKILL.md` обновлён (вне репо); `grep "9 команд\|9 методов\|9 примитивов"` вне docs/superpowers — пусто.
