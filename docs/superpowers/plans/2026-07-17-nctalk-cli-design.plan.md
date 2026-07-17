# nctalk CLI — Implementation Plan

> **Для исполнителя:** план исполняется через `superpowers:limit-aware-subagent-driven-development` — одна задача = один implementer-цикл (создать пакет/файл + тесты). Каждый шаг с чекбоксом `- [ ]` отдельный. Ссылки вида `§N` указывают на разделы спеки — не пересказывай их, открывай первоисточник: `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md`.

**Цель:** собрать тонкий Go-CLI `nctalk` над Nextcloud Talk (OCS-API), покрывающий 7 примитивов MVP (спека §6).

**Источник:** спека `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md` (все детали API, форматы, фильтры, exit-коды, redact, нормализация URL, модель actorId — там).

**Стек:** Go 1.21+, только стандартная библиотека (`net/http`, `encoding/json`, разбор флагов ручной). `cobra` НЕ подключаем (см. спека §3 — только если станет тесно).

**Архитектура:** изолированное ядро `internal/client` (ничего не знает про CLI/вывод) поверх интерфейса `httpDoer` → мокается в тестах; `internal/cli` поверх клиента через интерфейс; `internal/render` отдельно. Слои и ответственность — спека §4.

**Сборка и тесты:**
```sh
go build ./...                  # должно собираться без ошибок
go test ./...                   # весь unit-test набор, зелёный
go test -tags=integration ./... # боевой сервер, НЕ в CI (спека §10)
```

**Module path:** `github.com/stas/nctalk` (можно заменить на локальный — единообразно в `go.mod` и импортах).

## Глобальные ограничения (действуют на каждую задачу)

- Go 1.21+; только stdlib; без внешних зависимостей без явной нужды.
- Креды — ТОЛЬКО из env (`NEXTCLOUD_URL`/`NEXTCLOUD_LOGIN`/`NEXTCLOUD_PASS`/`NEXTCLOUD_TIMEOUT`), никогда из argv, никогда в выводе/ошибках (спека §5).
- Все HTTP-запросы идут с `context.Context` (отмена по Ctrl-C).
- Заголовки на каждый запрос: `Authorization: Basic ...`, `OCS-APIRequest: true`, `Accept: application/json`. На mutation-запросы дополнительно `Content-Type: application/json` (спека §5, §9).
- `http.Client.CheckRedirect` — same-host only; cross-host → ошибка, редирект НЕ следуем (спека §5).
- Запрет `httputil.DumpRequestOut` / `DumpResponse` даже в debug.
- Комментарии и тексты вывода — кириллица (как в спеке).
- Коммиты БЕЗ AI-атрибуции (никакого `Co-Authored-By: Claude`).
- Идентификатор «человека» во всех фильтрах — `actorId` (userId), НЕ `displayName` (спека §8).

---

## Этап 0 — Скелет

### Task 0.1: `go.mod`, структура пакетов, заглушка `main.go`

**Files:**
- Create: `go.mod`
- Create: `cmd/nctalk/main.go`
- Create: `internal/config/doc.go`, `internal/client/doc.go`, `internal/cli/doc.go`, `internal/render/doc.go` (пакетные комментарии-заглушки)
- Create: `testdata/.gitkeep`

**Interfaces:**
- Produces: module path `github.com/stas/nctalk`; пустые пакеты `internal/{config,client,cli,render}` готовы к наполнению.

**Inside:**
- `go.mod`: `module github.com/stas/nctalk`, `go 1.21`.
- `cmd/nctalk/main.go`: пустой `func main()` с `// TODO: wire config → client → cli`. Дополнительный файл `internal/cli/exit.go` НЕ здесь (см. Task 0.2).
- `doc.go` в каждом внутреннем пакете: однострочный комментарий пакета (напр. `// Package client содержит HTTP-клиент Nextcloud Talk (ядро).`).

- [ ] **Step 1: создать `go.mod`** — `go mod init github.com/stas/nctalk`, затем руками выставить `go 1.21` в файле.
- [ ] **Step 2: создать структуру директорий и `doc.go`-заглушки.**
- [ ] **Step 3: создать `cmd/nctalk/main.go` (заглушка).**
- [ ] **Step 4: проверить сборку** — `go build ./...` → 0 ошибок.
- [ ] **Step 5: commit** — `git add -A && git commit -m "chore: skeleton — go.mod, package layout, main stub"`.

**Definition-of-done:** `go build ./...` собирается без ошибок; `go vet ./...` чистый; структура директорий совпадает со спекой §4.

### Task 0.2: контракт exit-кодов

**Files:**
- Create: `internal/cli/exit.go`
- Create: `internal/cli/exit_test.go`

**Interfaces:**
- Produces: именованные константы exit-кодов и тип ошибок с привязанным кодом.

**Inside:**
- Константы (спека §7): `ExitOK=0`, `ExitError=1` (сеть/авторизация), `ExitNotFound=2`, `ExitAmbiguous=3`.
- Тип `ExitError struct { Code int; Err error }` с методом `Error() string` и хелпером `Exit(code int, err error) ExitError`.
- Функция `Run(args []string, deps Deps) int` — здесь НЕ реализуется (это Task 4.1), только каркас для exit.

- [ ] **Step 1: написать тест** на `ExitError.Code()`/`Error()` (table-driven: code+message → корректная строка и код).
- [ ] **Step 2: проверить что тест падает** (`go test ./internal/cli/...`).
- [ ] **Step 3: реализовать `exit.go` (константы + тип + хелпер).**
- [ ] **Step 4: тест зелёный.**
- [ ] **Step 5: commit** — `feat(cli): exit-code contract`.

**DoD:** тест `TestExitError` проходит; имена констант совпадают со спекой §7.

---

## Этап 1 — `config`

### Task 1.1: Чтение и валидация env, нормализация URL, дефолт timeout

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Interfaces:**
- Consumes: env (`NEXTCLOUD_URL`, `NEXTCLOUD_LOGIN`, `NEXTCLOUD_PASS`, `NEXTCLOUD_TIMEOUT`).
- Produces:
  ```go
  type Config struct {
      BaseURL  string // нормализованный, без trailing slash
      Login    string
      Password string
      Timeout  time.Duration // дефолт 30s
  }
  func Load() (Config, error) // читает env, валидирует, нормализует
  ```

**Inside:**
- Чтение env; обязательные `NEXTCLOUD_URL` и `NEXTCLOUD_LOGIN` — при отсутствии возвращается понятная ошибка (без утечки значений).
- `NEXTCLOUD_PASS` тоже обязателен (пустой пароль → ошибка).
- `NEXTCLOUD_TIMEOUT` парсится `time.ParseDuration`; при пустом → `30 * time.Second`; при ошибке парсинга — понятная ошибка.
- **Нормализация URL** (спека §5): трим trailing slash; итоговый путь клеится через `path.Join`/ручную склейку без двойных слэшей. Схема проверяется (`http`/`https`). Userinfo в URL НЕ допускается (ошибка). Canonical-форма после 301 в конфиг НЕ перезаписывается.
- Никаких логов креденшалов; ошибка валидации содержит имя переменной, но не значение.

- [ ] **Step 1: table-driven тесты** — `(envSet, wantConfig, wantErr)`:
  - полный валидный набор → корректная Config;
  - `NEXTCLOUD_URL` с trailing slash → обрезан;
  - `NEXTCLOUD_URL` с двойным слэшом в пути → схлопнут;
  - `NEXTCLOUD_URL` без схемы → ошибка;
  - `NEXTCLOUD_URL` с userinfo → ошибка;
  - пустой `NEXTCLOUD_LOGIN` → ошибка "обязательный NEXTCLOUD_LOGIN";
  - пустой `NEXTCLOUD_PASS` → ошибка;
  - пустой `NEXTCLOUD_TIMEOUT` → `30s`;
  - невалидный `NEXTCLOUD_TIMEOUT` (`"abc"`) → ошибка.
- [ ] **Step 2: проверить что тесты падают.**
- [ ] **Step 3: реализовать `config.go`.** Использовать `os.Getenv` и `net/url` для парсинга/валидации.
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(config): env loading, URL normalization, default timeout`.

**DoD:** все случаи выше проходят; `go vet ./internal/config/...` чистый; в ошибках НЕТ значений креденшалов.

---

## Этап 2 — `client` (ядро)

### Task 2.1: Базовый `TalkClient`, интерфейс `httpDoer`, OCS-конверт, redact, redirect-политика

**Files:**
- Create: `internal/client/client.go`
- Create: `internal/client/ocs.go`
- Create: `internal/client/redact.go`
- Create: `internal/client/types.go`         // domain types: Message, MsgParam (единое определение в пакете)
- Create: `internal/client/paths.go`         // именованные константы путей эндпоинтов
- Create: `internal/client/client_test.go`

**Interfaces:**
- Consumes: `config.Config` (из Task 1.1).
- Produces:
  ```go
  type httpDoer interface { Do(*http.Request) (*http.Response, error) }

  // Domain types (спека §12). Единое определение Message в этом пакете —
  // используется и в Room.LastMessage (Task 2.2), и в GetChat (Task 2.5).
  // НЕ переопределять в других задачах (иначе compile error «type redeclared»).
  type Message struct {
      Id                  int                 `json:"id"`
      ActorType           string              `json:"actorType"`
      ActorId             string              `json:"actorId"`
      ActorDisplayName    string              `json:"actorDisplayName"`
      MessageType         string              `json:"messageType"`      // "comment" / "system" / ...
      SystemMessage       string              `json:"systemMessage"`
      Message             string              `json:"message"`          // с плейсхолдерами {file}/{actor}/{mention-*}
      MessageParameters   map[string]MsgParam `json:"messageParameters"` // key=placeholder name
      Reactions           map[string]int      `json:"reactions"`        // emoji → count
      ReferenceId         string              `json:"referenceId"`
      Timestamp           int64               `json:"timestamp"`        // СЕКУНДЫ Unix
      IsReplyable         bool                `json:"isReplyable"`
      Markdown            bool                `json:"markdown"`
      ThreadId            int                 `json:"threadId"`
      ExpirationTimestamp int                 `json:"expirationTimestamp"`
      Token               string              `json:"token"`
  }

  type MsgParam struct {
      Type string `json:"type"`            // "file"/"user"/...
      Id   string `json:"id"`
      Name string `json:"name"`
      Path string `json:"path,omitempty"`
      Link string `json:"link,omitempty"`
  }

  // Именованные канонические пути эндпоинтов (спека §6/§12). Правки — в одном месте.
  // Каждая последующая задача (ListRooms, GetChat, SendMessage, GetReactions, ...)
  // ссылается на эти константы, а не хардкодит строку.
  const (
      pathRooms = "/ocs/v2.php/apps/spreed/api/v4/room"  // ListRooms (Task 2.2); v4 — канонический
      // pathChat, pathReaction, pathSearchRooms, pathSearchMessages
      // добавляются по мере реализации соответствующих задач.
  )

  type TalkClient struct { /* unexported: cfg, doer, baseURL *url.URL */ }
  func NewTalkClient(cfg config.Config) *TalkClient
  func NewTalkClientWithDoer(cfg config.Config, doer httpDoer) *TalkClient // для тестов

  // OCS-конверт (generic): ocs.data распаковывается в целевой тип.
  type OCSEnvelope[T any] struct {
      OCS struct {
          Meta struct { Status string; StatusCode int; Message string } `json:"meta"`
          Data T `json:"data"`
      } `json:"ocs"`
  }

  // Внутренний метод:
  func (c *TalkClient) doOCS(ctx context.Context, method, path string, body io.Reader, mutate bool, out any) error

  // Redact:
  func SanitizeURL(s string) string // → scheme://host[path], без userinfo/query
  ```

**Inside:**
- `TalkClient` хранит `cfg`, `doer httpDoer`, распарсенный `baseURL *url.URL`.
- Конструктор `NewTalkClient` использует `http.DefaultClient` с настроенным `CheckRedirect`.
- `CheckRedirect` (спека §5): для каждого редиректа сравниваем `redirect.URL.Host == req.URL.Host`:
  - **совпадение хостов** → вернуть `nil` (следуем редиректу; `Authorization` переотправляется на тот же хост стандартным механизмом `http.Client`, это безопасно);
  - **несовпадение хостов** → вернуть `errors.New("cross-host redirect blocked: " + redirect.URL.Host)` (НЕ следуем; `http.Client` возвращает ошибку наверх, клиентский вызов получает её через `doOCS`).
  - `http.ErrUseLastResponse` НЕ используем (он оставляет редирект «висеть» без ошибки и требует ручной обработки тела). Лимит редиректов — стандартный (`http.DefaultClient`’s 10).
- `doOCS`:
  - собирает URL через `c.baseURL` + path (без двойных слэшей);
  - ставит заголовки `Authorization: Basic <base64(login:pass)>`, `OCS-APIRequest: true`, `Accept: application/json`; если `mutate=true` — ещё `Content-Type: application/json`;
  - использует `ctx`;
  - декодирует тело в `OCSEnvelope[out]`; при `meta.statusCode >= 400` возвращает ошибку с `meta.message`;
  - все ошибки пропускаются через `sanitizeErr` (см. ниже).
- `redact.go`: `SanitizeURL` парсит строку через `url.Parse`, возвращает `scheme://host[+path]` без userinfo и query; при ошибке парсинга возвращает `<invalid url>`. Хелпер `sanitizeErr(err error) error` рекурсивно разворачивает `*url.Error` и заменяет URL на санитизированный.
- Запрет `httputil.DumpRequestOut`/`DumpResponse` — комментарием в пакете и нигде не используем.

- [ ] **Step 1: тесты `client_test.go`** (через `httptest.Server`):
  - запрос доходит с правильными заголовками (`Authorization`, `OCS-APIRequest`, `Accept`);
  - successful OCS-конверт распаковывается в целевой тип;
  - OCS-error (`meta.statusCode=404`) → возвращается ошибка с `message`;
  - `*url.Error` проходит через `sanitizeErr` → в тексте НЕТ userinfo/query;
  - **cross-host redirect блокируется**: сервер отдаёт 302 на другой хост → клиент возвращает ошибку, на второй хост запрос НЕ идёт (подсчитать счётчиком на тест-сервере);
  - **same-host redirect разрешается**: 302 на тот же хост → следуем, получаем итоговый ответ;
  - `mutate=true` добавляет `Content-Type: application/json`.
- [ ] **Step 2: проверить что тесты падают.**
- [ ] **Step 3: реализовать `client.go`, `ocs.go`, `redact.go`, `types.go` (Message/MsgParam), `paths.go` (pathRooms и др.).**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): TalkClient core, OCS envelope, redact, same-host redirect policy`.

**DoD:** все 7 тестов выше проходят; в выводе любой ошибки НЕТ пароля, userinfo, query. Для подтверждения redact-тест намеренно строит URL вида `https://user:secret@host/path?x=1` и проверяет отсутствие `secret`/`x=1` в результате.

### Task 2.2: `ListRooms`

**Files:**
- Create: `internal/client/rooms.go`
- Create: `internal/client/rooms_test.go`
- Create: `testdata/rooms_list.json` (обезличенная фикс-выдача `/ocs/v2.php/apps/spreed/api/v4/room`, несколько комнат типов 1/2/3 и одна типа 4/5/6).

**Interfaces:**
- Produces:
  ```go
  type RoomType int // 1=one-to-one, 2=group, 3=public, 4/5/6=former (спека §12)

  type Room struct {
      Type            RoomType            `json:"type"`
      Token           string              `json:"token"`
      DisplayName     string              `json:"displayName"`
      UnreadMessages  int                 `json:"unreadMessages"`
      ActorType       string              `json:"actorType"`
      ActorId         string              `json:"actorId"` // для type=1 — собеседник
      LastMessage     *Message            `json:"lastMessage,omitempty"` // Message — из Task 2.1 (domain types, types.go)
      // плюс listable и пр. по мере надобности
  }

  type ListRoomsOpts struct {
      Type           *RoomType // nil = без фильтра
      UnreadOnly     bool
      IncludeFormer  bool // показывать type 4/5/6
  }

  func (c *TalkClient) ListRooms(ctx context.Context, opts ListRoomsOpts) ([]Room, error)
  ```

**Inside:**
- `GET pathRooms` (константа из Task 2.1 = `/ocs/v2.php/apps/spreed/api/v4/room`, спека §6/§12) с заголовками OCS. Это единственный канонический путь ListRooms; варианты `v1/room` в коде НЕ использовать.
- Применение фильтров **на клиенте**: по `opts.Type`, `opts.UnreadOnly`, `opts.IncludeFormer` (по умолчанию type 4/5/6 скрыт — спека §6 `rooms list`).
- Тип `Message` для `LastMessage` берётся из Task 2.1 (`internal/client/types.go`) — единое определение во всём пакете. В этой задаче `Message` НЕ определять и НЕ расширять (иначе `type redeclared`).

- [ ] **Step 1: фикстура `testdata/rooms_list.json`** — 4-5 комнат разных типов; для type=1 указать `actorId` собеседника.
- [ ] **Step 2: тесты** — разбор фикс-ответа через httptest; проверка:
  - все поля смаппились (включая `ActorId`);
  - фильтр `Type=2` оставляет только группы;
  - `UnreadOnly=true` оставляет только непрочитанные;
  - без `IncludeFormer` type 4/5/6 скрыты; с `IncludeFormer=true` — видны.
- [ ] **Step 3: реализовать `rooms.go`** (метод `ListRooms`; `Message` уже определён в Task 2.1 — НЕ дублировать).
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): ListRooms with client-side type/unread/former filters`.

**DoD:** тест `TestListRooms` проходит; типы комнат 4/5/6 по умолчанию скрыты.

### Task 2.3: `FindRooms` (клиентский фильтр по подстроке + `actorId`)

**Files:**
- Modify: `internal/client/rooms.go`
- Modify: `internal/client/rooms_test.go`

**Interfaces:**
- Produces:
  ```go
  // FindRooms возвращает ВСЕ совпадения (одно или несколько). Empty → пустой срез, не ошибка.
  // query — case-insensitive подстрока по DisplayName.
  // actorId — необязательный фильтр; при непустом искать в т.ч. совпадение actorId (для type=1).
  func (c *TalkClient) FindRooms(ctx context.Context, query, actorId string) ([]Room, error)
  ```

**Inside:**
- Поверх `ListRooms(ctx, ListRoomsOpts{IncludeFormer: true})` (чтобы find искал по всем).
- `query` — `strings.Contains(strings.ToLower(r.DisplayName), strings.ToLower(query))`.
- `actorId != ""` → дополнительно фильтр `r.ActorId == actorId` (точное совпадение). Спека §8: `--user` принимает actorId.
- Возвращает ВСЕ совпадения (правило «exit 3/неоднозначно» здесь НЕ применяется — спека §6 `rooms find`).

- [ ] **Step 1: тесты** — table-driven:
  - подстрока совпадает с 1 комнатой → срез длины 1;
  - подстрока совпадает с 2 → срез длины 2 (не ошибка);
  - `actorId="alice"` → только комнаты где `ActorId == "alice"`;
  - ничего не найдено → пустой срез, nil error.
- [ ] **Step 2: проверить падение.**
- [ ] **Step 3: реализовать `FindRooms`.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): FindRooms client-side filter (displayName substring + actorId)`.

**DoD:** `TestFindRooms` покрывает все 4 случая; пустой результат — nil error.

### Task 2.4: `SearchRooms` (Unified talk-conversations)

**Files:**
- Create: `internal/client/search.go`
- Create: `internal/client/search_test.go`
- Create: `testdata/search_conversations.json` (обезличенный ответ `talk-conversations/search`).

**Interfaces:**
- Produces:
  ```go
  type ConversationResult struct {
      Title string
      Token string // из attributes.conversation
  }
  func (c *TalkClient) SearchRooms(ctx context.Context, term string, limit int) ([]ConversationResult, error)
  ```

**Inside:**
- `GET /ocs/v2.php/search/providers/talk-conversations/search?term=…&limit=…`.
- `term` обязателен непустой (мин длина 1 — спека §8): при пустом — клиентская ошибка `"term не может быть пустым"`.
- Разбор: `data.entries[]` → `title`, `attributes.conversation` (это token).
- Серверный 400 (если вдруг прошёл пустой term) → показать серверный `message` (спека §8).

- [ ] **Step 1: фикстура `testdata/search_conversations.json`.**
- [ ] **Step 2: тесты** — разбор фикс-ответа; проверка:
  - корректное извлечение token из `attributes.conversation`;
  - пустой `term` → клиентская ошибка ДО запроса (использовать `c.doer`-mock, счётчик вызовов = 0);
  - несколько entries → срез соответствующей длины;
  - 0 entries → пустой срез, nil error.
- [ ] **Step 3: реализовать `search.go`.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): SearchRooms via Unified talk-conversations provider`.

**DoD:** `TestSearchRooms` проходит; пустой term отсекается на клиенте без сетевого вызова.

### Task 2.5: `GetChat` (пагинация `limit=200` + `lastKnownMessageId`)

**Files:**
- Create: `internal/client/chat.go`
- Create: `internal/client/chat_test.go`
- (фикстуры генерируются программно внутри теста — см. Step 1; `testdata/` файлы для этой задачи НЕ создаются).

**Interfaces:**
- Consumes: типы `Message`/`MsgParam` из Task 2.1 (domain types, `internal/client/types.go`). НЕ определять здесь заново.
- Produces:
  ```go
  type GetChatOpts struct {
      Limit              int   // потолок ВЫБОРКИ (не путать с --last в cli); <=0 → без потолка (только одна страница)
      LastKnownMessageId int   // 0 = начать с последнего
      StopBeforeTs       int64 // 0 = без раннего стопа; иначе стоп при сообщении строго старше
  }
  // GetChat возвращает сообщения, упорядоченные от новых к старым.
  // Реализует: страницы по limit=200 (или меньше), перебор lastKnownMessageId назад,
  // lookIntoFuture=0, ранний стоп при StopBeforeTs (сообщение строго старше).
  func (c *TalkClient) GetChat(ctx context.Context, token string, opts GetChatOpts) ([]Message, error)
  ```

**Inside:**
- `GET /ocs/v2.php/apps/spreed/api/v1/chat/{token}?limit=200&lookIntoFuture=0&lastKnownMessageId=…` (спека §6 `chat show`, §12).
- Пагинация: пока (накоплено < `opts.Limit`) И (получили 200 на предыдущей странице) И (не дошли до начала чата — пустая страница) И (ранний стоп не сработал) — тянем следующую страницу с `lastKnownMessageId` = id самого старого из только что полученных.
- Ранний стоп: если в странице попалось сообщение с `Timestamp < opts.StopBeforeTs` — обрезаем выборку до него (включительно или нет — согласовать: «строго старше» = не включаем) и прекращаем пагинацию.
- Важно: `opts.Limit` — это потолок выборки (то что в cli будет `--last` или cap 200), НЕ серверный `limit=` (всегда 200).
- Тип `Message` берётся из Task 2.1 — единое определение; здесь только метод `GetChat` и логика пагинации поверх готового типа, без переопределения `Message`/`MsgParam`.

- [ ] **Step 1: программная генерация фикстур в тесте** — написать хелпер `genMessages(start, end int) []Message` (или `string` JSON), который собирает page1: 200 сообщений (IDs 1000..801), page2 (`lastKnownMessageId=801`): 50 сообщений (IDs 800..751). Файлы в `testdata/` НЕ создавать — фикс-данные живут в `_test.go`. Тест-сервер (`httptest`) раздаёт сгенерированные страницы по запросу с соответствующим `lastKnownMessageId`.
- [ ] **Step 2: тесты**:
  - одна страница при `Limit=20` (тянем 200, обрезаем до 20) — фактически срез `[0:20]`;
  - две страницы при `Limit=210` (прокручиваем `lastKnownMessageId` один раз);
  - ранний стоп: `StopBeforeTs` между сообщениями page1 — пагинация прекращается, обрезаем по нему;
  - пустой чат (сервер отдал `[]`) → пустой срез, nil error, НЕ зацикливаемся.
- [ ] **Step 3: реализовать `chat.go`** (только `GetChatOpts` + метод `GetChat`; `Message`/`MsgParam` брать из Task 2.1).
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): GetChat with limit=200 pagination, lastKnownMessageId, early stop`.

**DoD:** `TestGetChat_*` покрывает 4 случая; пагинация корректно перебирает `lastKnownMessageId`; нет бесконечного цикла на пустом чате.

### Task 2.6: `SendMessage` (POST)

**Files:**
- Modify: `internal/client/chat.go`
- Modify: `internal/client/chat_test.go`

**Interfaces:**
- Produces:
  ```go
  type SendMessageOpts struct {
      Message     string // обязательно
      ReplyTo     int    // 0 = без reply
      Silent      bool
      ReferenceId string // опц., UUID для client-side dedup
  }
  // Возвращает id нового сообщения (из ocs.data.id).
  func (c *TalkClient) SendMessage(ctx context.Context, token string, opts SendMessageOpts) (int, error)
  ```

**Inside:**
- `POST /ocs/v2.php/apps/spreed/api/v1/chat/{token}` с `Content-Type: application/json` (`mutate=true` в `doOCS`).
- Тело: `{"message":"…","replyTo":<int,опц.>,"silent":<bool,опц.>,"referenceId":"<uuid,опц.>"}` (спека §6 `chat send`).
- Пустой `opts.Message` → клиентская ошибка.
- Ответ: `ocs.data.id` → int. При невалидном `replyTo` сервер вернёт OCS-error (спека §9) — показать его `message`.
- Формат тела НЕ проверен на живом API (риск — см. раздел «Риски»).

- [ ] **Step 1: тесты**:
  - успешная отправка: сервер ответил 200 c `data.id=12345` → функция вернула `12345`; проверка тела запроса содержит правильный JSON с `message`/`silent`;
  - с `ReplyTo=42` → тело содержит `"replyTo":42`;
  - пустой `Message` → клиентская ошибка, сетевого вызова нет;
  - OCS-error (сервер отдал `meta.statusCode=400`, `message="invalid replyTo"`) → функция возвращает ошибку с текстом `"invalid replyTo"`.
- [ ] **Step 2: реализовать `SendMessage`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(client): SendMessage POST chat with replyTo/silent/referenceId`.

**DoD:** все 4 случая выше проходят; тело запроса в точности соответствует спеке §6.

### Task 2.7: `GetReactions`

**Files:**
- Create: `internal/client/reactions.go`
- Create: `internal/client/reactions_test.go`
- Create: `testdata/reactions.json` (мапа `emoji → [актёры]`), `testdata/reactions_empty.json` (пустой объект `{}`).

**Interfaces:**
- Produces:
  ```go
  type ReactionActor struct {
      ActorType        string `json:"actorType"`
      ActorId          string `json:"actorId"`
      ActorDisplayName string `json:"actorDisplayName"`
      Timestamp        int64  `json:"timestamp"`
  }
  // map[emoji][]ReactionActor. Пустой результат → пустая map, nil error.
  func (c *TalkClient) GetReactions(ctx context.Context, token string, messageId int) (map[string][]ReactionActor, error)
  ```

**Inside:**
- `GET /ocs/v2.php/apps/spreed/api/v1/reaction/{token}/{messageId}` (спека §6 `reactions get`, §12).
- Разбор `ocs.data` как `map[string][]ReactionActor`.
- Пустой `data={}` → пустая map, **nil error** (спека §6: exit 0, «реакций нет»).

- [ ] **Step 1: фикстуры.**
- [ ] **Step 2: тесты**:
  - непустой ответ → map с эмодзи и актёрами;
  - пустой ответ `{}` → пустая map, nil error;
  - id сообщения подставляется в URL.
- [ ] **Step 3: реализовать `reactions.go`.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): GetReactions; empty data → empty map, no error`.

**DoD:** `TestGetReactions` и `TestGetReactions_Empty` проходят; пустой случай НЕ возвращает ошибку.

### Task 2.8: `SearchMessages` (Unified talk-message, курсор)

**Files:**
- Modify: `internal/client/search.go`
- Modify: `internal/client/search_test.go`
- Create: `testdata/search_messages_page1.json`, `testdata/search_messages_page2.json` (две страницы с разными `cursor`/`isPaginated`).

**Interfaces:**
- Produces:
  ```go
  type MessageResult struct {
      Title       string // имя автора
      Subline     string // текст сообщения
      ResourceUrl string // …/call/{token}#message_{id}
      Attributes  struct {
          Conversation string // = token
          MessageId    int
          ActorType    string
          ActorId      string
          Timestamp    int64  // ИЗ СТРОКИ — нормализовать в int (спека §12)
      }
  }
  type SearchMessagesOpts struct {
      From     string // actorId → маппится в person= ; пусто = без фильтра
      Limit    int    // 1..25, дефолт 10
      All      bool   // собрать несколько страниц, cap 5 (спека §6 search)
  }
  func (c *TalkClient) SearchMessages(ctx context.Context, term string, opts SearchMessagesOpts) ([]MessageResult, error)
  ```

**Inside:**
- `GET /ocs/v2.php/search/providers/talk-message/search?term=…&person=…&limit=…&cursor=…`.
- `term` непустой (мин 1 символ) — клиентский чек; пустой → ошибка.
- `limit` нормализуется: `<1` → 10; `>25` → 25 (спека §6 search).
- Пагинация: по умолчанию ОДНА страница. При `All=true` — перебираем `cursor` из ответа, стоп при `isPaginated=false` или пустом `cursor`; cap 5 страниц (спека §6).
- `attributes.timestamp` приходит СТРОКОЙ — парсить в int64 (спека §8, §12). Для этого в JSON-декодере использовать промежуточный тип `string` и конвертировать, ИЛИ кастомный `UnmarshalJSON`.
- Разбор `resourceUrl` в `MessageResult.Attributes.MessageId` НЕ нужен — берём из `attributes.messageId`.

- [ ] **Step 1: фикстуры двух страниц** с `cursor` и `isPaginated=true`/`false`.
- [ ] **Step 2: тесты**:
  - одна страница по умолчанию;
  - `All=true` + сервер отдаёт 2 страницы (вторая с `isPaginated=false`) → собраны обе;
  - `All=true` + сервер отдаёт 6 страниц (все `isPaginated=true`) → клиент берёт ровно 5 (cap);
  - пустой `term` → клиентская ошибка без запроса;
  - `timestamp="1752710400"` (строка) → корректный `int64(1752710400)`;
  - `Limit=99` → серверу уходит `limit=25`.
- [ ] **Step 3: реализовать.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(client): SearchMessages via talk-message, cursor pagination (cap 5), timestamp normalization`.

**DoD:** все 6 случаев проходят; timestamp-нормализация покрыта отдельным кейсом.

---

## Этап 3 — `render`

### Task 3.1: Текстовые таблицы и JSON-вывод

**Files:**
- Create: `internal/render/render.go`
- Create: `internal/render/render_test.go`

**Interfaces:**
- Consumes: типы `client.Room`, `client.Message`, `client.ConversationResult`, `client.MessageResult`, `client.ReactionActor`, `map[string][]client.ReactionActor`.
- Produces:
  ```go
  // Все функции принимают io.Writer + срез/значение; возвращают error.
  func RoomsTable(w io.Writer, rooms []client.Room) error
  func RoomsJSON(w io.Writer, rooms []client.Room) error
  func MessagesTable(w io.Writer, msgs []client.Message) error
  func MessagesJSON(w io.Writer, msgs []client.Message) error
  func ConversationResultsTable(w io.Writer, rs []client.ConversationResult) error
  func ConversationResultsJSON(w io.Writer, rs []client.ConversationResult) error
  func MessageResultsTable(w io.Writer, rs []client.MessageResult) error
  func MessageResultsJSON(w io.Writer, rs []client.MessageResult) error
  func ReactionsText(w io.Writer, rs map[string][]client.ReactionActor) error  // пустая map → "реакций нет"
  func ReactionsJSON(w io.Writer, rs map[string][]client.ReactionActor) error  // --json-ветка для reactions get
  func NewMessageID(w io.Writer, id int) error                                  // вывод id отправленного (текст)
  func NewMessageIDJSON(w io.Writer, id int) error                              // --json-ветка для chat send
  func Candidates(w io.Writer, rooms []client.Room) error                       // ТИП|имя|token для exit 3

  // FormatTime форматирует unix-секунды в локальной TZ процесса в "2006-01-02 15:04:05".
  // Живёт в Этапе 3.1 (а не 3.2), потому что нужен самим таблицам
  // RoomsTable/MessagesTable/MessageResultsTable (колонка времени) — без неё они не
  // компилируются. Текст `"--"` для ts==0 (сообщение без timestamp) — на усмотрение.
  func FormatTime(ts int64) string
  ```

**Inside:**
- Таблица ручная (без внешних libs): простой текстовый формат с выравниванием через `text/tabwriter` (stdlib).
- `FormatTime(ts)` — `time.Unix(ts, 0).Local().Format("2006-01-02 15:04:05")` (спека §6, локальная TZ процесса).
- Формат `rooms list` (спека §6): `ТИП | ЧАТ | TOKEN | НЕПРОЧИТАНО | ПОСЛЕДНЕЕ (время, автор, превью)`. Всегда присутствует `actorId` (отдельная колонка или в скобках). Для `--json` поле `actorId` обязательно.
- Формат `chat show` (спека §6): построчно `[id] время автор: текст   👍×2 ✅×1`. Реакции-счётчики берутся из `Message.Reactions` БЕЗ N+1.
- Формат `search`: `время | автор | chat#messageId | текст`.
- Формат `reactions get`: `реакция → [авторы]`; пустая map → строка `реакций нет`.
- JSON — `json.NewEncoder(w).SetIndent("", "  ")`; поля соответствуют спеке (включая `actorId`). `ReactionsJSON` сериализует map как есть (`{"👍":[{"actorId":...}...]}`); `NewMessageIDJSON` — `{"id": <int>}`.

- [ ] **Step 1: table-driven тесты** для каждой функции — на типовых входах проверить:
  - содержит ожидаемые подстроки (имя комнаты, token, actorId, время, превью, реакции);
  - для JSON — `json.Unmarshal` обратный парсинг;
  - `ReactionsText` на пустой map выводит `реакций нет`;
  - `ReactionsJSON`/`NewMessageIDJSON` дают валидный JSON с ожидаемыми ключами.
- [ ] **Step 2: table-driven `TestFormatTime`** — фиксированный ts → ожидаемая строка в локальной TZ (`time.Local`).
- [ ] **Step 3: реализовать `render.go` + `FormatTime` (можно разбить на `render_table.go` / `render_json.go` / `time.go`, если файл растёт).**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(render): table/JSON renderers + FormatTime for rooms/messages/search/reactions`.

**DoD:** все renderer-функции покрыты; JSON валиден; `tabwriter`-таблицы читаемы; пустые реакции дают текст `реакций нет`.

### Task 3.2: Подстановка `messageParameters` и форматы времени

**Files:**
- Create: `internal/render/params.go`
- Create: `internal/render/params_test.go`
- Create: `internal/render/time.go`
- Create: `internal/render/time_test.go`

**Interfaces:**
- Produces:
  ```go
  // SubstituteParams заменяет {actor}/{file}/{mention-*} на name из messageParameters.
  // Нераспознанные плейсхолдеры оставляет как есть.
  func SubstituteParams(msg string, params map[string]client.MsgParam) string

  // ParseSince парсит форматы спеки §6 `--since`:
  //   "30s"/"15m"/"2h"/"1d" (относительные от now, локальная TZ);
  //   "2026-07-17" (начало дня 00:00:00, локальная TZ);
  //   "2026-07-17T13:00:00" (локальная TZ);
  //   "2026-07-17T13:00:00+03:00" (как есть).
  // Возвращает unix-секунды. При ошибке парсинга — понятная ошибка.
  func ParseSince(s string) (int64, error)

  // NB: FormatTime перенесён в Task 3.1 (нужен таблицам).
  ```
- NB: после переноса `FormatTime` в 3.1, задача 3.2 не имеет зависимости от 3.1 — их можно исполнять параллельно разными implementer-циклами (3.1 = FormatTime + таблицы, 3.2 = SubstituteParams + ParseSince).

**Inside:**
- `SubstituteParams` (спека §6 `chat show` «Подстановка»):
  - regex `\{(\w[\w-]*)\}`; для каждого ключа искать в `params`:
    - `type=="file"` → подставить `name` (в `--json` прикладывается ссылка — это в render-слое, не здесь; функция только текст);
    - `type=="user"` → подставить `name`;
    - остальные (`mention-*`) → подставить `name` если есть;
  - нераспознанные → оставить как есть.
- `ParseSince` (спека §6 «Форматы `--since`»):
  - сначала пробуем относительные (суффикс `s/m/h/d`): `now - N` в локальной TZ;
  - потом точную дату `2006-01-02` → начало дня;
  - потом `2006-01-02T15:04:05` (без зоны) → локальная TZ;
  - потом RFC3339 `2006-01-02T15:04:05Z07:00` → как есть;
  - всё остальное → ошибка.

- [ ] **Step 1: table-driven `TestSubstituteParams`**:
  - `{file}` → `name` файлового параметра;
  - `{actor}` → `name` пользовательского параметра;
  - `{mention-user2}` → `name` mention-параметра (ключ точно `mention-user2`);
  - `{unknown}` → остаётся `{unknown}`;
  - несколько плейсхолдеров в одном сообщении.
- [ ] **Step 2: table-driven `TestParseSince`**:
  - `"30s"` → now-30s (проверить с допуском ±2s);
  - `"2026-07-17"` → `2026-07-17 00:00:00 local`;
  - `"2026-07-17T13:00:00"` → `13:00 local`;
  - `"2026-07-17T13:00:00+03:00"` → `10:00 UTC` (= эквивалент);
  - `"abc"` → ошибка.
- [ ] **Step 3: реализовать `params.go` (`SubstituteParams`) и `time.go` (`ParseSince`). `FormatTime` здесь НЕ трогаем — он в Task 3.1.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(render): messageParameters substitution + --since parsing`.

**DoD:** все случаи выше проходят; `ParseSince` толерантен к локальной TZ (использовать `time.Local`).

---

## Этап 4 — `cli`

### Task 4.1: Роутинг `<resource> <verb>`, глобальный `--json`, точка входа `Run`

**Files:**
- Create: `internal/cli/cli.go`
- Create: `internal/cli/cli_test.go`
- Modify: `internal/cli/exit.go` (если нужно расширить)

**Interfaces:**
- Consumes: типы `client.TalkClient` (через интерфейс, см. ниже), `render.*`, `config.Config`.
- Produces:
  ```go
  // Клиентский интерфейс для mockability. Полностью покрывает 7 методов TalkClient.
  type TalkClient interface {
      ListRooms(ctx context.Context, opts client.ListRoomsOpts) ([]client.Room, error)
      FindRooms(ctx context.Context, query, actorId string) ([]client.Room, error)
      SearchRooms(ctx context.Context, term string, limit int) ([]client.ConversationResult, error)
      GetChat(ctx context.Context, token string, opts client.GetChatOpts) ([]client.Message, error)
      SendMessage(ctx context.Context, token string, opts client.SendMessageOpts) (int, error)
      GetReactions(ctx context.Context, token string, messageId int) (map[string][]client.ReactionActor, error)
      SearchMessages(ctx context.Context, term string, opts client.SearchMessagesOpts) ([]client.MessageResult, error)
  }

  type Deps struct {
      Client TalkClient
      Stdout io.Writer
      Stderr io.Writer
      Now    func() time.Time // для относительных --since в тестах
  }
  // Run разбирает args (без program name), вызывает нужный handler, возвращает exit-код.
  func Run(args []string, deps Deps) int
  ```

**Inside:**
- Разбор `args[0]` = resource (`rooms`/`chat`/`reactions`/`search`), `args[1]` = verb (`list`/`find`/`search`/`show`/`send`/`get`).
- **Special-case `search`**: для ресурса `search` НЕТ verb-уровня (нет `list`/`find`/`show`/...) — `args[1+]` интерпретируются сразу как `term` + флаги (`--from`/`--limit`/`--all`/`--json`), спека §6 `search`. Это не ломает общий шаблон `<resource> <verb>` для остальных ресурсов, но роутер должен явно различать: `search` → сразу в `handlers_search.go` с term = первый не-флаг аргумент; `rooms`/`chat`/`reactions` → двухуровневый разбор (`args[1]` = verb).
- Глобальный `--json` (можно в любом месте args) — парсится вручную и убирается из args перед роутингом.
- Хендлеры пока — заглушки, возвращающие `ExitError{Code: ExitError, Err: errors.New("not implemented")}` (реализация в Task 4.2-4.5c).
- Неизвестный resource/verb → `ExitError{ExitError, "неизвестная команда ..."}`.
- Все сетевые/клиентские ошибки → `ExitError{ExitError, err}` (с уже sanitize-овыми URL).

- [ ] **Step 1: тесты роутинга** (с mock-клиентом):
  - `rooms list` → вызывает соответствующий handler (пока заглушку, проверяем что роутинг свёлся);
  - `chat show` + `--json` → флаг распарсен и сохранён;
  - `search foo` → роутер распознаёт `search` как special-case (БЕЗ verb-уровня), term=`"foo"` доходит до search-хендлера;
  - неизвестная команда → `ExitError` с кодом 1;
  - `--json` может стоять в любой позиции (`rooms --json list`, `rooms list --json`).
- [ ] **Step 2: реализовать `cli.go` (роутинг + заглушки хендлеров).**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): resource/verb routing + global --json + TalkClient interface`.

**DoD:** 7 команд маршрутизируются; неизвестные команды дают exit 1; `--json` парсится в любой позиции.

### Task 4.2: Правило разрешения `<room>` (token positional / `--name` подстрока, exit 2/3)

**Files:**
- Create: `internal/cli/room.go`
- Create: `internal/cli/room_test.go`

**Interfaces:**
- Consumes: `TalkClient.FindRooms` (из Task 2.3).
- Produces:
  ```go
  // ResolveRoom разрешает room для chat/reactions-команд (спека §7).
  //   positional = token (primary): возвращается как есть без сетевого запроса.
  //   nameFlag задан → FindRooms(ctx, nameFlag, "") case-insensitive подстрока.
  //      1 совпадение → его token;
  //      >1 → ExitError{ExitAmbiguous, candidates}; кандидаты печатаются в stderr через render.Candidates;
  //      0 → ExitError{ExitNotFound, "комната не найдена"}.
  // Если заданы И positional И nameFlag → приоритет positional (или ошибка — согласовать: выбираем приоритет positional с предупреждением).
  func ResolveRoom(ctx context.Context, client TalkClient, positional, nameFlag string, stderr io.Writer) (string, error)
  ```

**Inside:**
- Если `positional != ""` → вернуть его (это token).
- Если `nameFlag != ""` → вызвать `FindRooms(ctx, nameFlag, "")`:
  - 1 → `rooms[0].Token`;
  - `>1` → `render.Candidates(stderr, rooms)` + `ExitError{ExitAmbiguous, ...}` (спека §7);
  - 0 → `ExitError{ExitNotFound, "комната не найдена: " + nameFlag}`.
- Если оба пусты → `ExitError{ExitError, "укажите token позиционно или --name"}`.

- [ ] **Step 1: тесты** (mock-клиент возвращает предзаданные срезы):
  - positional `"abc"` → `"abc"`, нет вызова `FindRooms`;
  - `--name` → 1 совпадение → его token;
  - `--name` → 2 совпадения → `ExitAmbiguous`, в stderr выведены кандидаты;
  - `--name` → 0 совпадений → `ExitNotFound`;
  - оба пусты → `ExitError`.
- [ ] **Step 2: реализовать `room.go`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): room resolution rule (token primary, --name substring, exit 2/3)`.

**DoD:** все 5 случаев выше проходят; правило строго соответствует спеке §7.

### Task 4.3: `chat show` — стратегия выборки и фильтры (`--last`/`--from`/`--since`/`--system`)

**Files:**
- Create: `internal/cli/chat.go`
- Create: `internal/cli/chat_test.go`

**Interfaces:**
- Consumes: `ResolveRoom` (Task 4.2), `TalkClient.GetChat` (Task 2.5), `render.MessagesTable/MessagesJSON`, `render.SubstituteParams`, `render.FormatTime`, `render.ParseSince`.
- Produces: реализация хендлера `chat show` поверх `cli.Run`.

**Inside:**
- Флаги (спека §6 `chat show`):
  - `<room>` позиционный = token, ИЛИ `--name <имя>` (через `ResolveRoom`).
  - `--last <N>` (по умолчанию = **0**, сигнал «не задан явно») — потолок ВЫБОРКИ (до фильтров). При ручном разборе флагов отслеживать **факт присутствия токена `--last`/значения** (отдельный bool `lastExplicit`), а не только числовое значение.
  - `--from <actorId>` — фильтр только comment-сообщений по `actorId`.
  - `--since <время>` — фильтр; через `render.ParseSince`.
  - `--system` — показывать system-сообщения (по умолчанию скрыты `messageType != "comment"`).
- Стратегия (спека §6, §8):
  1. **Выбор `limit` (потолок выборки)** — чёткое правило:
     - `last > 0` (т.е. `--last` задан явно с N>0) → `limit = last`;
     - `last == 0` (не задан), но задан `--from` ИЛИ `--since` → `limit = 200` (cap);
     - иначе (ничего не задано) → `limit = 20` (базовый дефолт показа).
     
     Это обеспечивает спеку §6: `chat show --last N --from X` = «среди последних N» (`limit=N`), а `chat show --from X` без `--last` → cap 200 (чтобы фильтр имел материал).
  2. `GetChatOpts.Limit = <потолок выборки>`, `StopBeforeTs = <sinceTs>` (если `--since`).
  3. Локальный фильтр: если `--from` → оставить `messageType=="comment" && actorId==from`; если `--since` → оставить `ts >= sinceTs`.
  4. **Предупреждение в stderr (строгий булев критерий, без «мало»/«много»)**: выводить **только если выполнены ОБА условия**:
     - (а) **достигнут cap** — сервер вернул полную страницу `limit=200` (т.е. `len(выборка до фильтра) == 200`) И известно, что есть более старые сообщения (пагинация не дошла до пустой страницы / `lastKnownMessageId` продолжил бы перебор);
     - (б) `len(результат после фильтра) == 0`.
     
     Текст: «выборка ограничена 200 сообщениями, совпадений не найдено; сузьте диапазон (`--since`) или используйте `search` (серверный фильтр `person=`)». Если `len(результат после фильтра) > 0` — предупреждения НЕТ, даже если достигнут cap.
- Вывод: применить `SubstituteParams` к каждому сообщению; вывод через `render.MessagesTable`/`MessagesJSON` (с `FormatTime`).
- Поиск становится «exit 0 с пустым выводом» только для поисковых команд; `chat show` с 0 сообщений после фильтра → exit 0 с пустым выводом (команда прочитана, просто нет данных).

- [ ] **Step 1: тесты** (mock-клиент возвращает фиксированные сообщения):
  - `--last 5` → вызвался `GetChat` с `Limit=5`; выведены 5;
  - `--from alice` без `--last` → `Limit=200`, фильтр оставил только alice;
  - `--since 2h` без `--last` → `Limit=200`, `StopBeforeTs` задан, фильтр по ts;
  - `--last 5 --from alice` → среди последних 5 от alice (потолок 5, **не** 200);
  - без `--last`/`--from`/`--since` → `Limit=20` (базовый дефолт);
  - `--system` → system-сообщения видны; без `--system` → скрыты;
  - cap 200 + **0 совпадений** после фильтра → в stderr есть предупреждение;
  - cap 200 + **>0 совпадений** после фильтра → предупреждения НЕТ;
  - подстановка `{file}` в выводе присутствует (через `SubstituteParams`);
  - `--name` с неоднозначностью → `ExitAmbiguous`.
- [ ] **Step 2: реализовать `chat.go` (`chat show` handler).**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): chat show with --last/--from/--since/--system, cap 200, early stop`.

**DoD:** все случаи выше проходят; `--last` это потолок выборки; по `--since` срабатывает ранний стоп в `GetChat`.

### Task 4.4: `chat send` — отправка из stdin / `--file`

**Files:**
- Modify: `internal/cli/chat.go`
- Modify: `internal/cli/chat_test.go`

**Interfaces:**
- Consumes: `ResolveRoom`, `TalkClient.SendMessage`, `render.NewMessageID`/`NewMessageIDJSON`.
- Produces: реализация хендлера `chat send`.

**Inside:**
- Флаги (спека §6 `chat send`):
  - `<room>` / `--name` — через `ResolveRoom`.
  - Источник тела: stdin по умолчанию (`-`), либо `--file <path>` (прочитать всё).
  - `--reply-to <int>`, `--silent`, `--reference-id <uuid>`.
- Вызов `SendMessage(ctx, token, SendMessageOpts{Message, ReplyTo, Silent, ReferenceId})`.
- **Вывод (ветвление по глобальному `--json`, спека §6)**: при `--json` → `render.NewMessageIDJSON(stdout, id)`, иначе → `render.NewMessageID(stdout, id)`.
- Пустой stdin / пустой файл → клиентская ошибка ДО запроса.

- [ ] **Step 1: тесты**:
  - тело из stdin → отправлено, выведен id (текстовый формат);
  - тело из stdin + `--json` → в stdout валидный JSON `{"id": <int>}`;
  - тело из `--file` → отправлено;
  - `--reply-to 42 --silent` → параметры дошли до mock-клиента;
  - пустой stdin → `ExitError` без вызова `SendMessage`;
  - OCS-error от сервера → `ExitError{ExitError, msg}`.
- [ ] **Step 2: реализовать `chat send`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): chat send from stdin/--file with reply-to/silent/reference-id`.

**DoD:** все случаи выше проходят; пустое тело отсекается без сетевого вызова.

### Task 4.5a: `rooms list`, `rooms find`, `rooms search` — хендлеры

**Files:**
- Create: `internal/cli/handlers_rooms.go`
- Create: `internal/cli/handlers_rooms_test.go`

**Interfaces:**
- Consumes: `TalkClient.ListRooms`/`FindRooms`/`SearchRooms` (через интерфейс из Task 4.1), `render.RoomsTable`/`RoomsJSON`, `ResolveRoom` при необходимости.
- Produces: хендлеры `rooms list`, `rooms find`, `rooms search`.

**Inside:**
- **`rooms list`** (спека §6): флаги `--type <1|2|3>`, `--unread`, `--include-former`, `--json`. Вызов `ListRooms` с `opts` (type → `*RoomType`, `UnreadOnly`, `IncludeFormer`). Вывод через `render.RoomsTable`/`RoomsJSON` (по `--json`).
- **`rooms find <запрос>`**: флаг `--user <actorId>`. `FindRooms(ctx, query, actorId)`. Пустой результат → exit 0 с пустым выводом (поисковая команда). Вывод таблица/json.
- **`rooms search <term>`**: `SearchRooms(ctx, term, 0)` (limit передаётся как 0 → серверный дефолт, или фикс 25; согласовать). Пустой → exit 0.

- [ ] **Step 1: тесты** — table-driven, mock-клиент:
  - `rooms list --type 2` → `ListRoomsOpts.Type` соответствует;
  - `rooms list --unread` → `UnreadOnly=true`;
  - `rooms find alice --user u-alice` → `FindRooms(ctx, "alice", "u-alice")`;
  - `rooms find` пустой результат → exit 0, пустой stdout;
  - `rooms search xy` → вызов `SearchRooms(ctx, "xy", ...)`, пустой → exit 0;
  - `rooms list --json` → в stdout JSON (через `RoomsJSON`).
- [ ] **Step 2: реализовать `handlers_rooms.go`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): rooms list/find/search handlers`.

**DoD:** все 3 rooms-хендлера работают; пустые результаты дают exit 0 с пустым stdout; `--json` ветвится на `RoomsJSON`.

### Task 4.5b: `reactions get` — хендлер

**Files:**
- Create: `internal/cli/handlers_reactions.go`
- Create: `internal/cli/handlers_reactions_test.go`

**Interfaces:**
- Consumes: `TalkClient.GetReactions` (через интерфейс Task 4.1), `render.ReactionsText`/`ReactionsJSON`, `ResolveRoom`.
- Produces: хендлер `reactions get`.

**Inside:**
- **`reactions get <room> <messageId>`**: `ResolveRoom` + `GetReactions`. Пустая map → `render.ReactionsText` печатает «реакций нет», exit 0 (спека §6, §7). `messageId` парсится из args[2] в int.
- **Вывод (ветвление по глобальному `--json`, спека §6)**: при `--json` → `render.ReactionsJSON(stdout, reps)` (сериализация map как есть), иначе → `render.ReactionsText(stdout, reps)` (текст «реакция → [авторы]»). Пустая map в json-режиме → `{}` (или `null` — согласовать; текстовый режим → «реакций нет»).

- [ ] **Step 1: тесты**:
  - успешный разбор + текстовый вывод;
  - успешный разбор + `--json` → в stdout валидный JSON (`json.Unmarshal` обратный парсинг);
  - пустая map → «реакций нет», exit 0 (текстовый); `--json` + пустая map → exit 0;
  - неоднозначный `--name` → exit 3 (через `ResolveRoom`).
- [ ] **Step 2: реализовать `handlers_reactions.go`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): reactions get handler with --json branch`.

**DoD:** `reactions get` работает в обоих режимах; пустая map возвращает exit 0; `--json` ветвится на `ReactionsJSON`.

### Task 4.5c: `search` — хендлер (Unified talk-message)

**Files:**
- Create: `internal/cli/handlers_search.go`
- Create: `internal/cli/handlers_search_test.go`

**Interfaces:**
- Consumes: `TalkClient.SearchMessages` (через интерфейс Task 4.1), `render.MessageResultsTable`/`MessageResultsJSON`.
- Produces: хендлер `search`.

**Inside:**
- **`search <term>`** (спека §6 search, special-case роутинга из Task 4.1): term = первый не-флаг аргумент из `args[1+]`. Флаги `--from <actorId>` (→ `SearchMessagesOpts.From`), `--limit <N>`, `--all`, `--json`. Вызов `SearchMessages`. Пустой → exit 0.

- [ ] **Step 1: тесты**:
  - `search foo` → `SearchMessagesOpts{Limit:10}` (дефолт);
  - `search foo --from u-alice --limit 25 --all` → все три параметра дошли;
  - пустой результат → exit 0;
  - пустой term (`search ""` или `search` без аргумента) → exit 1 с ошибкой "term не может быть пустым";
  - `search foo --json` → в stdout JSON (через `MessageResultsJSON`).
- [ ] **Step 2: реализовать `handlers_search.go`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(cli): search handler (talk-message) with --json branch`.

**DoD:** `search` работает; пустой term отсекается с exit 1; `--json` ветвится на `MessageResultsJSON`; пустой результат даёт exit 0.

---

## Этап 5 — Сборка в `main`, end-to-end

### Task 5.1: `cmd/nctalk/main.go` — связка config → client → cli

**Files:**
- Modify: `cmd/nctalk/main.go`
- Create: `cmd/nctalk/main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `client.NewTalkClient`, `cli.Run`, `cli.Deps`.
- Produces: рабочий `main()`.

**Inside:**
- `main()`:
  1. `cfg, err := config.Load()`; при ошибке → `fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)`.
  2. `c := client.NewTalkClient(cfg)`.
  3. `code := cli.Run(os.Args[1:], cli.Deps{Client: c, Stdout: os.Stdout, Stderr: os.Stderr, Now: time.Now})`.
  4. `os.Exit(code)`.
- Никаких паник с кредами; всё через `error` + sanitize.

- [ ] **Step 1: e2e тест `main_test.go`** через `httptest.Server` + `os.Setenv`:
  - поднять httptest-сервер с фикс-ответом `/ocs/v2.php/apps/spreed/api/v4/room`;
  - выставить env (`NEXTCLOUD_URL=<server URL>` и т.д.);
  - запустить `cli.Run([]string{"rooms", "list"}, deps)` с реальным `client.NewTalkClient`;
  - проверить что stdout содержит ожидаемые имена комнат и actorId;
  - аналогично — `chat show <token>` с фикс-ответом `/api/v1/chat/{token}`.
- [ ] **Step 2: реализовать `main()`.**
- [ ] **Step 3: `go build ./...` + `go test ./...` зелёные.**
- [ ] **Step 4: ручной smoke** — `go run ./cmd/nctalk rooms list --json` (с реальными env-кредами — человек).
- [ ] **Step 5: commit** — `feat(main): wire config → client → cli, e2e via httptest`.

**DoD:** e2e-тесты `rooms list` и `chat show` проходят; бинарник собирается (`go build -o /tmp/nctalk ./cmd/nctalk`); пароль не появляется в stderr/stdout ни при каком сценарии.

---

## Этап 6 — Интеграция с боевым сервером (build-tag `integration`)

### Task 6.1: Интеграционные тесты (вне CI)

**Files:**
- Create: `internal/client/integration_test.go` (header `//go:build integration`)
- Create: `docs/integration-run.md` — короткая инструкция запуска (как выставить env, какие команды погонять).

**Interfaces:**
- Consumes: реальные env-креды; `config.Load`, `client.NewTalkClient`.

**Inside:**
- `//go:build integration` — НЕ входит в обычный `go test ./...`.
- Skip если нет `NEXTCLOUD_URL`/`LOGIN`/`PASS`.
- Сценарии (спека §10; читающие — безопасны, мутации — по согласованию):
  - `TestIntegration_ListRooms` — `ListRooms`, `len >= 1`, проверка наличия `actorId` у type=1;
  - `TestIntegration_SearchRooms` — короткий term, проверка entries;
  - `TestIntegration_GetChat` — взять первый token из `ListRooms`, `GetChat` с `Limit=5`;
  - `TestIntegration_SearchMessages` — короткий term, проверка timestamp как int;
  - `TestIntegration_GetReactions` — на сообщении с известными реакциями (если есть; иначе skip);
  - **`TestIntegration_SendMessage`** — опционально, ТОЛЬКО при env `NCTALK_INTEGRATION_SEND=1`, отправка в тестовую комнату и проверка возврата id (мутация, спека §12 «не проверено»).
- Проверить, что redact работает на живом сервере: намеренно подсунуть cross-host redirect в одном из тестов (если есть тестовая конфигурация) — иначе пропустить.

- [ ] **Step 1: написать integration-тесты.**
- [ ] **Step 2: `go test -tags=integration ./internal/client/... -v -run TestIntegration_List`** вручную (запускает человек с кредами).
- [ ] **Step 3: зафиксировать в `docs/integration-run.md` команды.**
- [ ] **Step 4: убедиться что `go test ./...` (без тега) НЕ запускает эти тесты.**
- [ ] **Step 5: commit** — `test(client): integration tests behind build tag (not in CI)`.

**DoD:** интеграционные тесты компилируются только с `-tags=integration`; smoke-прогоны на боевом сервере проходят (отмечено человеком в отчёте); мутационный тест защищён env-флагом.

---

## Риски и альтернативы

1. **Мутации `POST /chat/{token}` и реакции** (спека §12 «не проверено на живом API»). Формат тела берётся из документации Talk; до интеграции (Этап 6) не подтверждён на боевом сервере. **Митигация:** `TestIntegration_SendMessage` под флагом env; если сервер вернёт OCS-error с понятным `message` — корректируем тело запроса точечно. Альтернатива (если формат стабильно расходится с докой) — подсмотреть реальный запрос из talk-desktop через DevTools.

2. **Единая модель `actorId` без `users find`** (спека §8). Узнавать `actorId` по `displayName` предполагается из вывода `rooms list`/`chat show` (там `actorId` есть). Резолв «displayName → actorId» через autocomplete в MVP НЕ делаем. **Риск:** для пользователя, с которым нет личного чата, агент не сможет найти `actorId`. **Митигация:** явно задокументировать в skill-обёртке, что `--from`/`--user` требуют именно `actorId`, и указать источник его получения.

3. **Client-side пагинация для `--from`/`--since`** (cap 200). Серверного фильтра по автору у chat-API нет (спека §8). Если в чате > 200 последних сообщений от одного автора — они могут быть обрезаны. **Митигация:** предупреждение в stderr при достижении cap; рекомендация использовать `search` (серверный фильтр `person=`) для глобального поиска по автору. Альтернатива (отложена) — увеличивать cap явным `--last 1000` по требованию.

4. **Redact-корректность.** Все пути утечки кредов (URL-ошибки, dump request, cross-host redirect, паники) закрываются механизмом в `Task 2.1`. **Риск:** неучтённый путь (например, сторонняя либа, которую подключат позже). **Митигация:** в `Task 2.1` тест redact явно строит URL с userinfo+query и проверяет отсутствие; нет внешних зависимостей → поверхность утечки минимальна. Перед релизом — ручной прогон `nctalk chat show ... 2>&1 | grep -i <password-fragment>` должен дать 0 совпадений.

5. **Путь эндпоинта ListRooms.** Канонический путь зафиксирован в Task 2.1 как `pathRooms = "/ocs/v2.php/apps/spreed/api/v4/room"` (спека §6, §12). Вариант `v1/room` в плане НЕ используется (рассинхрон с Task 5.1 устранён — обе задачи ссылаются на v4 через `pathRooms`). **Митигация:** путь в именованной константе пакета `client` — правка в одном месте; интеграционный тест (Этап 6) подтвердит на боевом сервере.

6. **Локальная TZ и `ParseSince`.** Относительное `--since 2h` и дата-без-зоны интерпретируются в локальной TZ процесса (спека §6). В CI/контейнере TZ может отличаться от ожиданий пользователя. **Митигация:** `Task 3.2` явно использует `time.Local`; в skill-обёртке задокументировать поведение; альтернатива (отложена) — флаг `--tz`.

7. **Ограничение лимитов Anthropic при исполнении.** План исполняется через `limit-aware-subagent-driven-development`; **22 задачи** в 7 этапах (Task 4.5 разбит на 4.5a/4.5b/4.5c — три независимых implementer-цикла). **Митигация:** задачи Этапов 0-2 независимы по пакетам и могут исполняться свежими subagent-ами с минимальным контекстом (каждый видит только свой task-блок + Глобальные ограничения + Interfaces блок соседей); чекпоинты после каждого этапа.

---

## Критерии приёмки MVP

План считается завершённым, когда:

- [ ] Все 7 команд работают на unit-тестах:
  - `rooms list` (с фильтрами `--type`/`--unread`/`--include-former`);
  - `rooms find` (подстрока + `--user actorId`, empty → exit 0);
  - `rooms search` (talk-conversations, term min 1, empty → exit 0);
  - `chat show` (`--last`/`--from`/`--since`/`--system`, cap 200, ранний стоп, подстановка параметров);
  - `chat send` (stdin / `--file`, `--reply-to`/`--silent`/`--reference-id`, возврат id);
  - `reactions get` (детальный вывод, пусто → «реакций нет» exit 0);
  - `search` (talk-message, `--from`→`person`, `--limit`/`--all` cap 5, timestamp int).
- [ ] `go test ./...` зелёный.
- [ ] `go build -o /tmp/nctalk ./cmd/nctalk` собирает рабочий бинарник.
- [ ] `go vet ./...` чистый.
- [ ] **Пароль не утекает** ни в одном сценарии:
  - в выводе любой команды (`stdout`/`stderr`) — нет фрагмента пароля;
  - в сообщениях ошибок (`*url.Error`, OCS-error, сетевые) — URL'ы санитизированы до `scheme://host`;
  - cross-host redirect блокируется (тест в `Task 2.1`);
  - `httputil.DumpRequest*` нигде не вызывается (grep по репо → 0 совпадений).
- [ ] Exit-коды строго по спеке §7: `0` ок / `1` сеть-авторизация / `2` not found (только room-разрешение) / `3` неоднозначно / `0` с пустым выводом для поисковых команд.
- [ ] Единая модель actorId: все флаги «человека» (`--from`, `--user`, `person=`) работают с actorId; нигде не принимают displayName как фильтр.
- [ ] Ручной smoke на боевом сервере (Этап 6) отмечен человеком как успешный для читающих команд (мутация — опционально по env-флагу).

---

## Карта покрытия спеки → задачи

| Спека § | Покрытие |
|---|---|
| §1 Цель/контекст | весь план |
| §3 Архитектура (тонкий клиент, слои) | Этапы 0-5 |
| §4 Структура пакетов | Task 0.1 + все этапы |
| §5 Конфиг и redact | Task 1.1, Task 2.1 |
| §6 `rooms list` | Task 2.2, Task 4.5a |
| §6 `rooms find` | Task 2.3, Task 4.5a |
| §6 `rooms search` | Task 2.4, Task 4.5a |
| §6 `chat show` (фильтры, пагинация, подстановка, форматы времени) | Task 2.5, Task 3.1 (FormatTime), Task 3.2, Task 4.3 |
| §6 `chat send` | Task 2.6, Task 4.4 |
| §6 `reactions get` | Task 2.7, Task 4.5b |
| §6 `search` | Task 2.8, Task 4.5c |
| §7 Правило room + exit-коды | Task 0.2, Task 4.2 |
| §8 Фильтры/модель actorId/timestamp-единицы/min term | Task 2.5, Task 2.8, Task 3.2, Task 4.3, Task 4.5a/b/c |
| §9 Обработка ошибок | Task 2.1 (redact/sanitize), все хендлеры |
| §10 Тестирование | все этапы (unit), Этап 6 (integration) |
| §12 Проверка API (поля, типы, timestamp int/string) | Tasks 2.2/2.5/2.7/2.8 |

---

## Исполнение

План исполняется через `superpowers:limit-aware-subagent-driven-development`:
- **Одна задача = один implementer-цикл** (свежий subagent видит только свой task-блок + раздел «Глобальные ограничения» + Interfaces-блоки соседей-источников).
- **Чекпоинт после каждого этапа** (коммит + `go test ./...` зелёный + ревью критических мест).
- После завершения каждого этапа — выбор: продолжить в этой сессии ИЛИ остановиться до новой сессии (контроль лимитов).
- Очередность строгая: Этап 0 → 1 → 2 (2.1 раньше остальных) → 3 (3.1 можно параллельно с 3.2) → 4 (4.1 раньше остальных) → 5 → 6.
