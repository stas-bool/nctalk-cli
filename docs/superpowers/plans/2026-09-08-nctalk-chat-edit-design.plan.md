# `nctalk chat edit` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить в `nctalk` восьмую команду `chat edit <room> <messageId>` — правку уже отправленного сообщения (PUT chat-API), симметричную `chat send`.

**Architecture:** Дельта к базовому CLI по спеке `docs/superpowers/specs/2026-09-08-nctalk-chat-edit-design.md` (утверждённый дизайн). Три слоя затрагиваются как у всех существующих команд: `internal/client` (метод `EditMessage` над `doOCS`), `internal/cli` (handler + интерфейс `TalkClient` + роутинг + help-декларация `cmdSpecs`), интеграционный тест за build-тегом. `render` не расширяется — переиспользуются `NewMessageID`/`NewMessageIDJSON`.

**Tech Stack:** Go 1.21+, только stdlib. Новых зависимостей нет.

## Global Constraints

- **`CGO_ENABLED=0` обязательно** для всех `go build` / `go test` / `go vet` (macOS Tahoe 26.5 + Go 1.21.4: без него dyld-падение `missing LC_UUID`).
- Только stdlib; новых импортов вне stdlib не вводить.
- **Кириллические комментарии в существующем коде сохранять**; новые комментарии писать на русском в стиле окружающего файла (это конвенция всего репозитория).
- **Коммиты: Conventional Commits обычным текстом, БЕЗ emoji** (`feat: ...`, не `✨ feat: ...`) и **БЕЗ какой-либо AI-атрибуции** — никакого `Co-Authored-By: Claude` (юридическое требование владельца репозитория, перекрывает дефолты ассистента).
- Секреты/личные URL в код, тесты и коммиты не допускать (публичный репозиторий `stas-bool/nctalk-cli`).
- Эталон контракта — спека `2026-09-08-nctalk-chat-edit-design.md`; контракт команд/exit-кодов базового CLI — `2026-07-17-nctalk-cli-design.md` §7. Exit-коды не меняются: `0` успех (в т.ч. `202 Accepted`), `1` общая, `2` not found (OCS 404 / 0 совпадений `--name`), `3` неоднозначное `--name`.
- API-контракт (проверено по официальной документации Talk «Editing a chat message»): `PUT /ocs/v2.php/apps/spreed/api/v1/chat/{token}/{messageId}`, тело `{"message": "..."}`, ответ `200`/`202`, `ocs.data` — системное сообщение `message_edited`, в поле `parent` — обновлённое сообщение; клиент разбирает только `parent.id`.

## Отклонение от буквы спеки (зафиксировано)

Спека §6 говорит «Фикстура в `internal/client/testdata/`», но **фактическая конвенция репозитория — корневой `testdata/`** (рядом с go.mod): там лежат все фикстуры эндпоинтов (`rooms_list.json`, `reactions.json`, `search_*.json`), тесты `internal/client` читают их как `../../testdata/<file>` (см. `internal/client/rooms_test.go:13-18`). План кладёт фикстуру в корневой `testdata/chat_edit_response.json` — это соответствует духу правила базовой спеки («фикстуры обязаны отражать реальный формат каждого эндпоинта») и DRY с существующими тестами. Директорию `internal/client/testdata/` не создавать.

Спека §4 требует «`url.PathEscape` обоих сегментов» пути; план эскейпит только token, а `messageId` форматирует через `strconv.Itoa` — целое не содержит символов, которые PathEscape изменил бы (поведение байт-в-байт то же, что в букве спеки). Причина: PathEscape над заведомо цифровым сегментом — шум; rationale зафиксирован комментарием в коде (Task 1 Step 4).

---

### Task 1: client — метод `EditMessage` + фикстура реального формата

**Files:**
- Create: `testdata/chat_edit_response.json` (корневой `testdata/`)
- Modify: `internal/client/chat.go` (новый код после `SendMessage`, ~строка 247)
- Test: `internal/client/chat_test.go` (дописать в конец)

**Interfaces:**
- Consumes: `c.doOCS(ctx, method, path, query, body, mutate, out)` — существующий транспорт; `pathChat` — существующая константа `/ocs/v2.php/apps/spreed/api/v1/chat`; хелперы тестов `testCfg`/`ocsBody` из `client_test.go`.
- Produces (Task 2/4 завязаны на эти имена): `client.EditMessageOpts struct{ Message string }`; метод `func (c *TalkClient) EditMessage(ctx context.Context, token string, messageId int, opts EditMessageOpts) (int, error)`.

- [ ] **Step 1: Создать фикстуру `testdata/chat_edit_response.json`**

Обезличенный ответ PUT-правки РЕАЛЬНОГО формата (официальная документация Talk: data = полное системное сообщение «You edited a message» с `systemMessage: "message_edited"`, в `parent` — обновлённое сообщение с полями `lastEdit*`; поля — по таблице «Receive chat messages of a conversation»). Клиент в тесте обязан взять `parent.id` (2927), а НЕ id системного сообщения (5101):

```json
{
  "ocs": {
    "meta": {
      "status": "ok",
      "statuscode": 200,
      "message": "OK"
    },
    "data": {
      "id": 5101,
      "token": "tok123",
      "actorType": "users",
      "actorId": "alice",
      "actorDisplayName": "Alice",
      "timestamp": 1757300000,
      "message": "Message edited by you",
      "messageParameters": {
        "actor": { "type": "user", "id": "alice", "name": "Alice" }
      },
      "systemMessage": "message_edited",
      "messageType": "system",
      "isReplyable": false,
      "referenceId": "",
      "expirationTimestamp": 0,
      "silent": false,
      "parent": {
        "id": 2927,
        "token": "tok123",
        "actorType": "users",
        "actorId": "alice",
        "actorDisplayName": "Alice",
        "timestamp": 1757290000,
        "message": "исправленный текст",
        "messageParameters": {},
        "systemMessage": "",
        "messageType": "comment",
        "isReplyable": true,
        "referenceId": "",
        "expirationTimestamp": 0,
        "reactions": { "👍": 2 },
        "lastEditActorType": "users",
        "lastEditActorId": "alice",
        "lastEditActorDisplayName": "Alice",
        "lastEditTimestamp": 1757300000,
        "silent": false
      }
    }
  }
}
```

- [ ] **Step 2: Написать падающие тесты** — дописать в конец `internal/client/chat_test.go`

В блок импорта файла добавить `"errors"`, `"os"`, `"path/filepath"` (остальные уже есть).

```go
// -----------------------------------------------------------------------------
// EditMessage (chat edit, дизайн 2026-09-08)
// -----------------------------------------------------------------------------

// chatEditReq — слепок одного запроса к chat-эндпоинту, для asserts EditMessage.
type chatEditReq struct {
	method      string
	path        string
	body        string
	contentType string
}

// chatEditReqLog — потокобезопасный лог запросов к chat-эндпоинту.
type chatEditReqLog struct {
	mu   sync.Mutex
	reqs []chatEditReq
}

func (l *chatEditReqLog) record(r chatEditReq) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r)
}

func (l *chatEditReqLog) snapshot() []chatEditReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]chatEditReq, len(l.reqs))
	copy(out, l.reqs)
	return out
}

// chatEditServer поднимает httptest-сервер, отвечающий на любой запрос телом
// respBody (успех — фикстура реального формата; ошибки — ocsBody со statusCode)
// и логирующий method/path/Content-Type/тело запроса.
func chatEditServer(t *testing.T, respBody []byte) (*httptest.Server, *chatEditReqLog) {
	t.Helper()
	log := &chatEditReqLog{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		log.record(chatEditReq{
			method:      r.Method,
			path:        r.URL.Path,
			body:        string(body),
			contentType: r.Header.Get("Content-Type"),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
	})), log
}

// TestEditMessage_Success — PUT chat/{token}/{messageId}, тело {"message":...},
// заголовок Content-Type: application/json (mutate=true). Фикстура реального
// формата → метод вернул parent.id (2927), а НЕ id системного сообщения (5101) —
// это фиксация контракта формата ответа (дизайн §3).
func TestEditMessage_Success(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "chat_edit_response.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ts, log := chatEditServer(t, fixture)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	id, err := c.EditMessage(context.Background(), "tok", 2927, EditMessageOpts{Message: "исправленный текст"})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if got, want := id, 2927; got != want {
		t.Errorf("id: got %d, want %d (parent.id, НЕ id системного сообщения 5101)", got, want)
	}
	reqs := log.snapshot()
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d", got, want)
	}
	r := reqs[0]
	if r.method != http.MethodPut {
		t.Errorf("method: got %q, want PUT", r.method)
	}
	const wantPath = "/ocs/v2.php/apps/spreed/api/v1/chat/tok/2927"
	if r.path != wantPath {
		t.Errorf("path: got %q, want %q", r.path, wantPath)
	}
	if !strings.Contains(r.body, `"message":"исправленный текст"`) {
		t.Errorf("body: нет \"message\":\"исправленный текст\": %q", r.body)
	}
	if r.contentType != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json (mutate=true)", r.contentType)
	}
}

// TestEditMessage_OCSError — серверные отказа правки (400 старше 24ч, 403 чужое
// сообщение/read-only, 404 не найдено, 405 не comment) приходят как *OCSError
// с кодом и текстом сервера (стандартная обработка doOCS, дизайн §2/§3).
func TestEditMessage_OCSError(t *testing.T) {
	cases := []struct {
		code int
		msg  string
	}{
		{400, "message is too old"},
		{403, "not allowed to edit"},
		{404, "message not found"},
		{405, "not a normal chat message"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			ts, _ := chatEditServer(t, ocsBody(t, tc.code, tc.msg, nil))
			defer ts.Close()

			c := NewTalkClient(testCfg(ts.URL))
			_, err := c.EditMessage(context.Background(), "tok", 2927, EditMessageOpts{Message: "x"})
			if err == nil {
				t.Fatalf("err = nil, want OCSError с кодом %d", tc.code)
			}
			var oe *OCSError
			if !errors.As(err, &oe) || oe.Code != tc.code {
				t.Errorf("err: got %v, want *OCSError{Code:%d}", err, tc.code)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err: got %q, want содержит %q", err.Error(), tc.msg)
			}
		})
	}
}

// TestEditMessage_Guards — пустое сообщение и неположительный messageId —
// клиентские ошибки ДО сети (дизайн §4): лог запросов пуст, сервер не дёргаем.
func TestEditMessage_Guards(t *testing.T) {
	ts, log := chatEditServer(t, ocsBody(t, 200, "OK", nil))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.EditMessage(context.Background(), "tok", 2927, EditMessageOpts{Message: ""}); err == nil {
		t.Error("пустое Message: err = nil, want клиентская ошибка")
	}
	if _, err := c.EditMessage(context.Background(), "tok", 0, EditMessageOpts{Message: "x"}); err == nil {
		t.Error("messageId=0: err = nil, want клиентская ошибка")
	}
	if _, err := c.EditMessage(context.Background(), "tok", -1, EditMessageOpts{Message: "x"}); err == nil {
		t.Error("messageId=-1: err = nil, want клиентская ошибка")
	}
	if got := len(log.snapshot()); got != 0 {
		t.Errorf("запросов: got %d, want 0 (guard-ы отсекают ДО сети)", got)
	}
}
```

- [ ] **Step 3: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run 'TestEditMessage' -v`
Expected: FAIL — compile error `undefined: EditMessageOpts` / `c.EditMessage undefined` (метод ещё не написан).

- [ ] **Step 4: Реализовать `EditMessage`** — дописать в `internal/client/chat.go` после `SendMessage`

```go
// EditMessageOpts — параметры EditMessage (дизайн 2026-09-08 §4, `chat edit`).
type EditMessageOpts struct {
	// Message — новый текст сообщения, ОБЯЗАТЕЛЬНЫЙ. Пустая строка → клиентская
	// ошибка ДО сетевого вызова (не тратим запрос).
	Message string
}

// editMessageReq — форма JSON-тела запроса chat edit: единственное поле message
// (документация Talk, Chat API «Editing a Chat Message»).
type editMessageReq struct {
	Message string `json:"message"`
}

// editMessageResp — минимальная форма ocs.data ответа chat edit. Сервер
// возвращает ПОЛНОЕ системное сообщение о правке (systemMessage=message_edited),
// в поле parent — ОБНОВЛЁННОЕ сообщение. Документация явно предупреждает:
// системное сообщение предназначено для обновления кэша клиентов, не для
// отображения — поэтому из ответа разбираем только parent.id (= входной id).
type editMessageResp struct {
	Parent struct {
		Id int `json:"id"`
	} `json:"parent"`
}

// EditMessage редактирует уже отправленное сообщение messageId в комнате token
// (PUT chat/{token}/{messageId}, дизайн 2026-09-08 §3/§4) и возвращает id
// отредактированного сообщения из ответа сервера (ocs.data.parent.id) — НЕ эхом
// входного аргумента.
//
// Контракт:
//   - opts.Message == "" или messageId <= 0 → клиентская ошибка ДО сети;
//   - серверные отказа (400 старше 24ч, 403 чужое/read-only, 404, 405, 412)
//     не предугадываем — приходят как *OCSError с текстом сервера (doOCS);
//   - серверные ограничения (свои сообщения, 24 часа, тип comment) клиент
//     не дублирует (тонкий клиент, дизайн §1 Out of scope).
func (c *TalkClient) EditMessage(ctx context.Context, token string, messageId int, opts EditMessageOpts) (int, error) {
	// Клиентская валидация до сети — дубль CLI-guard-ов, защищает прямые вызовы.
	if opts.Message == "" {
		return 0, errors.New("client: EditMessage: пустое сообщение (opts.Message обязательно)")
	}
	if messageId <= 0 {
		return 0, fmt.Errorf("client: EditMessage: messageId ожидает положительное число, получено %d", messageId)
	}
	body, err := json.Marshal(editMessageReq{Message: opts.Message})
	if err != nil {
		// Практически недостижимо, но пропускаем через sanitizeErr единообразно
		// с SendMessage.
		return 0, sanitizeErr(fmt.Errorf("client: не удалось собрать тело EditMessage: %w", err))
	}
	// token и messageId — отдельные path-сегменты: PathEscape токена,
	// messageId форматируем через strconv.Itoa (целое — безопасно без эскейпа).
	p := pathChat + "/" + url.PathEscape(token) + "/" + strconv.Itoa(messageId)
	var out editMessageResp
	if _, err := c.doOCS(ctx, http.MethodPut, p, nil, bytes.NewReader(body), true, &out); err != nil {
		return 0, err
	}
	return out.Parent.Id, nil
}
```

Нужные импорты (`bytes`, `context`, `encoding/json`, `errors`, `fmt`, `net/http`, `net/url`, `strconv`) в `chat.go` уже есть — новых не добавлять.

- [ ] **Step 5: Прогнать тесты пакета + vet, убедиться в зелёном**

Run: `CGO_ENABLED=0 go test ./internal/client/ -v && CGO_ENABLED=0 go vet ./internal/client/`
Expected: PASS (все старые + 3 новых теста), vet чист.

- [ ] **Step 6: Commit**

```sh
git add testdata/chat_edit_response.json internal/client/chat.go internal/client/chat_test.go
git commit -m "feat(client): EditMessage — PUT chat/{token}/{messageId} + фикстура реального формата"
```

---

### Task 2: cli — handler `chatEditHandler`, интерфейс `TalkClient`, расширение mock-ов

**Files:**
- Modify: `internal/cli/cli.go:16-27` (интерфейс + комментарий «7 методов» → «8»)
- Modify: `internal/cli/handlers_chat.go` (новый handler в конце; правка заголовочного комментария файла)
- Modify (стабы интерфейса — без них пакет не соберётся): `internal/cli/cli_test.go`, `internal/cli/handlers_rooms_test.go`, `internal/cli/handlers_reactions_test.go`, `internal/cli/handlers_search_test.go`, `internal/cli/room_test.go`
- Test: `internal/cli/handlers_chat_test.go` (шпион `chatSpyClient` + новые тесты)

**Interfaces:**
- Consumes: `client.EditMessageOpts`/`EditMessage` из Task 1; `ResolveRoom`, `exitFromClientErr`, `render.NewMessageID{,JSON}`, `newChatSendDeps` — существующие.
- Produces (Task 3 завязан): функция `chatEditHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError` с точным порядком шагов (дизайн §5): 1) scan флагов `--name`/`--file` (обе формы); 2) распределение позиционных как у `reactionsGetHandler`; 3) чтение тела ДО `ResolveRoom` + трим одного trailing `\n`/`\r\n`, пустое → exit 1; 4) парсинг `messageId`; 5) `ResolveRoom` (коды 2/3); 6) `EditMessage`; 7) вывод id.

- [ ] **Step 1: Написать падающие тесты** — дописать в `internal/cli/handlers_chat_test.go`

Расширить шпион (поля + метод — рядом с SendMessage-блоком):

```go
	// EditMessage-запись (chat edit)
	gotEditToken string
	gotEditId    int
	gotEditOpts  client.EditMessageOpts
	editCalls    int

	// EditMessage-результат
	editId  int
	editErr error
```

```go
func (m *chatSpyClient) EditMessage(_ context.Context, token string, messageId int, opts client.EditMessageOpts) (int, error) {
	m.editCalls++
	m.gotEditToken = token
	m.gotEditId = messageId
	m.gotEditOpts = opts
	return m.editId, m.editErr
}
```

Таблица тестов (контракты дизайна §2/§6):

```go
// -----------------------------------------------------------------------------
// chat edit (дизайн 2026-09-08)
// -----------------------------------------------------------------------------

// TestChatEdit — table-driven по контракту `chat edit` (дизайн §2/§6): вывод id
// (текстовый режим), пустое тело и невалидный messageId — без вызова клиента,
// неизвестный флаг, --name (1/0/2 совпадения → 0/2/3), OCS 404 → 2, OCS 403 → 1,
// лишние позиционные игнорируются, трим одного trailing newline, --file, =формы.
// JSON-ветка — отдельно в TestChatEdit_JSONOutput (writeJSON печатает с отступами,
// точное строковое сравнение в таблице хрупко).
func TestChatEdit(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		stdin         string
		editId        int
		editErr       error
		findRooms     []client.Room
		wantCode      int
		wantEditCalls int
		wantFindCalls int    // FindRooms: 1 для кейсов с --name (ResolveRoom зовёт поиск), иначе 0
		wantStdout    string // точное совпадение (пустая строка — проверить как пустое)
		checkStdout   bool
		wantErrSubstr string // подстрока в ee.Err ("" — не проверять)
	}{
		{
			name: "stdin тело → успех, stdout id", args: []string{"TOK", "2927"}, stdin: "исправлено",
			editId: 555, wantCode: ExitOK, wantEditCalls: 1, wantStdout: "555\n", checkStdout: true,
		},
		{
			name: "трейлинг \\n срезается", args: []string{"TOK", "2927"}, stdin: "hi\n",
			editId: 1, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "многострочное тело сохраняется", args: []string{"TOK", "2927"}, stdin: "line1\nline2\n",
			editId: 1, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "--name= форма + позиционный messageId", args: []string{"2927", "--name=proj"}, stdin: "x",
			findRooms: []client.Room{{Type: 1, Token: "NAMETOK", DisplayName: "Project", ActorId: "u-1"}},
			editId:    7, wantCode: ExitOK, wantEditCalls: 1, wantFindCalls: 1,
		},
		{
			name: "лишние позиционные игнорируются", args: []string{"TOK", "2927", "junk"}, stdin: "x",
			editId: 3, wantCode: ExitOK, wantEditCalls: 1,
		},
		{
			name: "пустое тело → exit 1 без сети", args: []string{"TOK", "2927"}, stdin: "",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "пусто",
		},
		{
			name: "тело только \\n → exit 1", args: []string{"TOK", "2927"}, stdin: "\n",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "пусто",
		},
		{
			name: "messageId не число → exit 1 до сети", args: []string{"TOK", "abc"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "целое число",
		},
		{
			name: "messageId <= 0 → exit 1 до сети", args: []string{"TOK", "0"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "положительное",
		},
		{
			name: "неизвестный флаг → exit 1", args: []string{"TOK", "2927", "--silent"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "неизвестный флаг",
		},
		{
			name: "1 позиционный без --name → exit 1", args: []string{"2927"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId>",
		},
		{
			name: "0 позиционных без --name → exit 1", args: []string{}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId> или --name",
		},
		{
			name: "0 позиционных с --name → exit 1 (нет messageId)", args: []string{"--name", "proj"}, stdin: "x",
			wantCode: ExitGeneric, wantEditCalls: 0, wantErrSubstr: "ожидается <room> <messageId> или --name",
		},
		{
			name: "--name 0 совпадений → exit 2", args: []string{"2927", "--name", "нету"}, stdin: "x",
			findRooms: []client.Room{}, wantCode: ExitNotFound, wantEditCalls: 0, wantFindCalls: 1,
		},
		{
			name: "--name 2 совпадения → exit 3", args: []string{"2927", "--name", "тест"}, stdin: "x",
			findRooms: []client.Room{
				{Type: 1, Token: "T1", DisplayName: "Первый", ActorId: "u-a"},
				{Type: 2, Token: "T2", DisplayName: "Второй", ActorId: "u-b"},
			},
			wantCode: ExitAmbiguous, wantEditCalls: 0, wantFindCalls: 1,
		},
		{
			name: "OCS 404 от EditMessage → exit 2", args: []string{"TOK", "2927"}, stdin: "x",
			editErr: &client.OCSError{Code: 404, Message: "message not found"},
			wantCode: ExitNotFound, wantEditCalls: 1,
		},
		{
			name: "OCS 403 от EditMessage → exit 1 с текстом сервера", args: []string{"TOK", "2927"}, stdin: "x",
			editErr: &client.OCSError{Code: 403, Message: "not allowed to edit"},
			wantCode: ExitGeneric, wantEditCalls: 1, wantErrSubstr: "not allowed to edit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{editId: tc.editId, editErr: tc.editErr, findRooms: tc.findRooms}
			deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))

			ee := chatEditHandler(context.Background(), deps, tc.args, false)
			if ee.Code != tc.wantCode {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantCode, ee.Err)
			}
			if spy.editCalls != tc.wantEditCalls {
				t.Errorf("EditMessage calls: got %d, want %d", spy.editCalls, tc.wantEditCalls)
			}
			// wantFindCalls, а не «0 при wantEditCalls==0»: кейсы --name с 0/2
			// совпадениями требуют вызова FindRooms (это единственный источник
			// кодов 2/3) — безликая проверка «FindRooms не звался» ложно ругалась
			// бы на корректной реализации.
			if spy.findCalls != tc.wantFindCalls {
				t.Errorf("FindRooms calls: got %d, want %d", spy.findCalls, tc.wantFindCalls)
			}
			if tc.wantErrSubstr != "" && (ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.wantErrSubstr)) {
				t.Errorf("err: got %v, want содержит %q", ee.Err, tc.wantErrSubstr)
			}
			if tc.checkStdout {
				if out := deps.Stdout.(*bytes.Buffer).String(); out != tc.wantStdout {
					t.Errorf("stdout: got %q, want %q", out, tc.wantStdout)
				}
			}
		})
	}
}

// TestChatEdit_PassesArguments — позиционные <room> <messageId> и тело доходят
// до EditMessage; id вывода берётся из ответа клиента (не эхо входного).
func TestChatEdit_PassesArguments(t *testing.T) {
	spy := &chatSpyClient{editId: 999}
	deps := newChatSendDeps(spy, strings.NewReader("исправлено"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotEditToken != "TOK" {
		t.Errorf("token: got %q, want %q", spy.gotEditToken, "TOK")
	}
	if spy.gotEditId != 2927 {
		t.Errorf("messageId: got %d, want 2927", spy.gotEditId)
	}
	if spy.gotEditOpts.Message != "исправлено" {
		t.Errorf("Message: got %q, want %q", spy.gotEditOpts.Message, "исправлено")
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); out != "999\n" {
		t.Errorf("stdout: got %q, want %q (id из ответа клиента)", out, "999\n")
	}
}

// TestChatEdit_JSONOutput — jsonOut=true → в stdout валидный JSON {"id": <int>}
// (render.NewMessageIDJSON, тот же вывод, что у chat send; проверка через
// json.Unmarshal — по образцу TestChatSendStdinJSON).
func TestChatEdit_JSONOutput(t *testing.T) {
	spy := &chatSpyClient{editId: 42}
	deps := newChatSendDeps(spy, strings.NewReader("json body"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).Bytes()
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	if got.Id != 42 {
		t.Errorf("json.id: got %d, want 42", got.Id)
	}
}

// TestChatEdit_TrimsOneTrailingNewline — срезается РОВНО ОДИН завершающий
// \n / \r\n; внутренние переводы строк сохраняются (идентично chat send).
func TestChatEdit_TrimsOneTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
		want  string
	}{
		{"unix", "hi\n", "hi"},
		{"crlf", "hi\r\n", "hi"},
		{"multiline", "line1\nline2\n", "line1\nline2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &chatSpyClient{editId: 1}
			deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))

			ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927"}, false)
			if ee.Code != ExitOK {
				t.Fatalf("%s: code got %d, want %d (err=%v)", tc.name, ee.Code, ExitOK, ee.Err)
			}
			if spy.gotEditOpts.Message != tc.want {
				t.Errorf("%s: Message got %q, want %q", tc.name, spy.gotEditOpts.Message, tc.want)
			}
		})
	}
}

// TestChatEdit_File — тело из --file <path> (временный файл), stdin не читается.
func TestChatEdit_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(path, []byte("new-body"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	spy := &chatSpyClient{editId: 5}
	deps := newChatSendDeps(spy, strings.NewReader(""))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927", "--file", path}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.editCalls != 1 || spy.gotEditOpts.Message != "new-body" {
		t.Errorf("EditMessage: calls=%d Message=%q, want 1 и %q", spy.editCalls, spy.gotEditOpts.Message, "new-body")
	}
}

// TestChatEdit_NamePositionalConflict — заданы и token, и --name → приоритет у
// token, --name игнорируется с предупреждением в stderr (поведение ResolveRoom).
func TestChatEdit_NamePositionalConflict(t *testing.T) {
	spy := &chatSpyClient{editId: 1}
	deps := newChatSendDeps(spy, strings.NewReader("x"))

	ee := chatEditHandler(context.Background(), deps, []string{"TOK", "2927", "--name", "ignored"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if spy.gotEditToken != "TOK" {
		t.Errorf("token: got %q, want %q (positional выигрывает)", spy.gotEditToken, "TOK")
	}
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "приоритет у token") {
		t.Errorf("stderr должен содержать предупреждение о конфликте; got %q", stderr)
	}
}
```

- [ ] **Step 2: Прогнать, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestChatEdit' -v`
Expected: FAIL — compile error `undefined: chatEditHandler` (handler ещё не написан).

- [ ] **Step 3: Добавить `EditMessage` в интерфейс `TalkClient` и стабы во все mock-и**

`internal/cli/cli.go` — в интерфейс после `SendMessage` (и правка комментария «покрывающий 7 методов» → «покрывающий 8 методов»):

```go
	EditMessage(ctx context.Context, token string, messageId int, opts client.EditMessageOpts) (int, error)
```

Стаб-метод в 5 заглушках, не связанных с chat (одинаковый для всех; добавить рядом с `SendMessage`):

- `internal/cli/cli_test.go` → `mockTalkClient`
- `internal/cli/handlers_rooms_test.go` → `roomsSpyClient`
- `internal/cli/handlers_reactions_test.go` → `reactionsSpyClient`
- `internal/cli/handlers_search_test.go` → `searchSpyClient`
- `internal/cli/room_test.go` → `roomMockClient`

```go
func (m *X) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
```

(в каждом файле подставить своё имя типа вместо `X`; у `roomMockClient` и прочих приёмник уже `(m *X)` — сохранить как в файле).

- [ ] **Step 4: Реализовать `chatEditHandler`** — дописать в конец `internal/cli/handlers_chat.go`

Заодно поправить заголовочный комментарий файла: «реализации команд `chat show` (Task 4.3) и `chat send` (Task 4.4...)» → дополнить «и `chat edit` (дизайн 2026-09-08)».

```go
// chatEditHandler — реализация `chat edit <room> <messageId>` (дизайн
// 2026-09-08 §2/§5): правка уже отправленного сообщения. Тело нового текста —
// как у chat send (stdin по умолчанию, --file как альтернатива), распределение
// позиционных <room> <messageId> — как у reactions get.
//
// Порядок шагов (дизайн §5): флаги → позиционные → чтение тела (ДО ResolveRoom;
// пустое → exit 1 без сети) → парсинг messageId (exit 1 до сети) → ResolveRoom
// (коды 2/3) → EditMessage → вывод id.
//
// Других флагов НЕТ: --silent/--reply-to/--reference-id для правки бессмысленны
// (дизайн §2) и отвергаются как неизвестные.
//
// Вывод: id отредактированного сообщения из ОТВЕТА сервера (parent.id), а не
// эхо входного аргумента: текстом `<int>\n` или `{"id": <int>}` при --json —
// тот же вывод, что у chat send (render.NewMessageID{,JSON}).
func chatEditHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1. Разбор флагов: ручной scan (без flag-пакета, спека §3), обе формы
	// `--flag value` и `--flag=value` — единообразно с chatSend.
	var (
		positionals []string
		nameFlag    string
		fileFlag    string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat edit: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case a == "--file":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("chat edit: --file требует значение (путь)")}
			}
			fileFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--file="):
			fileFlag = strings.TrimPrefix(a, "--file=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat edit: неизвестный флаг %q", a)}
		default:
			// Позиционные — <room> и <messageId>; распределение ниже.
			positionals = append(positionals, a)
		}
	}

	// 2. Распределение positionals — единообразно с reactions get (дизайн §2):
	//   - >=2 → первый = <room> (token), второй = <messageId>; заданный при этом
	//     --name игнорируется с предупреждением (поведение ResolveRoom);
	//   - 1 + --name → позиционный это <messageId>, комната по --name;
	//   - 1 без --name → нехватка;
	//   - 0 (с --name или без) → нехватка (default-ветка покрывает обе).
	var roomPos, msgIdRaw string
	switch {
	case len(positionals) >= 2:
		roomPos = positionals[0]
		msgIdRaw = positionals[1]
	case len(positionals) == 1 && nameFlag != "":
		msgIdRaw = positionals[0]
	case len(positionals) == 1:
		return ExitError{Code: ExitGeneric, Err: errors.New("chat edit: ожидается <room> <messageId>")}
	default:
		return ExitError{Code: ExitGeneric, Err: errors.New("chat edit: ожидается <room> <messageId> или --name <имя> <messageId>")}
	}

	// 3. Чтение тела ДО ResolveRoom: пустое тело отсекается без единого
	// сетевого вызова (ни FindRooms, ни EditMessage) — идентично chat send.
	var body []byte
	var readErr error
	if fileFlag != "" {
		body, readErr = os.ReadFile(fileFlag)
	} else {
		stdin := deps.Stdin
		if stdin == nil {
			// Production-путь: main не проставляет Stdin, fallback на os.Stdin.
			stdin = os.Stdin
		}
		body, readErr = io.ReadAll(stdin)
	}
	if readErr != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat edit: не удалось прочитать тело: %w", readErr)}
	}
	// Срезаем ОДИН завершающий перевод строки (\n или \r\n); многострочные
	// тела валидны — внутренние переводы сохраняются.
	text := string(body)
	if strings.HasSuffix(text, "\r\n") {
		text = text[:len(text)-2]
	} else if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if len(text) == 0 {
		return ExitError{Code: ExitGeneric, Err: errors.New("chat edit: тело сообщения пусто")}
	}

	// 4. Парсинг messageId: невалид/неположительный → exit 1 ДО сети (ни
	// FindRooms, ни EditMessage — дизайн §2).
	messageId, err := strconv.Atoi(msgIdRaw)
	if err != nil {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat edit: <messageId> ожидает целое число, получено %q", msgIdRaw)}
	}
	if messageId <= 0 {
		return ExitError{Code: ExitGeneric, Err: fmt.Errorf("chat edit: <messageId> ожидает положительное число, получено %d", messageId)}
	}

	// 5. Разрешение <room>: positional token (primary) или --name (поиск).
	// ResolveRoom сам печатает кандидатов при неоднозначности и возвращает
	// ExitAmbiguous/ExitNotFound как ExitError — пробрасываем код.
	token, err := ResolveRoom(ctx, deps.Client, roomPos, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 6. Правка. Ошибки приходят sanitized из client-слоя; OCS 404 (комната или
	// сообщение не найдены) → exit 2, прочие (400/403/405/412/сеть/401) → exit 1.
	id, err := deps.Client.EditMessage(ctx, token, messageId, client.EditMessageOpts{Message: text})
	if err != nil {
		return exitFromClientErr(err)
	}

	// 7. Вывод id по ветке jsonOut — тот же вывод, что у chat send.
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
```

- [ ] **Step 5: Прогнать cli-тесты + vet**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v && CGO_ENABLED=0 go vet ./internal/cli/`
Expected: PASS — новые TestChatEdit* зелёные, ВСЕ старые зелёные (стабы вернули компиляцию mock-ов; compile-time `var _ TalkClient = (*client.TalkClient)(nil)` проходит благодаря Task 1).

- [ ] **Step 6: Полный прогон репозитория (изоляция не сломана)**

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: все пакеты PASS (включая `TestCmdNctalkDoesNotDependOnPion` — pion-изоляция не затронута: новых импортов нет).

- [ ] **Step 7: Commit**

```sh
git add internal/cli/cli.go internal/cli/handlers_chat.go internal/cli/handlers_chat_test.go \
  internal/cli/cli_test.go internal/cli/handlers_rooms_test.go internal/cli/handlers_reactions_test.go \
  internal/cli/handlers_search_test.go internal/cli/room_test.go
git commit -m "feat(cli): chat edit — handler, EditMessage в TalkClient, стабы mock-ов"
```

---

### Task 3: cli — роутинг + help-декларация + каноны анти-drift

**Files:**
- Modify: `internal/cli/cli.go` (routes["chat"], подсказка verb-ов, ~строки 62-75 и 119)
- Modify: `internal/cli/help.go` (cmdSpecs: запись после `chat send`; комментарий «7 команд» → «8»)
- Test: `internal/cli/cli_test.go` (TestRunRoutesToCorrectHandler; комментарий «все 7 команд» → «все 8»)
- Test: `internal/cli/help_test.go` (два канона + buildPositionalArgs + спец-кейс Stdin; комментарии «7» → «8» в двух местах)

**Interfaces:**
- Consumes: `chatEditHandler` из Task 2.
- Produces: маршрутизация `nctalk chat edit ...` → handler; декларация `["chat", "edit"]` в `cmdSpecs` (единый источник правды help-ов и anti-drift). Все детальные/anti-drift проверки (`TestCmdSpecs_CoverAllRoutes`, `TestHandleHelp_DetailedContent`, `TestAntiDrift_HelpMatchesDeclaration`, `TestAntiDrift_HandlerMatchesDeclaration`) подхватывают запись автоматически; правятся руками ТОЛЬКО каноны с hardcoded-перечнями.

ВАЖНО: routes-запись и cmdSpecs-запись landed в одном шаге — `TestCmdSpecs_CoverAllRoutes` сверяет их множества, разнос по коммитам роняет прогон.

- [ ] **Step 1: Обновить каноны и роутинговый тест (сначала тесты — красные)**

`internal/cli/help_test.go`:

1. `TestCmdSpecs_OrderMatchesHelpOrder` — want-слайс, вставить после `"chat send"`:

```go
	want := []string{
		"rooms list", "rooms find", "rooms search",
		"chat show", "chat send", "chat edit",
		"reactions get",
		"search",
	}
```

2. `TestCmdSpecs_FlagsExactly` — добавить ключ (иначе `t.Fatalf("неизвестный путь")`):

```go
		"chat edit":     {"--name", "--file"},
```

3. `buildPositionalArgs` — новый кейс (по образцу `reactions get`, иначе handler получает невалидные позиционные и prong-и анти-drift'а вырождаются):

```go
	case "chat edit":
		return []string{"tok123", "1"}
```

4. `TestAntiDrift_HandlerMatchesDeclaration` — спец-кейс непустого Stdin расширен на `chat edit` (edit читает тело ДО ResolveRoom и при nil-Stdin fallback-ает на os.Stdin — на TTY prong 1 для `--name` рискует зависанием). Оба вхождения (prong 1 и prong 3) заменить:

```go
				if p := joinPath(cs.Path); p == "chat send" || p == "chat edit" {
					deps.Stdin = strings.NewReader("anti-drift body")
				}
```

и в комментарии над ними упомянуть chat edit рядом с chat send («chat send и chat edit читают тело ДО ResolveRoom...»).

5. Комментарии-счётчики в этом файле: `TestCmdSpecs_CoverAllRoutes` — «ровно те же 7 команд» → «8 команд»; `TestHandleHelp_DetailedContent` — «для каждой из 7 команд» → «для каждой из 8 команд».

`internal/cli/cli_test.go`:

6. `TestRunRoutesToCorrectHandler` — новый кейс в таблицу:

```go
		{"chat/edit", []string{"chat", "edit", "TOK123", "42"}, []string{"TOK123", "42"}, ""},
```

7. `TestRunRoutesAllStubs` — добавить кейс в таблицу после `chat send` (без кейса комментарий начнёт врать: кейсов останется 7 — тот же комментарий-drift, который вычищает Task 5 Step 4) и обновить комментарий «все 7 команд» → «все 8 команд»:

```go
		{"chat edit", []string{"chat", "edit", "tok", "1", "--text", "hi"}},
```

(маска — как у соседнего кейса `chat send`: заведомо неизвестный handler-у флаг `--text` → ExitGeneric с текстом в stderr ДО чтения stdin, детерминированно; до Step 3 кейс тоже зелёный — Run отвечает «неизвестный verb» с кодом 1).

- [ ] **Step 2: Прогнать, убедиться в падении канонов**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestCmdSpecs|TestRunRoutesToCorrectHandler' -v`
Expected: FAIL — красные:
- `TestCmdSpecs_OrderMatchesHelpOrder` — len(cmdSpecs)=7 != len(want)=8 (канон из Step 1 обновлён, декларация — ещё нет);
- `TestCmdSpecs_CoverAllRoutes` — артефакт нового кейса `chat/edit` из Step 1.6: `installSpy` создаёт ключ `routes["chat"]["edit"]` map-присваиванием, а restore пишет в него `orig` (nil) — ключ ОСТАЁТСЯ в routes, множество routes «видит» восьмой путь, которого нет в cmdSpecs (тесты исполняются по файлам по алфавиту: cli_test.go раньше help_test.go, кейс успевает исполниться до CoverAllRoutes).

ЗЕЛЁНЫЕ на этом шаге — это нормально, НЕ дебажить: `TestCmdSpecs_FlagsExactly` (сверка односторонняя — цикл идёт по cmdSpecs с поиском в want, лишний ключ в want не детектируется; канон краснеет только в обратную сторону — запись в cmdSpecs есть, а ключ в want забыли) и `TestRunRoutesToCorrectHandler` (шпион, записанный `installSpy` в `routes["chat"]["edit"]`, вызывается и без routes-записи из Step 3 → ExitOK). Красный канон есть → TDD-шаг состоятелен; полный зелёный — после Step 3.

- [ ] **Step 3: Добавить роутинг и декларацию**

`internal/cli/cli.go` — routes:

```go
	"chat": {
		"show": chatShowHandler,
		"send": chatSendHandler,
		"edit": chatEditHandler,
	},
```

и подсказка при отсутствии verb (строка ~119):

```go
		fmt.Fprintf(deps.Stderr, "nctalk %s: ожидается verb (list/find/search/show/send/edit/get)\n", resource)
```

(`TestRunMissingVerb` код проверки текста не содержит — дополнительных правок теста не нужно.)

`internal/cli/help.go` — комментарий `cmdSpecs` «7 команд базового CLI» → «8 команд базового CLI»; новая запись СРАЗУ после `chat send` (порядок слайса = порядок общего help):

```go
	{
		Path:        []string{"chat", "edit"},
		Short:       "отредактировать отправленное сообщение (stdin или --file)",
		UsageExtras: "<room> <messageId>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--file", Value: "<путь>", Desc: "взять тело из файла (иначе stdin)"},
		},
		Examples: []string{
			"echo \"исправлено\" | nctalk chat edit abc123 100",
			"nctalk chat edit --name \"Команда\" 100 --file new.txt",
		},
	},
```

- [ ] **Step 4: Прогнать help/anti-drift + весь cli-пакет**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v && CGO_ENABLED=0 go vet ./internal/cli/`
Expected: PASS — `TestCmdSpecs_CoverAllRoutes`, `TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`, `TestHandleHelp_DetailedContent` (подхватил `chat edit` автоматически), `TestAntiDrift_HelpMatchesDeclaration`, `TestAntiDrift_HandlerMatchesDeclaration` (prong-и проходят благодаря buildPositionalArgs + Stdin-спец-кейсу), `TestRunRoutesToCorrectHandler`.

- [ ] **Step 5: Ручная smoke-проверка help (не тест, верификация)**

Run: `CGO_ENABLED=0 go build -o /tmp/nctalk-edit ./cmd/nctalk && /tmp/nctalk-edit chat edit --help && /tmp/nctalk-edit --help | grep 'chat edit'`
Expected: детальный help `chat edit` (usage `nctalk chat edit <room> <messageId> [flags]`, флаги `--name`/`--file`/`--json`, примеры); в общем help строка `chat edit` между `chat send` и `reactions get`.

- [ ] **Step 6: Полный прогон**

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: PASS во всех пакетах.

- [ ] **Step 7: Commit**

```sh
git add internal/cli/cli.go internal/cli/help.go internal/cli/cli_test.go internal/cli/help_test.go
git commit -m "feat(cli): роутинг и help-декларация chat edit + каноны анти-drift (8 команд)"
```

---

### Task 4: Интеграционный тест send → edit → chat show

**Files:**
- Modify: `internal/client/integration_test.go` (новый тест после `TestIntegration_SendMessage`)

**Interfaces:**
- Consumes: `SendMessage`/`EditMessage`/`GetChat` (client-слой); существующие мутационные флаги `NCTALK_INTEGRATION_SEND=1` + `NCTALK_INTEGRATION_ROOM` — **новых env не вводим** (дизайн §6).
- Produces: `TestIntegration_EditMessage` — единственная проверка формата PUT-ответа на живом API (цикл: отправили → отредактировали → прочитали новый текст). Побочный мусор (исправленное сообщение + system-сообщение о правке) приемлем, убирается вручную, как за send-тестами.

- [ ] **Step 1: Написать тест** — дописать в конец `internal/client/integration_test.go`

```go
// TestIntegration_EditMessage — мутационный сценарий правки сообщения (дизайн
// 2026-09-08 §6): цикл SendMessage → EditMessage → GetChat проверяет и саму
// правку, и формат PUT-ответа (parent.id = id отредактированного сообщения —
// единственное место, где контракт ответа виден на живом API). Защита та же,
// что у TestIntegration_SendMessage: NCTALK_INTEGRATION_SEND=1 +
// NCTALK_INTEGRATION_ROOM (новых env не вводим). Мусор (исправленное сообщение
// + system-сообщение о правке) остаётся в тестовой комнате, убирается вручную.
func TestIntegration_EditMessage(t *testing.T) {
	if got := os.Getenv("NCTALK_INTEGRATION_SEND"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_SEND != 1 — пропускаю мутационный тест EditMessage")
	}
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропускаю EditMessage (нет целевой тестовой комнаты)")
	}

	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Уникальный суффикс — чтобы сообщения прогона не путались с прошлыми.
	uniq := strconv.FormatInt(time.Now().Unix(), 10)
	orig := "edit-before " + uniq
	fixed := "edit-after " + uniq

	id, err := c.SendMessage(ctx, token, SendMessageOpts{Message: orig})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	t.Logf("отправлено id=%d: %q", id, orig)

	editId, err := c.EditMessage(ctx, token, id, EditMessageOpts{Message: fixed})
	if err != nil {
		t.Fatalf("EditMessage(%d): %v", id, err)
	}
	// Контракт формата ответа: id берётся из ocs.data.parent.id и равен входному.
	if editId != id {
		t.Errorf("EditMessage: сервер вернул parent.id=%d, want %d (формат ответа отличается от документации?)", editId, id)
	}

	// Проверка нового текста через чтение истории.
	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: 20})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Id == id {
			found = true
			if m.Message != fixed {
				t.Errorf("сообщение %d: текст %q, want %q (правка не применилась?)", id, m.Message, fixed)
			}
			break
		}
	}
	if !found {
		t.Errorf("сообщение %d не найдено в последних %d сообщениях", id, len(msgs))
	}
}
```

- [ ] **Step 2: Компиляция integration-сьюта + отсутствие в обычном прогоне**

Run: `CGO_ENABLED=0 go vet -tags=integration ./internal/client/ && CGO_ENABLED=0 go test -tags=integration ./internal/client/ -run TestIntegration_EditMessage -v`
Expected: vet чист; тест SKIP («NCTALK_INTEGRATION_SEND != 1» — env не задан, это правильное поведение gate-а). Тег обязателен и в `go test`: без него файл исключён build-констрайнтом `//go:build integration`, теста в бинарнике нет — go молча выведет `no tests to run` (ok) вместо SKIP.

- [ ] **Step 3: (По возможности; держатель кредов) Живой прогон мутации**

Запускает человек на тестовой комнате (см. `docs/integration-run.md`; креды в env):

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run 'TestIntegration_(SendMessage|EditMessage)'
```

Expected: оба PASS; `editId == id` подтверждает формат PUT-ответа. Если формат отличается — **обновить фикстуру** `testdata/chat_edit_response.json` под реальный ответ (сняв его curl-ом/DevTools и обезличив) и перезапустить unit-тесты; это зафиксированное в спеке «не проверено на живом API (мутация)».

- [ ] **Step 4: Commit**

```sh
git add internal/client/integration_test.go
git commit -m "test(integration): цикл send -> edit -> chat show для EditMessage под NCTALK_INTEGRATION_SEND"
```

---

### Task 5: Сопроводительная документация

**Files:**
- Modify: `README.md` (секция команды после `chat send`, ~строка 118; проверяемая правка одной секции)
- Modify: `CLAUDE.md:5` («7 команд» → «8 команд»)
- Modify: `docs/integration-run.md` (5 мест, дизайн §5: шапка, таблица env, пример запуска, раздел «Безопасность», таблица тестов)

**Interfaces:** Consumes всё, что построено выше; Produces — консистентная документация (тот же drift, который ловят анти-drift тесты).

- [ ] **Step 1: README — новая секция после `chat send`** (после строки «Вывод: `id` отправленного сообщения.») и перед секцией `reactions get`:

````markdown
### `chat edit <room> <messageId>` — отредактировать сообщение

```sh
echo "исправлено" | nctalk chat edit kytxaiyc 2927      # новый текст из stdin (по умолчанию)
nctalk chat edit kytxaiyc 2927 --file new.txt          # новый текст из файла
nctalk chat edit --name "Команда" 2927 --file new.txt  # комнату по имени
```

Вывод: `id` отредактированного сообщения. Правки ограничивает сервер (ошибки 400/403/404/405 показывает сервер): свои сообщения — в течение 24 часов после отправки, чужие — только модератору, тип — comment.
````

- [ ] **Step 2: CLAUDE.md** — строка 5, фразу «только примитивы (7 команд)» заменить на «только примитивы (8 команд)». Больше ничего в CLAUDE.md про edit не добавлять (поведенческие детали — в спеке).

- [ ] **Step 3: `docs/integration-run.md`** — правки по §5 спеки:

1. Шапка (строка 7): «Все сценарии читающие и безопасные, кроме `TestIntegration_SendMessage` — он мутирует (отправляет сообщение)...» → «Все сценарии читающие и безопасные, кроме мутационных `TestIntegration_SendMessage` (отправка) и `TestIntegration_EditMessage` (правка) — они меняют состояние чата и защищены отдельным флагом.»
2. Таблица «Опциональное окружение», `NCTALK_INTEGRATION_SEND`: назначение → «`1` — включить мутационные `TestIntegration_SendMessage`/`TestIntegration_EditMessage`.»; `NCTALK_INTEGRATION_ROOM` → «Token тестовой комнаты для мутационных `SendMessage`/`EditMessage`.»
3. Секция «### Мутация (отправка сообщения)» — переименовать в «### Мутация (отправка и правка сообщений)», regex примера расширить и добавить строку запуска edit-цикла:

```sh
NCTALK_INTEGRATION_SEND=1 \
NCTALK_INTEGRATION_ROOM='<token-тестовой-комнаты>' \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(SendMessage|EditMessage)'
```

и абзац «Без `NCTALK_INTEGRATION_SEND=1` или без `NCTALK_INTEGRATION_ROOM` тесты молча скипаются.» (мн.ч.).
4. Раздел «Безопасность сценариев», пункт «Мутация»: «(`SendMessage`) — POST в чат» → «(`SendMessage`/`EditMessage`) — POST/PUT в чат; выполняются только в токен, заданный в `NCTALK_INTEGRATION_ROOM`, и только при `NCTALK_INTEGRATION_SEND=1`. Удалять тестовые сообщения нужно вручную — тесты этого не делают (спека §12 „мутация, не проверено“).»
5. Таблица «Что проверяет каждый тест» — строка после `TestIntegration_SendMessage`:

```markdown
| `TestIntegration_EditMessage` | PUT `/chat/{token}/{messageId}`             | `parent.id == id`; `GetChat` видит новый текст. Мутация — под флагом. |
```

- [ ] **Step 4: Проверить отсутствие оставшихся «7 команд»**

Run: `grep -rn "7 команд\|7 метод" /Users/stas/Projects/My/NCCliClient/internal /Users/stas/Projects/My/NCCliClient/README.md /Users/stas/Projects/My/NCCliClient/CLAUDE.md`
Expected: пусто (все счётчики — `internal/cli/help.go`, `internal/cli/cli.go`, `internal/cli/cli_test.go`, `internal/cli/help_test.go` — обновлены в Task 2/3; CLAUDE.md — в этом task).

- [ ] **Step 5: Финальный полный прогон**

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go build ./...`
Expected: PASS/чисто во всех пакетах.

- [ ] **Step 6: Commit**

```sh
git add README.md CLAUDE.md docs/integration-run.md
git commit -m "docs: chat edit — README, CLAUDE.md (8 команд), integration-run (мутационные флаги)"
```

---

## Риски и альтернативы

**Риски**

1. **Формат PUT-ответа не проверен на живом API** (мутация; спека §3 явно это фиксирует). Митигировано трёхслойно: unit-фикстура `testdata/chat_edit_response.json` смоделирована по официальной документации (data = системное сообщение `message_edited`, parent = обновлённое сообщение — сверено с актуальной версией docs «Editing a chat message»); клиент парсит только `parent.id` (минимальная поверхность контракта); интеграционный цикл Task 4 — источник истины. Если живой ответ отличается (например, `202 Accepted` без `parent`) — фикстура правится под реальность, guard-ы клиента не меняются.
2. **Интерфейс `TalkClient` растёт → 6 тестовых заглушек перестают компилироваться.** Это осознанно ловится compile-time проверками `var _ TalkClient = ...` в каждом файле. Механические стабы — Task 2 Step 3; пропустить нельзя (упадёт сборка пакета).
3. **Два канона с hardcoded-перечнями падают при добавлении команды** (`TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`) — так задумано (анти-drift). Поэтому routes-запись, cmdSpecs-запись и правка канонов идут в одном task/коммите (Task 3); разнос роняет прогон.
4. **Зависание на TTY в анти-drift prong 1 для `--name`:** `chatEditHandler` (как `chatSendHandler`) читает тело ДО `ResolveRoom`, при nil-Stdin fallback на `os.Stdin`. Спец-кейс непустого Stdin в `TestAntiDrift_HandlerMatchesDeclaration` расширен на `chat edit` (Task 3 Step 1.4) — та же причина, по которой кейс вводили для send.
5. **Мутационный тест мусорит в тестовой комнате** (исправленное сообщение + system-сообщение `message_edited` не удаляются) — приемлемо по спеке, поведение идентично `TestIntegration_SendMessage`.
6. **Отклонение от буквы спеки по пути фикстуры** (корневой `testdata/` вместо `internal/client/testdata/`) — см. блок «Отклонение от буквы спеки» выше; соответствие реальной конвенции репозитория важнее буквы, правило «реальный формат» соблюдено.

**Альтернативы (отклонены на брейншторме 2026-09-08, спека §1)**

- **B. Флаг `--edit` у `chat send`** — ломает семантику send (создание), конфликтует с `--reply-to`, расширяет и без того самый флагоёмкий handler.
- **C. Блок edit+delete+пометки в истории** — шире запроса; delete и пометка «(ред.)» в `chat show` — явно за рамками (спека §7 «Будущее»).
- **Проверка capability `edit-messages` до запроса** — отклонено: тонкий клиент не строит матрицу возможностей; ошибки сервера (404/405 на старом Talk) достаточно.

## Self-Review (выполнен при написании)

- **Покрытие спеки:** §2 контракт команды → Task 2 (handler + тесты) и Task 3 (help); §3 API → Task 1 (метод+фикстура) и Task 4 (живая проверка); §4 client-слой → Task 1; §5 cli-слой + сопроводительные правки → Task 2/3/5; §6 тестирование → Task 1/2/3/4 (все перечисленные в спеке группы тестов имеют код); §7 будущее — вне плана (корректно). Пункт спеки §2 «вывод id из ответа сервера, не эхом» покрыт `TestEditMessage_Success` (parent.id != id системного сообщения) и `TestChatEdit_PassesArguments` (editId=999 → stdout `999\n`).
- **Плейсхолдеры:** отсутствуют — каждый шаг содержит полный код/команды.
- **Консистентность имён:** `EditMessageOpts.Message`, `EditMessage(ctx, token, messageId, opts)`, `chatEditHandler`, `editId`/`gotEditId` — одинаковы во всех задачах; стабы всех 6 mock-ов используют сигнатуру из Task 2 Step 3.
