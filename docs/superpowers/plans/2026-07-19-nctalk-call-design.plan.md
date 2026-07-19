# nctalk-call / nctalk-talk (аудио-звонки WebRTC) — Implementation Plan

> **Для исполнителя:** план исполняется через `superpowers:limit-aware-subagent-driven-development` — одна задача = один implementer-цикл. Каждый шаг с чекбоксом `- [ ]` отдельный. Ссылки вида `§N` указывают на разделы спеки — открывай первоисточник: `docs/superpowers/specs/2026-07-19-nctalk-call-design.md`. Эталон контракта базового CLI: `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md`. Не пересказывай спеку — ей следуй.

**Цель:** добавить в `nctalk` аудио-звонки Nextcloud Talk (Spreed) поверх WebRTC — слушать и говорить из терминала/процесса, в двух режимах: `nctalk-call` (агент/pipe, PCM через stdin/stdout) и `nctalk-talk` (человек/TUI, микрофон/динамик через `ffmpeg`/`sox`).

**Approach:** spike-first (спека §12). Сначала изолированный spike, доказывающий три ключевых риска на боевом сервере (signaling-протокол, framing raw-Opus↔OGG, Opus-codec без CGO). Если spike взлетает — строим агентский модуль, затем TUI. Если нет — `rm -rf` и стоп.

**Architecture:** четыре+один слои. Существующие `config → client → cli → render` не получают ни строки про WebRTC. Добавляются: рефакторинг общего фундамента `internal/transport` (чистый HTTP + OCS-типы `OCSError`/`OCSEnvelope`, переиспользуются `client` и `signaling`/`capability`) и `internal/room` (вынесенный `ResolveRoom` как чистая логика, без `render`) + `internal/exit` (константы exit-кодов); изолированное WebRTC-ядро `internal/call/{signaling,capability,peer,media,agent,interactive}` + две точки входа `cmd/nctalk-call`, `cmd/nctalk-talk`. **DAG-инвариант (спека §4):** `media ← peer` (интерфейсы `AudioSource`/`AudioSink` определены в `call/media`, peer их потребляет — НЕ наоборот). Граница удаления WebRTC — `rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy` (спека §3).

**Tech Stack:** Go 1.21+, stdlib + единственная внешняя зависимость `github.com/pion/webrtc/v4` (только в `internal/call/{peer,media,capability}` и их потребителях). Audio-IO и Opus-кодек — через subprocess `ffmpeg`/`sox` (без CGO).

**Сборка и тесты (CGO_ENABLED=0 обязательно — `CLAUDE.md`, `dyld: missing LC_UUID` на этой машине):**
```sh
CGO_ENABLED=0 go build ./...                       # все бинарники
CGO_ENABLED=0 go build -o nctalk-call ./cmd/nctalk-call
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...                        # unit-тесты (не integration)
CGO_ENABLED=0 go test -tags=integration ./internal/call/...   # боевой сервер
```

**Module path:** `github.com/stas/nctalk` (уже в `go.mod`).

## Глобальные ограничения (действуют на каждую задачу)

- Go 1.21+; **`CGO_ENABLED=0`** на всех сборках/тестах; stdlib + `github.com/pion/webrtc/v4` — единственная разрешённая не-stdlib зависимость.
- Креды — **только из env** (`NEXTCLOUD_URL`/`NEXTCLOUD_LOGIN`/`NEXTCLOUD_PASS`/`NEXTCLOUD_TIMEOUT`), никогда в argv/выводе/логах. Пароль — только в заголовке `Authorization`.
- Все ошибки пропускаются через `sanitizeErr` (без userinfo/query). `http.Client.CheckRedirect` — same-host only, **https→http даунгрейд заблокирован**. Запрет `httputil.DumpRequestOut`/`DumpResponse`.
- Все HTTP-запросы с `context.Context` → Ctrl-C отменяет и signaling-poll, и peer-установку.
- Инвариант «0 строк про WebRTC в базовом `cmd/nctalk`»: `pion` не должен транзитивно попасть в `cmd/nctalk` — следим, чтобы `internal/client`/`internal/cli`/`internal/render`/`internal/config`/`internal/transport`/`internal/room` **не импортировали** `internal/call/*`.
- Кириллические комментарии и тексты вывода — сохраняются (как в спеке и существующем коде).
- Коммиты **без AI-атрибуции** (никакого `Co-Authored-By: Claude`).
- Signaling-эндпоинт — строго `/ocs/v2.php/apps/spreed/api/v4/signaling/{token}` (версия **v4**, та же что у Call API; не `v1`/без версии — спека §7).
- ICE-кандидаты содержат локальные/публичные IP — не пишем их в логи без явного `NCTALK_DEBUG_ICE=1`.

---

## Риски и точки решения (спека §1)

| # | Риск | Где проверяется | Mitigation | Точка решения «продолжать / выпилить» |
|---|---|---|---|---|
| R1 | Signaling-протокол Talk недокументирован (читается по JS-коду `spreed`) | Этап 2 (Spike), Task 2.3 — фикстуры реальных signaling-сообщений с боевого | Если за ~2 дня не удаётся собрать валидный `offer`/`answer`/`candidate`-обмен с браузером — выкидываем весь WebRTC-блок | End of Этап 2 (Spike): либо аудио идёт в обе стороны с браузером, либо `rm -rf` |
| R2 | Framing raw Opus-пакетов ↔ OGG-контейнер между ffmpeg и pion | Этап 2 (Spike), Task 2.5 — OGG-парсер (с OpusHead/comment) + round-trip PCM↔Opus через ffmpeg | (a1) pure-Go OGG-парсер (с корректными OpusHead BOS + vorbis-comment — иначе ffmpeg-decode падает «Invalid OpusHead»); fallback (a3) — RTP через `ffmpeg -f rtp` + `TrackLocalStaticRTP`; дальнейший fallback — (b) CGO libopus (нарушает CGO=0) | End of Task 2.5: round-trip синусоиды 440 Гц длительностью 2с детектируется на выходе (Goertzel/DFT) с SNR>10 дБ |
| R3 | Opus-кодек отсутствует в `pion` | Локализовано в `call/media` (codec-стратегия) | ffmpeg-subprocess — единственный вариант под CGO=0; смена стратегии затрагивает только `media`, не ядро | Не отдельная точка: снимается выполнением R2 |

**Дополнительные риски выполнения по этапам** — указаны в шапке каждого этапа.

---

## Этап 0 — Скелет и изоляция

**Цель этапа:** подготовить структуру каталогов и убедиться, что добавление `pion` в `go.mod` физически не попадает в `cmd/nctalk`. Точка выпиливания: если на этом этапе выявится, что изоляция через `internal/`+`cmd/` не работает — стоп, перепроектирование.

### Task 0.1: Создать скелет пакетов WebRTC-ядра (пустые `doc.go`)

**Files:**
- Create: `internal/call/doc.go`
- Create: `internal/call/signaling/doc.go`
- Create: `internal/call/capability/doc.go`
- Create: `internal/call/peer/doc.go`
- Create: `internal/call/media/doc.go`
- Create: `internal/call/media/ogg/doc.go`
- Create: `internal/call/agent/doc.go`
- Create: `internal/call/interactive/doc.go`
- Create: `cmd/nctalk-call/main.go` (заглушка `func main() { fmt.Fprintln(os.Stderr, "nctalk-call: not implemented"); os.Exit(1) }`)
- Create: `cmd/nctalk-talk/main.go` (то же)

**Interfaces:**
- Produces: пустые пакеты под WebRTC-ядро; две тонкие точки входа, не импортирующие ничего из `internal/call/`.

**Inside:**
- В каждом `doc.go` — однострочный пакетный комментарий на кириллице. Пример для `internal/call/signaling/doc.go`:
  ```go
  // Package signaling реализует OCS-polling signaling-клиент Nextcloud Talk
  // (long-poll /signaling/{token}, разбор usersInRoom/offer/answer/candidate).
  // Спека 2026-07-19 §7.
  package signaling
  ```
- `cmd/nctalk-call/main.go` и `cmd/nctalk-talk/main.go` — буквально `package main; func main() { ... }` без вызовов в `internal/`.

- [ ] **Step 1: создать директории и `doc.go`** — 8 файлов с пакетными комментариями (`signaling`, `capability`, `peer`, `media`, `media/ogg`, `agent`, `interactive` + родительский `call`).
- [ ] **Step 2: создать заглушки `cmd/nctalk-call/main.go`, `cmd/nctalk-talk/main.go`.**
- [ ] **Step 3: сборка** — `CGO_ENABLED=0 go build ./...` → 0 ошибок.
- [ ] **Step 4: commit** — `chore(call): scaffold WebRTC packages + cmd stubs`.

**DoD:** структура каталогов совпадает со спекой §4; сборка зелёная; `cmd/nctalk/main.go` нетронут.

---

### Task 0.2: Изоляция WebRTC-ядра — статический контроль через `go list`

**Files:**
- Create: `internal/call/issolation_test.go` (или `buildtag_check_test.go`)
- Modify: (нет)

**Interfaces:**
- Produces: тест, падающий, если любой из базовых пакетов (`internal/client`, `internal/cli`, `internal/render`, `internal/config`, `internal/transport`, `internal/room`) начнёт импортировать `internal/call/*`.

**Inside:**
- Тест строит граф импортов через `go list -deps -json ./cmd/nctalk` и проверяет, что `github.com/pion/webrtc/v4` **не входит** в дерево зависимостей базового бинарника. Это регрессионная защита инварианта спеки §3.

```go
// internal/call/isolation_test.go
package call_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestCmdNctalkDoesNotDependOnPion — инвариант спеки §3: базовый бинарник
// cmd/nctalk НЕ должен транзитивно тащить github.com/pion/webrtc. Если тест
// падает — какой-то из фундаментальных пакетов (client/cli/render/transport/room)
// случайно начал импортировать internal/call/*.
func TestCmdNctalkDoesNotDependOnPion(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/nctalk")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps ./cmd/nctalk: %v", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		// Точный префикс строки импорта (с пробелом после пути модуля) — чтобы
		// избежать false-positive на сторонние модули вроде pion/webrtc-extras
		// или pion/mediadevices. `go list -deps` печатает полный путь импорта,
		// поэтому проверяем префикс "github.com/pion/webrtc/v4 " (с пробелом,
		// отделяющим путь от версии в выводе `go list -deps`).
		if strings.HasPrefix(line, "github.com/pion/webrtc/v4 ") || line == "github.com/pion/webrtc/v4" {
			t.Fatalf("cmd/nctalk косвенно зависит от %s — нарушен инвариант изоляции (спека §3)", line)
		}
	}
}
```

- [ ] **Step 1: написать тест.** Запустить `CGO_ENABLED=0 go test ./internal/call/...` — должен проходить (т.к. pion ещё не добавлен).
- [ ] **Step 2: commit** — `test(call): static isolation guard — no pion in cmd/nctalk`.

**DoD:** тест зелёный; регрессионная защита добавлена.

---

### Task 0.3: Добавить `github.com/pion/webrtc/v4` в `go.mod`

**Files:**
- Modify: `go.mod`
- Modify: `go.sum` (генерируется `go mod tidy`)
- Modify: `internal/call/peer/doc.go` — добавить `_ "github.com/pion/webrtc/v4"` import, чтобы зависимость реально использовалась и линковалась.

**Interfaces:**
- Produces: `go.mod` с зависимостью `github.com/pion/webrtc/v4` (и транзитивными).

**Inside:**
- `go get github.com/pion/webrtc/v4@v4.0.47` — **зафиксированная минорная версия** (vendoring для воспроизводимости spike-критерия: `@latest` недетерминирован и ломает сравнение результатов spike'а при повторах). На момент написания план фиксирует `v4.0.47` как актуальный стабильный релиз `v4.0.x`; исполнитель должен **проверить** последний стабильный тег на https://github.com/pion/webrtc/releases и обновить константу в `go.mod` до ближайшего свежего `v4.0.x` (минор держим стабильный, не `v4.1.x` и не `@latest`).
- В `peer/doc.go` добавить blank-import для проверки, что pion реально компилируется под `CGO_ENABLED=0`.
- **Проверить инвариант:** `CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion` → должен оставаться зелёным (pion не должен просочиться в `cmd/nctalk`).

- [ ] **Step 1: `go get github.com/pion/webrtc/v4@v4.0.47`** (или актуальный стабильный `v4.0.x` — см. выше) + blank-import в `peer/doc.go`.
- [ ] **Step 2: `CGO_ENABLED=0 go build ./...`** → 0 ошибок.
- [ ] **Step 3: `CGO_ENABLED=0 go test ./internal/call/...`** → isolation-тест зелёный.
- [ ] **Step 4: `CGO_ENABLED=0 go build -o /tmp/nctalk ./cmd/nctalk && ls -la /tmp/nctalk`** — базовый бинарник собран, размер не должен существенно измениться.
- [ ] **Step 5: commit** — `chore(call): add github.com/pion/webrtc/v4 dependency`.

**DoD:** `go.mod` содержит `pion/webrtc/v4`; `cmd/nctalk` собирается и не зависит от pion (по isolation-тесту).

---

## Этап 1 — Рефакторинг фундамента: `internal/transport` + `internal/room`

**Цель этапа:** вынести общие примитивы, нужные WebRTC-слою, в отдельные пакеты так, чтобы базовое поведение `nctalk` не изменилось. Рефакторинг **механический**, без изменения контракта. **Не входит в границу удаления WebRTC** — даже после отката звонков эти пакеты остаются в дереве (спека §3/§4).

**Риск этапа:** сломать существующие тесты `TestRun_PasswordDoesNotLeak_CrossHostRedirect` или контракты базовых команд. Mitigation: тесты переезжают вместе с кодом; все существующие тесты `cmd/nctalk`, `internal/cli`, `internal/client` должны оставаться зелёными без правок.

### Task 1.1: Вынести чистый HTTP-транспорт + OCS-типы в `internal/transport`

**Files:**
- Create: `internal/transport/transport.go`
- Create: `internal/transport/transport_test.go`
- Create: `internal/transport/ocs.go` — переезд `OCSError`/`OCSEnvelope` из `internal/client/ocs.go` (см. ниже обоснование).
- Modify: `internal/client/client.go` — удалить `httpDoer`, `sameHostRedirectPolicy`, сборку Basic-auth+OCS-заголовков; `TalkClient.doOCS` делегирует в `transport.DoOCS`; восстановить явную панику на невалидном BaseURL.
- Modify: `internal/client/redact.go` — `SanitizeURL`/`sanitizeErr` переезжают в `internal/transport` (реэкспорт `sanitizeErr` через thin wrapper в client для backcompat существующих вызовов внутри пакета, либо прямая замена `sanitizeErr`→`transport.SanitizeErr` по всем файлам `internal/client/*.go`).
- Delete: `internal/client/ocs.go` — типы переезжают в `internal/transport/ocs.go`. В `internal/client` остаются только thin type-aliases (`type OCSError = transport.OCSError`, `type OCSEnvelope[T any] = transport.OCSEnvelope[T]`) в новом файле `internal/client/aliases.go` для backcompat существующих тестов/кода внутри пакета, **либо** все ссылки обновляются на `transport.OCSError`/`transport.OCSEnvelope`. Выбор за реализацией; aliases короче (не трогают тесты `client/ocs_test.go`), прямые ссылки — чище. **Рекомендуются aliases** (минимум правок в существующих тестах `client`).
- Modify: `internal/cli/exit.go` — `exitFromClientErr` использует `errors.As(err, &transport.OCSError{})` вместо `*client.OCSError`. Различение `Code:404 → exit 2` сохраняется (тип тот же, только пакет-владелец сменился).

**Interfaces:**
- Consumes: `config.Config` (через интерфейс или явные поля — см. ниже).
- Produces:
  ```go
  // Package transport — чистый HTTP-транспорт поверх OCS-конверта.
  // Не знает про WebRTC и про специфику ресурсов Spreed (rooms/chat/call/signaling) —
  // только HTTP, OCS-конверт и OCS-ошибки. Спека 2026-07-19 §3/§4/§5.
  package transport

  // Doer — минимальный HTTP-интерфейс (переименован, чтобы не коллидировать
  // с прежним client.httpDoer). *http.Client реализует его; моки — в тестах.
  type Doer interface {
      Do(*http.Request) (*http.Response, error)
  }

  // Auth — креды для Basic-auth + распарсенный BaseURL. Через структуру (а не
  // config.Config напрямую), чтобы transport не зависел от internal/config —
  // это позволяет использовать его из любого потребителя. Timeout сюда НЕ входит:
  // таймаут выставляется на http.Client (см. NewTalkClient), а не на запрос.
  type Auth struct {
      BaseURL  *url.URL // распарсенный один раз; nil невалиден (см. panic ниже)
      Login    string
      Password string
  }

  // SameHostRedirectPolicy — переезд client.sameHostRedirectPolicy без изменений
  // поведения (блокировка cross-host auth-leak и https→http downgrade).
  // Экспортируемая, т.к. её размещают в http.Client.CheckRedirect оба потребителя
  // (internal/client и internal/call/signaling).
  func SameHostRedirectPolicy(req *http.Request, via []*http.Request) error

  // DoOCS — транспортный метод, бывший client.TalkClient.doOCS. Контракт
  // идентичен: собирает URL, Basic-auth+OCS-заголовки, разворот конверта
  // (OCSEnvelope), возврат заголовков ответа, типизированная *OCSError при
  // meta.statusCode>=400. Спека 2026-07-19 §3.
  func DoOCS(ctx context.Context, doer Doer, auth Auth, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error)

  // SanitizeURL/SanitizeErr — переезд из client.redact.go без изменений.
  func SanitizeURL(s string) string
  func SanitizeErr(err error) error

  // OCSError — переезд из client.ocs.go. Тип идентичен (поля Code+Message,
  // метод Error, поддержка errors.As через сравнение указателя). Доменно это
  // ОШИБКА OCS-конверта — принадлежность транспорту корректна: конверт сам
  // по себе доменно-нейтрален (OCS — соглашение Nextcloud об ошибках на уровне
  // HTTP-API). Спека 2026-07-19 §3.
  type OCSError struct { Code int; Message string }
  // OCSEnvelope[T] — переезд из client.ocs.go (generics, формат ocs.meta+data).
  type OCSEnvelope[T any] struct { ... }
  ```

**Решение по циклическому импорту (review замечание 1):** типы `OCSError`/`OCSEnvelope` переезжают ВМЕСТЕ с транспортом — в `internal/transport`. Тогда граф зависимостей однонаправленный: `internal/client` → `internal/transport` (client использует `transport.DoOCS` и работает с `transport.OCSError`/`OCSEnvelope`), `internal/cli` → и `client`, и `transport` (для `errors.As(err, &transport.OCSError{})`). **Обратной дуги `transport → client` нет**, цикла не возникает. Альтернатива «отдельный `internal/ocs` пакет только для типов» отклонена как лишнее дробление: OCS-типы и транспорт живут в общем модуле концептуально (OCS-конверт — свойство HTTP-API), а отдельный ultra-thin пакет дал бы +1 импорт без выгоды. Зафиксировано: choice = **`OCSError`/`OCSEnvelope` в `internal/transport`**.

**Inside:**
- Перенести `httpDoer`→`Doer` (переименование), `sameHostRedirectPolicy`→`SameHostRedirectPolicy`, `sanitizeErr`→`SanitizeErr` (экспортируем — нужно в `call/signaling`), `SanitizeURL`, `OCSError`, `OCSEnvelope` — механически, без правок логики.
- Перенести тело `doOCS` как функцию `DoOCS(ctx, doer, auth, ...)`; внутри используется `OCSEnvelope[json.RawMessage]` и возврат `*OCSError{Code, Message}` на `meta.statusCode>=400` и на HTTP 404 (контракт базового клиента сохранён).
- `TalkClient` в `internal/client`:
  ```go
  type TalkClient struct {
      cfg     config.Config
      doer    transport.Doer
      auth    transport.Auth
  }
  func NewTalkClient(cfg config.Config) *TalkClient {
      hc := &http.Client{
          Transport:     http.DefaultTransport,
          Timeout:       cfg.Timeout,       // таймаут висит на http.Client, не на transport.Auth
          CheckRedirect: transport.SameHostRedirectPolicy,
      }
      return NewTalkClientWithDoer(cfg, hc)
  }
  func NewTalkClientWithDoer(cfg config.Config, doer transport.Doer) *TalkClient {
      // config.Load уже валидирует URL, поэтому ошибка парсинга здесь — баг
      // вызывающего. Паника (как в существующем коде) — намеренная: НЕ игнорируем
      // err от url.Parse молча (review замечание 8), падаем громко с контекстом.
      u, err := url.Parse(cfg.BaseURL)
      if err != nil {
          panic(fmt.Sprintf("client: невалидный BaseURL после config.Load: %v", err))
      }
      return &TalkClient{
          cfg:  cfg,
          doer: doer,
          auth: transport.Auth{BaseURL: u, Login: cfg.Login, Password: cfg.Password},
      }
  }
  func (c *TalkClient) doOCS(ctx context.Context, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error) {
      return transport.DoOCS(ctx, c.doer, c.auth, method, p, query, body, mutate, out)
  }
  ```
- **Регресс-защита 1:** тест `TestRun_PasswordDoesNotLeak_CrossHostRedirect` (в `cmd/nctalk/main_test.go`, с `NEXTCLOUD_PASS=SECRET_MARKER`) должен оставаться зелёным **без правок теста**. Тесты redirect-политики переезжают в `internal/transport/transport_test.go`.
- **Регресс-защита 2:** тест `TestDoOCS_ReturnsTypedOCSError` (в `internal/client/ocs_test.go`) — проверяет, что `errors.As(err, &OE)` извлекает `*OCSError{Code:404}`. С переездом типа в `transport` тест работает через alias `client.OCSError = transport.OCSError` **без правок**, либо обновляется на прямую ссылку `transport.OCSError` — оба варианта сохраняют семантику «`Code:404 → exit 2` через `cli.exitFromClientErr`» (проверяется отдельным end-to-end тестом).
- **Регресс-защита 3:** `cli.exitFromClientErr` (`internal/cli/exit.go`) обновляется: `var ocsErr *client.OCSError` → `var ocsErr *transport.OCSError`; `errors.As` продолжает работать (тип идентичен, сменился только пакет-владелец). Контракт `Code == http.StatusNotFound → ExitNotFound (2)` сохранён.

- [ ] **Step 1: написать `internal/transport/transport_test.go`** — тесты для `SameHostRedirectPolicy` (same-host follows, cross-host blocked, https→http blocked) и `SanitizeURL`/`SanitizeErr`. Источник: тесты `TestSanitizeErr_URLRedacted` и сопутствующие из `internal/client/client_test.go` (NB: в дереве НЕТ файла `internal/client/redact_test.go` — тесты sanitize лежат в `client_test.go`; не ссылаться на несуществующий файл).
- [ ] **Step 2: проверить, что новые тесты падают (пакет пустой).**
- [ ] **Step 3: реализовать `internal/transport/transport.go` + `internal/transport/ocs.go`** — перенос кода из `client.go` + `redact.go` + `ocs.go` (типы).
- [ ] **Step 4: `CGO_ENABLED=0 go test ./internal/transport/...`** — зелёный.
- [ ] **Step 5: правки `internal/client/`** — удалить дублированный код, делегировать в `transport`, оставить aliases (`type OCSError = transport.OCSError` и т.п.) в новом `internal/client/aliases.go` (или обновить ссылки напрямую). Восстановить панику на невалидном BaseURL в `NewTalkClientWithDoer`.
- [ ] **Step 6: правки `internal/cli/exit.go`** — `exitFromClientErr` использует `*transport.OCSError` (или продолжает работать через alias `*client.OCSError` — это alias того же типа, errors.As работает в обоих случаях). Добавить/сохранить регресс-тест: `errors.As(err, &transport.OCSError{})` различает `Code:404 → exit 2`.
- [ ] **Step 7: `CGO_ENABLED=0 go test ./...`** — **все** существующие тесты зелёные, включая `TestRun_PasswordDoesNotLeak_CrossHostRedirect`, `TestDoOCS_ReturnsTypedOCSError`, `TestSanitizeErr_URLRedacted`.
- [ ] **Step 8: `CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion`** — изоляция сохранена (Task 1.1 НЕ зависит от Task 0.3, см. DAG).
- [ ] **Step 9: commit** — `refactor(transport): extract HTTP/OCS primitives + OCSError/OCSEnvelope to internal/transport`.

**DoD:** поведение `cmd/nctalk` не изменилось (все существующие тесты зелёные, включая `TestRun_PasswordDoesNotLeak_CrossHostRedirect` и `TestDoOCS_ReturnsTypedOCSError`); `transport` переиспользуется без дублирования; `call/signaling` сможет импортировать `transport.DoOCS` и `transport.OCSError` без циклической зависимости; паника на невалидном BaseURL восстановлена; dead-code в `transport.Auth` (Timeout) отсутствует.

---

### Task 1.2: Вынести `ResolveRoom` в `internal/room` (чистая логика, БЕЗ `render`)

**Files:**
- Create: `internal/room/room.go`
- Create: `internal/room/room_test.go`
- Modify: `internal/cli/room.go` — оставить тонкую обёртку, делегирующую в `internal/room`, **или** заменить все вызовы `cli.ResolveRoom` на `room.ResolveRoom` (предпочтительно — удалить дублирование). Печать кандидатов (`render.Candidates`) остаётся в **вызывающем коде** (cli-роутере), НЕ в `room.ResolveRoom`.
- Modify: `internal/cli/handlers_chat.go`, `internal/cli/handlers_reactions.go` — обновить импорты, если `ResolveRoom` переехал полностью. Если `room.ResolveRoom` возвращает список кандидатов — handler/роутер печатает их через `render.Candidates`.

**Interfaces:**
- Consumes: интерфейс `TalkClient` из `internal/cli` либо новый минимальный интерфейс в `internal/room` (см. ниже).
- Produces:
  ```go
  // Package room реализует разрешение <room>/--name → token для команд
  // базового CLI и для cmd/nctalk-call/cmd/nctalk-talk. Контракт exit-кодов
  // (1/2/3) — спека 2026-07-17 §7 и 2026-07-19 §6.
  //
  // ВАЖНО: пакет room НЕ импортирует internal/render — это чистая логика
  // разрешения. Форматирование вывода (печать списка кандидатов в stderr при
  // неоднозначности) делает вызывающий код — cmd/nctalk через render.Candidates,
  // cmd/nctalk-call/cmd/nctalk-talk своим способом. Без этого инвариант спеки
  // §4 «cmd/nctalk-call без cli-роутера и render» нарушался бы (room→render
  // затянул бы render в бинарник агента).
  package room

  // RoomLister — минимальная зависимость от клиента: нужен только FindRooms.
  // Интерфейс здесь (а не cli.TalkClient), чтобы internal/room не зависел
  // от internal/cli (иначе cmd/nctalk-call утянет cli-роутер и render).
  // Возвращает []client.Room (тип из internal/client — это доменный тип, не
  // cli-специфичный; cmd/nctalk-call и так импортирует client для других целей).
  type RoomLister interface {
      FindRooms(ctx context.Context, query, actorId string) ([]client.Room, error)
  }

  // Result — результат разрешения. Status определяет ветку:
  //   - StatusResolved   : Token валиден, Candidates пуст.
  //   - StatusAmbiguous  : Token пуст, Candidates содержит >1 совпадения (печать
  //                       списка — ответственность вызывающего кода).
  //   - StatusNotFound   : 0 совпадений по --name.
  //   - StatusEmptyInput : не задан ни positional, ни --name.
  // Сетевая/OCS-ошибка возвращается через err (ExitError{ExitGeneric} или с
  // сохранённым Code через errors.As → *transport.OCSError → exit 2 для 404).
  type Status int
  const (
      StatusResolved Status = iota
      StatusAmbiguous
      StatusNotFound
      StatusEmptyInput
  )

  type Result struct {
      Status     Status
      Token      string         // для StatusResolved
      Candidates []client.Room  // для StatusAmbiguous (для печати вызывающим)
      Query      string         // исходное --name (для диагностик вызывающего)
  }

  // ResolveRoom — чистая функция разрешения. НЕ пишет в stderr, НЕ печатает
  // список кандидатов. Возвращает Result; Formatting/вывод — на вызывающем.
  func ResolveRoom(ctx context.Context, client RoomLister, positional, nameFlag string) (Result, error)
  ```

**Inside:**
- Скопировать логику `ResolveRoom` из `internal/cli/room.go` в `internal/room/room.go`; тип параметра `TalkClient` заменить на `RoomLister`; **убрать вызов `render.Candidates`** — вместо записи в stderr вернуть `Result{Status: StatusAmbiguous, Candidates: rooms, Query: nameFlag}`.
- **Решение по `ExitError`:** либо (а) вынести `ExitError`/`Exit*` константы в отдельный пакет `internal/exit` (и `cli`, и `room`, и `cmd/nctalk-call` его импортируют), либо (б) дублировать константы в `room`. **Рекомендуется (а)** — это раз и навсегда решает проблему с переиспользованием exit-кодов и не раздувает `cli`.
- Если идём путём (а): создать `internal/exit/exit.go` (`ExitOK=0`, `ExitGeneric=1`, `ExitNotFound=2`, `ExitAmbiguous=3`, тип `ExitError`, хелпер `Exit`), перенести использование в `cli/exit.go` в thin re-export (или заменить полностью + поправить импорты в handlers).
- Тесты `cli/room_test.go` переносятся в `room/room_test.go` **с правками**: вместо «прочитали stderr, там список кандидатов» → `Result.Status == StatusAmbiguous && len(Result.Candidates) == N` (тест становится чище — проверяет данные, а не текстовый вывод).
- `cli/room.go` **обязательно остаётся** как точка печати: тонкая обёртка `cli.ResolveRoom(ctx, client, pos, name, stderr) (string, error)` зовёт `room.ResolveRoom`, маппит `Result.Status` в exit-код и **печатает `Candidates` через `render.Candidates(stderr, result.Candidates)` при `StatusAmbiguous`**. Это сохраняет контракт базовых команд `nctalk` без правок их callers (`handlers_chat.go`, `handlers_reactions.go` продолжают звать `cli.ResolveRoom` как прежде).
- `cmd/nctalk-call`/`cmd/nctalk-talk` зовут `room.ResolveRoom` напрямую, маппят Status в exit-код, и печатают candidates своим способом (минимально — построчный список в stderr, либо через тот же `render.Candidates` если он не зависит от cli).
- **Маппинг ошибок FindRooms:** `room.ResolveRoom` возвращает `err` от `FindRooms` как есть (raw, без обёртки). Вышестоящий код (cli.ResolveRoom или cmd/nctalk-call) зовёт `exitFromClientErr(err)` (или её relocated-версию в `internal/exit`, см. ниже) для маппинга `*transport.OCSError{Code:404}` → `exit 2`, прочее → `exit 1`. **Рекомендация:** перенести `exitFromClientErr` из `cli/exit.go` в `internal/exit/exit.go` (вместе с константами и `ExitError`) — тогда и `cli`, и `room`, и `cmd/nctalk-call`/`cmd/nctalk-talk` используют общий хелпер без дубляжа. Backcompat в `cli/exit.go` — через алиас `type ExitError = exit.ExitError` + thin wrapper `func exitFromClientErr(err error) exit.ExitError { return exit.FromClientErr(err) }` (или прямое обновление всех вызовов на `exit.FromClientErr`).

- [ ] **Step 1: создать `internal/exit/exit.go`** — константы, `ExitError`, хелпер `Exit` **и `FromClientErr(err) error`** (перенос `exitFromClientErr` из `cli/exit.go` в exported-форме; работает с `*transport.OCSError` post-Task 1.1).
- [ ] **Step 2: правки `cli/exit.go`** — реэкспорт через `type ExitError = exit.ExitError` (алиас) для backcompat, чтобы не править все handler-ы; `exitFromClientErr` становится thin wrapper `return exit.FromClientErr(err)` (или прямой вызов `exit.FromClientErr` из handler-ов — выбор за реализацией).
- [ ] **Step 3: `CGO_ENABLED=0 go test ./internal/cli/...`** — существующие тесты exit зелёные.
- [ ] **Step 4: написать `internal/room/room_test.go`** — перенос тестов из `cli/room_test.go` с правкой ассертов на `Result.Status`/`Result.Candidates` (вместо чтения stderr).
- [ ] **Step 5: реализовать `internal/room/room.go`** — перенос `ResolveRoom` с `RoomLister`-интерфейсом и возвратом `Result` (без render, без печати; FindRooms-ошибка возвращается как raw err, без маппинга в ExitError — это задача вызывающего через `exit.FromClientErr`).
- [ ] **Step 6: правки `cli/room.go`** — `cli.ResolveRoom` становится тонкой обёрткой: зовёт `room.ResolveRoom`, маппит `Status` → exit-код (или `err` → `exit.FromClientErr`), печатает `Candidates` через `render.Candidates` при `StatusAmbiguous`. Контракт существующих callers сохранён.
- [ ] **Step 7: `CGO_ENABLED=0 go test ./...`** — все тесты зелёные, включая `cli/handlers_chat_test.go`/`handlers_reactions_test.go` (они проверяют текстовый вывод кандидатов — он должен сохраниться через обёртку).
- [ ] **Step 8: commit** — `refactor(room): extract ResolveRoom as pure logic (no render) to internal/room (+ internal/exit w/ FromClientErr)`.

**DoD:** `cmd/nctalk-call` сможет вызывать `room.ResolveRoom` БЕЗ подтягивания `cli`/`render` (проверка через `go list -deps ./cmd/nctalk-call | grep render` пустая); базовые команды `nctalk` работают идентично (тесты `handlers_*_test.go` зелёные — текстовый вывод кандидатов сохранён через обёртку `cli.ResolveRoom` → `render.Candidates`).

---

## Этап 2 — Spike (главная точка решения)

**Цель этапа:** доказать на боевом сервере, что аудио идёт между `nctalk-call` и звонком в браузере. **Только этот этап проверяет три главных риска** (R1 signaling, R2 framing, R3 Opus-codec). Spike ведётся в отдельной ветке — если не взлетает, ветка выкидывается.

**Критерий успеха spike'а (спека §12):**
1. `nctalk-call <room>` на боевом сервере (с браузером собеседника в той же комнате) успешно join'ит звонок (Call API + signaling).
2. PCM-файл, поданный на stdin, преобразуется в Opus, доставляется через WebRTC, и **собеседник слышит звук** в браузере.
3. Голос собеседника из браузера доходит обратно: `OnTrack` → Opus → PCM → stdout, и записанный PCM воспроизводится как узнаваемый звук.
4. **Объективный PASS-критерий (review замечание 6, главный):** round-trip синусоиды 440 Гц длительностью ≥3с через `nctalk-call ↔ браузер` — в записанном PCM детектируется пик на ~440 Гц (алгоритм Гёрцеля или простой DFT, pure-Go без новых зависимостей) с SNR>10 дБ и длительностью детектируемого тона >2с. Реализовано в `spike-check` утилите (Task 2.9).
5. Round-trip PCM→Opus→PCM через ffmpeg+OGG-парсер работает с корректными OpusHead/comment заголовками (Task 2.5 — без них ffmpeg-decode падает).
6. Ручное прослушивание — дополнительно к п.4, не единственный критерий.

**Точка выпиливания spike'а:** если хотя бы один из R1/R2 не получается в течение отведённого времени (ориентир: 2–3 рабочих дня) — `rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk && go mod tidy`, вывод «WebRTC-аудио к Talk в Go не окупается», **стоп**.

**Риск этапа:** для разбора signaling-протокола нужен браузерный JS-код `nextcloud/spreed` — исполнитель должен уметь читать JS и собирать фикс-трафик с боевого (включая DevTools браузера). Mitigation: параллельно с реализацией — сбор обезличенных signalling-фикстур (`testdata/signaling/*.json`) для unit-тестов.

### Task 2.1: Собрать фикстуры signaling-трафика с боевого (ручной шаг, описан)

**Files:**
- Create: `testdata/signaling/usersInRoom.json`
- Create: `testdata/signaling/offer.json`
- Create: `testdata/signaling/answer.json`
- Create: `testdata/signaling/candidate.json`
- Create: `testdata/signaling/call_participants.json` (из `GET /call/{token}`)
- Create: `testdata/signaling/capability.json` (STUN/TURN config из capability сервера)

**Interfaces:**
- Produces: обезличенные (без реальных URL/login/sessionId) JSON-фикстуры реального signaling-обмена.

**Inside:**
- На боевом сервере открыть звонок в браузере (DevTools → Network → XHR-фильтр на `signaling`).
- Сохранить:
  - `GET /ocs/v2.php/apps/spreed/api/v4/signaling/{token}` — ответ с `usersInRoom`/`participants`;
  - `POST` на тот же эндпоинт с телом `{"type":"offer","payload":{...}}` / `answer` / `candidate` / `usersInRoom`-sync;
  - `GET /ocs/.../capability` (или `/ocs/v2.php/cloud/capabilities`) — секция spreed со STUN/TURN-серверами.
- **Обезличивание:** sessionId, actorId, public IP в ICE-candidates, displayName — заменить на синтетические `"SESSION_1"`, `"alice"`, `"203.0.113.10"` (RFC 5737 TEST-NET-3) и т.п. Оригиналы — удалить.

- [ ] **Step 1: сделать дамп signaling'а через DevTools.** Сохранить как есть.
- [ ] **Step 2: обезличить** — заменить sessionId/actorId/IP на синтетику.
- [ ] **Step 3: сложить в `testdata/signaling/`.**
- [ ] **Step 4: commit** — `test(signaling): capture anonymized signaling fixtures from real server`.

**DoD:** фикстуры достаточны для написания парсера и unit-тестов; никакого PII/кредов в репозитории нет (проверка `grep -E 'password|secret|Authorization' testdata/` пустая).

---

### Task 2.2: signaling-клиент — `internal/call/signaling` (polling + разбор)

**Files:**
- Create: `internal/call/signaling/signaling.go`
- Create: `internal/call/signaling/types.go`
- Create: `internal/call/signaling/signaling_test.go`

**Interfaces:**
- Consumes: `internal/transport.Doer` (через `transport.DoOCS`), `internal/config.Config` (через `transport.Auth`).
- Produces:
  ```go
  // Package signaling — OCS-polling signaling-клиент для P2P (без HPB).
  // Спека 2026-07-19 §7. Long-poll /signaling/{token}, разбор usersInRoom и
  // offer/answer/candidate; отправка своих сообщений — POST на тот же эндпоинт.
  package signaling

  type Auth = transport.Auth   // alias

  // Event — сигнал из signaling-loop, доставляемый подписчику (peer-слою).
  type Event struct {
      Kind EventKind
      // UsersUpdated — обновился список участников (usersInRoom); PID/SessionId/flags.
      // Offer / Answer / Candidate — SDP/ICE от удалённого пира.
      // Error — фатальная ошибка (401/403/404), loop завершён.
      Users     []User
      From      string       // sessionId/actorId отправителя (для offer/answer/candidate)
      SDP       string       // для Offer/Answer
      Candidate ICECandidate // для Candidate
      Err       error        // для Error
  }
  type EventKind int
  const (
      EvUsersUpdated EventKind = iota
      EvOffer
      EvAnswer
      EvCandidate
      EvError
  )

  type User struct {
      SessionId string
      ActorId   string
      ActorType string
      InCall    int  // битмаск flags (IN_CALL / WITH_AUDIO / ...)
  }
  type ICECandidate struct {
      Candidate     string
      SDPMLineIndex *int
      SDPMid        *string
  }

  // Client — signaling-клиент. Не знает про WebRTC-пир и аудио.
  type Client struct { /* unexported: doer, auth, token, httpClient */ }
  func New(auth Auth, doer transport.Doer) *Client

  // JoinCall — POST /call/{token} с flags (1=recvonly, 3=sendrecv — спека §6).
  func (c *Client) JoinCall(ctx context.Context, token string, flags int) error
  // LeaveCall — DELETE /call/{token} (no all= для обычного участника).
  func (c *Client) LeaveCall(ctx context.Context, token string) error

  // PollLoop — long-poll /signaling/{token}. Каждое событие — в ch. Выходит
  // при ctx.Done() (штатный leave) или фатальной 401/403/404 (EvError).
  // Стратегия retry/backoff — спека §7: 2xx/304 → немедленный повтор;
  // 5xx/timeout → backoff (1с..30с); 401/403 → exit 1 без retry; 404 → exit 2.
  func (c *Client) PollLoop(ctx context.Context, token string, ch chan<- Event) error

  // Send — POST сообщения в signaling (offer/answer/candidate/usersInRoom-sync).
  func (c *Client) Send(ctx context.Context, token string, msg Message) error

  type Message struct {
      Type    string          `json:"type"`              // "offer"/"answer"/"candidate"/...
      To       string         `json:"to,omitempty"`      // sessionId получателя (для P2P unicast)
      Payload json.RawMessage `json:"payload,omitempty"` // SDP / ICE / ...
  }
  ```

**Inside:**
- Имена эндпоинтов — константы в `types.go` (по аналогии с `client/paths.go`):
  ```go
  const (
      pathCallFmt      = "/ocs/v2.php/apps/spreed/api/v4/call/%s"
      pathSignalingFmt = "/ocs/v2.php/apps/spreed/api/v4/signaling/%s"
  )
  ```
- `PollLoop` реализует стратегию retry/backoff из §7. Ключевые правила:
  - На `2xx`/`304` — обработать тело (если есть изменения) → немедленный повторный poll.
  - На сетевой сбой/`5xx` — экспоненциальный backoff (база 1с, потолок 30с), лог в stderr не чаще раза в 30с.
  - На `401`/`403` — `EvError{Err: &exit.ExitError{Code: 1, Err: ...}}`, loop выходит без retry.
  - На `404` — `EvError{...Code: 2...}`, loop выходит без retry.
  - `ctx.Done()` — return nil (штатный leave).
- Разбор `usersInRoom` и peer-сообщений — по фиксурам Task 2.1. Точная структура JSON уточняется по собранным данным; если в спеке есть несоответствие — приоритет за реальным трафиком (зафиксировать в комментарии в коде).
- Все ошибки — через `transport.SanitizeErr`.

- [ ] **Step 1: написать unit-тесты на парсер** — на каждую фикстуру из Task 2.1: `usersInRoom` → `[]User`, `message{type:offer}` → `Event{EvOffer, From, SDP}`, и т.д.
- [ ] **Step 2: проверить, что тесты падают.**
- [ ] **Step 3: реализовать `types.go` + `signaling.go`.**
- [ ] **Step 4: unit-тесты зелёные.**
- [ ] **Step 5: написать retry/backoff-тест** — mock `Doer` возвращает 5xx N раз, потом 2xx — проверить, что `PollLoop` сделал N+1 запросов и не вышел в exit.
- [ ] **Step 6: написать тест на 401 → EvError{Code:1} без retry** — один запрос, loop вышел.
- [ ] **Step 7: `CGO_ENABLED=0 go test ./internal/call/signaling/... -v`** — все зелёные.
- [ ] **Step 8: commit** — `feat(signaling): OCS polling + parser + retry/backoff`.

**DoD:** signaling-клиент компилируется и проходит unit-тесты; retry/backoff соответствует §7; `cmd/nctalk` по-прежнему не зависит от pion.

---

### Task 2.3: Интеграция signaling с боевым (вручную, задокументировать в `docs/integration-run.md`)

**Files:**
- Modify: `docs/integration-run.md` — добавить раздел «signaling integration».
- Create (опционально): `internal/call/signaling/integration_test.go` (build-tag `integration`).

**Interfaces:**
- Produces: документация как запустить signaling-тест; интеграционный тест за build-тегом.

**Inside:**
- Вручную запустить на боевом сервере минимальный сценарий: join call → получить один signaling-poll → корректно разобрать → leave. Использовать временный `main.go`-debbuger или `go test -tags=integration`.
- Зафиксировать факты: структура `usersInRoom`, формат `message`-payload'а, как приходят `candidate`'s (сразу пачкой или по одному).
- Если фикстуры Task 2.1 расходятся с реальным поведением — обновить их и тесты Task 2.2.

- [ ] **Step 1: написать `integration_test.go`** — `TestSignalingPollLoop_JoinGetOneEventLeave`, с проверкой что Event приходит с `Kind == EvUsersUpdated` или `EvOffer`.
- [ ] **Step 2: запустить вручную** — `CGO_ENABLED=0 NCTALK_INTEGRATION_ROOM=... NCTALK_INTEGRATION_CALL=1 go test -tags=integration ./internal/call/signaling/... -run TestSignalingPollLoop -v`.
- [ ] **Step 3: зафиксировать факты в `docs/integration-run.md`** + при необходимости обновить фикстуры.
- [ ] **Step 4: commit** — `docs(integration): signaling e2e + facts captured`.

**DoD:** R1 (signaling-протокол) — **подтверждён или выявлен как заблокировавший**; факты зафиксированы. Если протокол не удаётся разобрать за разумное время — это сигнал к выкидыванию spike'а.

---

### Task 2.4: peer-обёртка — `internal/call/peer` (один участник, без glare)

**Files:**
- Create: `internal/call/peer/peer.go`
- Create: `internal/call/peer/track.go` (audio-track + WriteSample-loop)
- Create: `internal/call/peer/peer_test.go`

**Interfaces:**
- Consumes: `pion/webrtc/v4`, `internal/call/signaling.Event`, `internal/call/media` (для `media.AudioSource`/`media.AudioSink` — см. ниже).
- Produces:
  ```go
  // Package peer — обёртка над pion/webrtc: PeerConnection на каждого
  // удалённого участника, SDP/ICE через signaling, audio-track, OnTrack.
  // Спека 2026-07-19 §7. Ничего не знает про формат звука — работает через
  // интерфейсы media.AudioSource / media.AudioSink (определены в call/media,
  // см. Task 2.5). peer — ПОТРЕБИТЕЛЬ этих интерфейсов; обратной зависимости
  // media→peer нет (спека §4: media не зависит от peer).
  package peer

  // Peer — одна PeerConnection на одного удалённого участника.
  type Peer struct { /* unexported: pc *webrtc.PeerConnection, ... */ }

  // Config для New. ICEServers — из capability сервера (Task 2.10), НЕ из env;
  // TURN-credentials (Username/Credential/CredentialType) поставляет capability
  // вместе с URL-ами серверов. Спека §5.
  type Config struct {
      ICEServers []webrtc.ICEServer // STUN + TURN (с credentials для TURN)
      IsPolite   bool               // perfect-negotiation role (спека §7)
  }

  func New(cfg Config) (*Peer, error)

  // HandleEvent — обработка одного signaling-события (Offer/Answer/Candidate).
  // Конечный автомат SDP/ICE — спека §7.
  func (p *Peer) HandleEvent(ev signaling.Event) error

  // Outgoing — канал исходящих signaling-сообщений (для отправки в signaling.Client.Send).
  Outgoing() <-chan signaling.Message

  // AttachOutgoingAudio — связывает исходящий audio-track с media.AudioSource.
  // direction sendrecv/sendonly — в зависимости от call flags (спека §6).
  func (p *Peer) AttachOutgoingAudio(src media.AudioSource) error

  // OnIncomingAudio — регистрирует media.AudioSink для входящего трека от этого
  // пира. Вызывается из pion OnTrack; sink должен быть готов к параллельным
  // WriteSample (в mesh OnTrack срабатывает на N пирах параллельно — каждый
  // пишет в свой sink; см. Task 2.8 про decoder-per-peer).
  func (p *Peer) OnIncomingAudio(sink media.AudioSink)

  // Close — закрывает PeerConnection, дренирует каналы. БЕЗОПАСЕН для вызова
  // из горутины мониторинга при DTLS/ICE-fail (см. single-peer failure ниже).
  func (p *Peer) Close() error

  // Failed — канал одностороннего уведомления о фатальной ошибке peer'а
  // (DTLS/ICE-fail, закрытое соединение удалённой стороной). Получив значение,
  // вышестоящий layer (agent/interactive) зовёт Close и удаляет peer из
  // активного набора, ПРОДОЛЖАЯ работу с оставшимися (mesh переживёт отказ).
  func (p *Peer) Failed() <-chan error
  ```

**Inside:**
- Минимальный flow без perfect-negotiation (это Task 2.6) — только accept incoming offer → SetRemoteDescription → CreateAnswer → Send. Outgoing offer создаётся по триггеру из usersInRoom.
- ICE buffering (спека §7): входящие `candidate` до `SetRemoteDescription` складываются в очередь, дренируются сразу после SetRemoteDescription.
- `TrackLocalStaticSample` с кодеком `audio/opus` для исходящего трека; goroutine читает `media.AudioSource.ReadSample` и пишет в `track.WriteSample`.
- Входящие треки: `OnTrack` → goroutine `rtpReceiver.Track().ReadSample` → `sink.WriteSample`.
- **ICEServer wiring:** `Config.ICEServers` поставляется из capability (Task 2.10), прокидывается в `webrtc.Configuration{ICEServers: cfg.ICEServers}` при создании `PeerConnection`. Empty list допустим (хост в той же сети, STUN не нужен) — pion работает с пустым списком.
- **Single-peer failure (review замечание 13, спека §10):** на DTLS/ICE-fail ОДНОГО peer'а — `peer.Close` + лог в stderr (`fmt.Fprintf(stderr, "nctalk: peer %s отключён (%v)\n", sessionId, err)`) + **продолжаем работу** с оставшимися. НЕ exit. Exit `1` только когда **все** peer'ы упали И истёк `NCTALK_ICE_TIMEOUT` (см. Task 3.2). Реализуется через горутину мониторинга `pc.OnConnectionStateChange` → при `Failed`/`Closed` послать в `p.failed` канал. Вышестоящий layer (agent/interactive) читает `Failed()` и реагирует.
- **Направление encode — одно на звонок:** исходящий audio-track создаётся один (один `media.FFmpegEncoder`, читающий PCM из stdin/device); pion сам размножает отправку на каждый remote через свои RTP-sender'ы. НЕ создаём encoder-per-peer. Это асимметрия с decode (где decoder-per-peer — см. Task 2.8).

- [ ] **Step 1: написать peer-loop тест** — два инстанса `Peer` в одном процессе, обмениваются SDP/ICE напрямую (loop, без signaling-сервера); один отправляет Opus-фреймы из мока `media.AudioSource`, второй принимает в мок `media.AudioSink`. Проверить, что фреймы дошли (спека §11).
- [ ] **Step 2: проверить, что тест падает (нет реализации).**
- [ ] **Step 3: реализовать `peer.go` + `track.go`** — минимальный flow без glare-handling. Горутина мониторинга state-change → `Failed()` канал.
- [ ] **Step 4: тест зелёный.**
- [ ] **Step 5: тест на ICE buffering** — присылаем `candidate` ДО `offer` — peer не падает, дренирует очередь после SetRemoteDescription.
- [ ] **Step 6: тест на single-peer failure** — эмуляция `pc.OnConnectionStateChange(Failed)` → значение в `Failed()` канал; `peer.Close` не паникует, не блокирует.
- [ ] **Step 7: `CGO_ENABLED=0 go test ./internal/call/peer/... -v`** — зелёный.
- [ ] **Step 8: commit** — `feat(peer): minimal PeerConnection + ICE buffering + single-peer failure handling`.

**DoD:** peer-loop без сети работает; raw Opus-фреймы проходят от source к sink; ICE buffering не падает на out-of-order; single-peer failure не роняет весь звонок (есть `Failed()` нотификация, `Close` безопасен). `Config.ICEServers` прокинут в `webrtc.Configuration`.

---

### Task 2.5: OGG-парсер (с OpusHead/comment) + round-trip PCM↔Opus через ffmpeg — `internal/call/media/ogg` + `call/media`

**Files:**
- Create: `internal/call/media/ogg/ogg.go`
- Create: `internal/call/media/ogg/ogg_test.go`
- Create: `internal/call/media/codec.go`
- Create: `internal/call/media/codec_test.go`

**Interfaces:**
- Produces:
  ```go
  // Package ogg — минимальный pure-Go OGG-демультиплексор/мультиплексор.
  // Режет OGG-страницы из ffmpeg-encode-вывода на отдельные Opus-пакеты,
  // и оборачивает raw Opus-пакеты в OGG-страницы перед ffmpeg-decode-input.
  // Спека 2026-07-19 §8 (стратегия a1).
  package ogg

  // Reader — читает OGG-страницы из io.Reader, отдаёт сырые payload'ы пакетов.
  type Reader struct { /* ... */ }
  func NewReader(r io.Reader) *Reader
  // NextPacket — возвращает следующий raw Opus-пакет (~20мс). io.EOF — конец.
  func (r *Reader) NextPacket() ([]byte, error)

  // Writer — оборачивает raw Opus-пакеты в минимальные OGG-страницы. ПЕРВЫМ
  // делом (при construction или первом WritePacket) пишет BOS-страницу с
  // OpusHead (19 байт: "OpusHead"+version+channels+pre-skip+sample-rate+
  // output-gain+channel-mapping-family, RFC 6716/5334), за ней — страницу с
  // пустым vorbis-comment (минимальный валидный vendor). Без этих заголовков
  // ffmpeg `-f opus -i -` падает «Invalid OpusHead» (review замечание 4).
  // На Flush() — EOS-страница (header_type |= 0x4). Granule_position на
  // audio-страницах — аккумулированное число 48к-отсчётов (Opus — 48 кГц native).
  type Writer struct { /* ... */ }
  func NewWriter(w io.Writer) (*Writer, error) // error — если не удалось записать OpusHead/comment
  func (w *Writer) WritePacket(payload []byte) error
  func (w *Writer) Flush() error
  ```
  ```go
  // Package media — интерфейсы AudioSource/AudioSink + codec-стратегия.
  // Спека 2026-07-19 §4/§8. Здесь же (НЕ в peer) определены интерфейсы
  // audio-источника/приёмника — peer их потребляет (media ← peer зависимость,
  // обратной нет — спека §4).
  package media

  // AudioSource — источник raw Opus-фреймов для отправляющего трека
  // (реализация — FFmpegEncoder, AudioDeviceIn; peer использует только интерфейс).
  // Спека 2026-07-19 §4/§7.
  type AudioSource interface {
      // ReadSample возвращает один raw Opus-пакет (~20мс) и его метаданные
      // (duration). io.EOF — конец потока (звонок завершён).
      ReadSample() (payload []byte, duration time.Duration, err error)
      Close() error
  }

  // AudioSink — приёмник raw Opus-фреймов с входящего трека. Спека §4/§7.
  // В mesh (Task 2.8) инстанцируется ОТДЕЛЬНЫЙ sink на каждого remote-пира
  // (decoder-per-peer); каждый decoder пишет PCM в общий media.Mixer.
  type AudioSink interface {
      WriteSample(payload []byte, duration time.Duration) error
      Close() error
  }

  // FFmpegEncoder — PCM (s16le/48к/моно) → raw Opus через ffmpeg-subprocess.
  // Запускает ffmpeg с флагами из §8 (encode). По pipe читает OGG-вывод,
  // ogg.Reader пропускает OpusHead/comment (первые два пакета не-audio) и режет
  // остальные на raw Opus-пакеты — они отдаются через ReadSample.
  // Реализует media.AudioSource.
  type FFmpegEncoder struct { /* ... */ }
  func NewFFmpegEncoder(stdin io.Reader) (*FFmpegEncoder, error)  // stdin — источник PCM
  func (e *FFmpegEncoder) ReadSample() ([]byte, time.Duration, error)
  func (e *FFmpegEncoder) Close() error

  // FFmpegDecoder — raw Opus → PCM (s16le/48к/моно) через ffmpeg-subprocess.
  // Получает raw Opus (через WriteSample), оборачивает в OGG (ogg.Writer, включая
  // корректные OpusHead/comment — см. ogg.Writer выше), кормит ffmpeg-stdin,
  // читает PCM с ffmpeg-stdout. Реализует media.AudioSink.
  //
  // ВНИМАНИЕ (review замечание 5): один FFmpegDecoder = один ffmpeg-subprocess =
  // один PCM-поток. НЕ потокобезопас на stdin (race на общий pipe). В mesh
  // (Task 2.8) инстанцируется ОТДЕЛЬНЫЙ FFmpegDecoder на каждого remote-пира;
  // их PCM-выводы сводит media.Mixer.
  type FFmpegDecoder struct { /* ... */ }
  func NewFFmpegDecoder(stdout io.Writer) (*FFmpegDecoder, error) // stdout — приёмник PCM
  func (d *FFmpegDecoder) WriteSample(payload []byte, duration time.Duration) error
  func (d *FFmpegDecoder) Close() error
  ```

**Inside:**
- OGG-формат (RFC 5334 framing): страница = magic `OggS` + version (0) + header_type + granule_position (8 bytes, little-endian) + serial (4) + page_seq (4) + checksum (4) + segment_table (1 byte count + N bytes sizes) + payload.
  - `Reader`: состояние = текущая страница, накопленный payload до границы пакета; OGG-пакет может span несколько страниц (continuation flag), но для Opus-encode-вывода ffmpeg пакеты обычно укладываются в одну страницу.
    - **Пропуск OpusHead/comment (review замечание 4):** первые два пакета OGG-потока — не audio: OpusHead (префикс `"OpusHead"`) и vorbis-comment (префикс `"OpusTags"`). `NextPacket()` **прозрачно для caller'а** их пропускает, возвращая только audio-пакеты; caller получает raw Opus-payload, готовый для `WriteSample`.
  - `Writer`: см. блок интерфейса выше — BOS с OpusHead + vorbis-comment первыми двумя страницами, далее одна audio-страница на `WritePacket`, EOS на `Flush()`. Granule_position на audio-страницах аккумулирует 48к-отсчёты.
    - Параметры OpusHead фиксируются (моно, 48 кГц): channels=1, sample_rate=48000, output_gain=0, channel_mapping_family=0, pre-skip из encode (обычно 312 для libopus voip — проверить по выводу ffmpeg `-f opus` в spike).
- Команды ffmpeg (из спеки §8):
  - encode: `ffmpeg -f s16le -ar 48000 -ac 1 -i - -c:a libopus -application voip -f opus -`
  - decode: `ffmpeg -f opus -i - -f s16le -ar 48000 -ac 1 -`
- Round-trip тест (DoD для риска R2):
  ```go
  // TestRoundTrip_PCM_Opus_PCM — проверка риска R2 (framing raw Opus ↔ OGG,
  // включая корректные OpusHead/comment — review замечание 4).
  // Генерирует тестовый PCM (sine wave 440Гц, 2с), encode через FFmpegEncoder
  // → цикл ReadSample → WriteSample в декодер → PCM-вывод. Проверяет:
  //   - пик спектра входного и выходного PCM на ~440 Гц (детекция тона);
  //   - SNR > 10 дБ в полосе вокруг 440 Гц vs. остальной спектр;
  //   - длительность детектируемого тона > 1.5с (на 2с входного — допустимая
  //     потеря на pre-skip/выравнивании);
  //   - отсутствие decode-ошибок ffmpeg (subprocess завершился 0).
  // НЕ побайтно — Opus lossy.
  // t.Skip если ffmpeg не установлен в окружении теста (спека §11).
  func TestRoundTrip_PCM_Opus_PCM(t *testing.T) {
      if _, err := exec.LookPath("ffmpeg"); err != nil {
          t.Skip("ffmpeg не установлен — пропуск round-trip теста")
      }
      // ... генерация PCM, прогон, проверка (Goertzel/DFT — pure-Go, без новых зависимостей)
  }
  ```

- [ ] **Step 1: написать тесты OGG-парсера** — на синтетических OGG-страницах (собрать вручную или ffmpeg-вывод в `testdata/`): `NextPacket` отдаёт N audio-пакетов с правильными payload'ами; OpusHead/comment прозрачно пропускаются.
- [ ] **Step 2: проверить, что тесты падают.**
- [ ] **Step 3: реализовать `ogg.go`** — Reader с пропуском OpusHead/comment, Writer с корректными BOS-заголовками + EOS на Flush.
- [ ] **Step 4: OGG-тесты зелёные.** Отдельно проверить: OGG, записанный `Writer`, читается ffmpeg-decode БЕЗ «Invalid OpusHead» (ручной smoke через `exec.Command("ffmpeg", "-f", "opus", "-i", "-", "-f", "null", "-")`).
- [ ] **Step 5: написать round-trip тест** `TestRoundTrip_PCM_Opus_PCM` с `t.Skip` если ffmpeg нет. Внутри — Goertzel/DFT-детектор тона 440 Гц (pure-Go, без новых зависимостей).
- [ ] **Step 6: реализовать `codec.go`** (FFmpegEncoder реализует `media.AudioSource`; FFmpegDecoder реализует `media.AudioSink`). Определить `AudioSource`/`AudioSink` интерфейсы здесь же (в media), НЕ в peer.
- [ ] **Step 7: round-trip тест зелёный (с ffmpeg).** SNR/длительность/частота — по критериям выше.
- [ ] **Step 8: `CGO_ENABLED=0 go test ./internal/call/media/... -v`** — все зелёные (с ffmpeg) / OGG-тесты зелёные, round-trip пропущен (без ffmpeg).
- [ ] **Step 9: commit** — `feat(media): OGG parser (OpusHead+comment) + ffmpeg codec (PCM↔Opus round-trip)`.

**DoD:** риск R2 **подтверждён как решённый** либо выявлен как заблокировавший (тогда fallback на a3 — RTP-путь — отдельной задачей). Round-trip на тестовых данных (sine 440 Гц, 2с) работает с детектируемой частотой и SNR>10 дБ. `media.AudioSource`/`media.AudioSink` интерфейсы определены здесь, peer их импортирует.

---

### Task 2.6: peer — perfect-negotiation (glare handling)

**Files:**
- Modify: `internal/call/peer/peer.go` — добавить glare-detection и rollback.
- Create: `internal/call/peer/negotiation.go` — выделенный конечный автомат.
- Modify: `internal/call/peer/peer_test.go` — glare-scenarios.

**Interfaces:**
- Produces: дополнение к `peer.Peer`:
  ```go
  // IsPolite — роль в perfect-negotiation (спека §7). Назначается детерминированно
  // по сравнению sessionId/actorId пиров: меньший id = impolite, больший = polite.
  // Оба пира приходят к одному распределению без дополнительного обмена.
  ```

**Inside:**
- Состояние negotiation в `Peer`: `idle`, `have-local-offer`, `have-remote-offer`.
- На входящий offer при `have-local-offer`:
  - `polite` → rollback (`SetLocalDescription` с типом `rollback`), принять входящий, answer.
  - `impolite` → игнорировать входящий, свой offer уйдёт и будет обработан polite-стороной.
- Тесты: смоделировать glare (оба пира зовут `CreateOffer` одновременно) — убедиться, что в конце оба пира установлены.

- [ ] **Step 1: написать glare-тест** — оба пира в `have-local-offer`, один получает входящий offer → в зависимости от роли, разные исходы.
- [ ] **Step 2: проверить, что тест падает.**
- [ ] **Step 3: реализовать `negotiation.go` + интегрировать в `peer.go`.**
- [ ] **Step 4: glare-тест зелёный.**
- [ ] **Step 5: `CGO_ENABLED=0 go test ./internal/call/peer/... -v`** — зелёный.
- [ ] **Step 6: commit** — `feat(peer): perfect-negotiation (glare handling)`.

**DoD:** glare-сценарий разрешается корректно; существующие peer-loop тесты не сломаны.

---

### Task 2.7: `Mixer` — сведение N входящих PCM-потоков

**Files:**
- Create: `internal/call/media/mixer.go`
- Create: `internal/call/media/mixer_test.go`

**Interfaces:**
- Produces:
  ```go
  // Mixer сводит N входящих PCM (s16le) потоков в один. Спека §7/§9:
  // простое суммирование отсчётов с насыщением/clipping на ±32767.
  type Mixer struct { /* ... */ }

  // NewMixer создаёт Mixer с N слотами источников. Каждый слот — это
  // идентификатор пира (sessionId или индекс); пуш через Push.
  func NewMixer(numSources int) *Mixer

  // Push добавляет PCM-буфер от источника idx. Mixer хранит last-seen
  // данные каждого источника для корректного сведения (если источник
  // временно замолчал — его последний буфер не переиспользуется бесконечно,
  // есть short-window сохранения — см. реализацию).
  func (m *Mixer) Push(idx int, pcm []int16)

  // Mix возвращает сведённый PCM-буфер длиной = min по активным источникам.
  // Clipping на ±32767.
  func (m *Mixer) Mix() []int16
  ```

**Inside:**
- Алгоритм: для каждого отсчёта `out[i] = clamp(sum(src[idx][i]), -32767, 32767)` (спека §7/§9).
- Edge cases: 0 источников → пустой буфер; 1 источник → тривиально (без копия); latency-бюджет = длина самого короткого буфера.

- [ ] **Step 1: unit-тесты** — таблица (входные N буферов, ожидаемый выход): clipping на переполнении, один источник, пустой набор, разные длины.
- [ ] **Step 2: проверить, что тесты падают.**
- [ ] **Step 3: реализовать `mixer.go`.**
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: `CGO_ENABLED=0 go test ./internal/call/media/... -run Mixer -v`** — зелёный.
- [ ] **Step 6: commit** — `feat(media): Mixer (N→1 PCM mixdown with clipping)`.

**DoD:** Mixer проходит unit-тесты; готов к интеграции с `OnTrack`-обработчиками.

---

### Task 2.8: pipe-режим — `internal/call/agent` (decoder-per-peer в mesh)

**Files:**
- Create: `internal/call/agent/agent.go`
- Create: `internal/call/agent/agent_test.go`

**Interfaces:**
- Consumes: `peer.Peer`, `signaling.Client`, `media.FFmpegEncoder`/`media.FFmpegDecoder` (через `media.AudioSource`/`media.AudioSink` интерфейсы), `media.Mixer`, `capability.Client` (Task 2.10, для `ICEServers`).
- Produces:
  ```go
  // Package agent — режим nctalk-call (pipe). Звук течёт через stdin/stdout
  // как PCM s16le/48к/моно. Спека 2026-07-19 §6/§9.
  package agent

  // Config для Run.
  type Config struct {
      Cfg         config.Config
      Token       string         // уже разрешённый (через room.ResolveRoom)
      InFlags     int            // 1 = recvonly (только --out), 3 = sendrecv (--in или оба)
      ICEServers  []webrtc.ICEServer // из capability сервера (Task 2.10)
      Stdin       io.Reader      // PCM на отправку (nil если InFlags == 1)
      Stdout      io.Writer      // PCM приём (Mixer → stdout)
      Stderr      io.Writer      // статус
  }

  // Run — главный loop: join call → запускает encoder (если send), mixer,
  // peer-loop → крутит до ctx.Done() или фатальной ошибки → leave.
  // decoder-per-peer: на каждый новый remote-peer создаётся свой FFmpegDecoder
  // (см. Inside — review замечание 5).
  func Run(ctx context.Context, cfg Config) error
  ```

**Inside:**
- **Encode-направление — ОДИН на звонок:** один `media.FFmpegEncoder` (один ffmpeg-encode-subprocess) читает PCM из `cfg.Stdin`, отдаёт raw Opus через `ReadSample`. **Все** remote peer'ы получают **копию** этого же Opus-потока через свой `AttachOutgoingAudio(encoder)` (encoder реализует `media.AudioSource`; pion сам множит отправку через RTP-sender'ы). НЕ запускаем encoder-per-peer — это асимметрия с decode.
- **Decode-направление — DECODER-PER-PEER (review замечание 5):** на каждого remote-пира при его появлении (`EvUsersUpdated` → `peer.New`) agent создаёт **свой** `media.FFmpegDecoder` (свый ffmpeg-decode-subprocess) и регистрирует его через `peer.OnIncomingAudio(decoder)` (decoder реализует `media.AudioSink`). Один decoder = один subprocess = один PCM-поток — race на общий stdin исключён (у каждого свой pipe). PCM-вывод каждого decoder'а пишется в общий `media.Mixer` (через callback / writer-обёртку, которая подаёт PCM в `mixer.Push(idx, pcm)` с уникальным idx для пира). Mixer суммирует N входов → единый PCM-микс → `cfg.Stdout`.
  - При уходе пира (`peer.Close` / `peer.Failed()`) — соответствующий decoder закрывается (`decoder.Close()`), его слот в Mixer помечается неактивным (Mixer перестаёт учитывать его в sum).
- На каждое `EvUsersUpdated` — обновить набор peer'ов (`peer.New` для новых с своим decoder'ом; `peer.Close` + `decoder.Close` для ушедших).
- На каждое `EvOffer`/`EvAnswer`/`EvCandidate` — `peer.HandleEvent`.
- Исходящие peer-сообщения → `signaling.Client.Send`.
- В SDP m-line: `sendrecv` если `InFlags==3`, `recvonly` если `InFlags==1` (спека §6).
- **Single-peer failure (review замечание 13, спека §10):** на `peer.Failed()` — `peer.Close` + `decoder.Close` соответствующего пира + лог в stderr (`fmt.Fprintf(cfg.Stderr, "nctalk: peer %s отключён (%v)\n", sessionId, err)`) + **продолжаем** работу с оставшимися peer'ами. НЕ exit. Exit `1` только когда ВСЕ peer'ы упали И истёк `NCTALK_ICE_TIMEOUT` (см. Task 3.2 — end-to-end проверка).
- Финализация: `ctx.Done()` → `LeaveCall` + закрытие всех peer'ов/decoder'ов/encoder'а + kill subprocess'ов через `cmd.Cancel` (без orphan-процессов — Task 3.4).

- [ ] **Step 1: unit-тест** — мок `signaling.Client` (последовательность Events с N=2 пирами), фейковый `peer.Peer` (через интерфейс или `peer.New`-injection), `bytes.Buffer` для stdin/stdout. Проверить: корректный join, обмен, leave, PCM-данные в stdout. Проверить, что на N=2 пирах создаются ДВА decoder'а (mock-assert на фабрику decoder'ов).
- [ ] **Step 2: тест listening-only** — `InFlags=1`, нет encoder, send-track не создаётся; на входящие треки decoder-per-peer всё равно создаётся.
- [ ] **Step 3: тест single-peer failure** — эмуляция падения одного пира из двух: `peer.Failed()` для peer-A → agent закрывает peer-A + decoder-A, peer-B продолжает работать; выхода из `Run` нет.
- [ ] **Step 4: реализовать `agent.go`** — encode-single / decode-per-peer wiring, Mixer-интеграция, single-peer failure handling.
- [ ] **Step 5: тесты зелёные.**
- [ ] **Step 6: `CGO_ENABLED=0 go test ./internal/call/agent/... -v`** — зелёный.
- [ ] **Step 7: commit** — `feat(agent): pipe mode (stdin/stdout PCM) + decoder-per-peer + single-peer failure`.

**DoD:** pipe-режим собирается, unit-тесты проходят; decoder-per-peer wiring реализован; single-peer failure не роняет весь звонок; готов к интеграции с `cmd/nctalk-call`.

---

### Task 2.9: `cmd/nctalk-call` — точка входа + spike-тест на боевом

**Files:**
- Modify: `cmd/nctalk-call/main.go`
- Create: `cmd/nctalk-call/integration_test.go` (build-tag `integration`)

**Interfaces:**
- Produces: бинарник `nctalk-call`, запускающий `agent.Run`.

**Inside:**
- `main.go`:
  - `config.Load()` → при ошибке: `fmt.Fprintln(os.Stderr, "nctalk-call: "+err); os.Exit(1)`.
  - Разбор флагов: позиционный `<room>` или `--name <name>`; `--in <path>` (default stdin); `--out <path>` (default stdout).
  - `room.ResolveRoom` → token (exit 1/2/3 при ошибках). Печать candidates при `StatusAmbiguous` — своим способом (построчно), БЕЗ импорта `render`.
  - Загрузка capability (`capability.Client` из Task 2.10) → `[]webrtc.ICEServer` для `peer.New`. При ошибке capability — продолжаем с пустым списком ICE-серверов (хост в той же сети — pion работает без STUN), лог в stderr.
  - Контекст с `signal.Notify(SIGINT, SIGTERM)` → cancel.
  - `agent.Run` → маппинг в exit-код: nil→0, `exit.ExitError`→Code, прочее→1.
- Выбор `InFlags`: только `--out` (без `--in`) → `1` (recvonly); иначе → `3` (sendrecv) (спека §6).
- **Spike-gate: объективный критерий (review замечание 6).** «Аудио идёт» — недостаточно субъективного «слышу». Главный критерий — round-trip синусоиды 440 Гц:
  - Подать на `--in` PCM-файл с тоном 440 Гц длительностью ≥3с (синтетический sine, s16le/48к/моно).
  - На принимающей стороне (браузер собеседника) — записать вывод через DevTools MediaRecorder ИЛИ на стороне `nctalk-call` подать сгенерированный браузером тестовый сигнал и записать `--out recording.pcm`.
  - В записанном PCM (decode-выход `nctalk-call`) проверить: **(а)** пик спектра на ~440 Гц (детектируется алгоритмом Гёрцеля или простым DFT на окне — pure-Go, без новой heavy-зависимости), **(б)** SNR в полосе 440 Гц vs. остальной спектр > 10 дБ, **(в)** длительность детектируемого тона > 2с (на 3с входного — допустимая потеря на ICE/pre-skip).
  - **PASS:** все три условия — переход к Этапу 3. **FAIL:** любое не выполнено — `rm -rf` и стоп (R1/R2/R3 не снят).
  - Утилита spike-check живёт в `internal/call/media/spike_check.go` (или `cmd/spike-check`); тест `TestSpikeCheck_DetectsSine440` на синтетических PCM (известный sine 440 + шум) верифицирует сам детектор.
  - Ручное прослушивание (человек в наушниках) — **дополнительно**, не единственный критерий: подтверждающее, не блокирующее.
- Интеграционный тест:
  ```go
  // +build integration
  // TestSpike_AudioBothDirections — главный spike-критерий (спека §12):
  // nctalk-call join'ит комнату с собеседником в браузере, подаёт тестовый PCM
  // (sine 440 Гц, 3с) на stdin, и одновременно пишет PCM собеседника в --out.
  // Объективный PASS — round-trip-детектор (Goertzel/DFT) находит пик 440 Гц
  // в записанном PCM с SNR>10 дБ и длительностью >2с (review замечание 6).
  // Ручное прослушивание — дополнительно.
  ```

- [ ] **Step 1: реализовать `main.go`** с флагами, ResolveRoom (с печатью candidates без render), signal-handling, вызовом capability + agent.Run.
- [ ] **Step 2: unit-тест на main** — `run` функция (по аналогии с `cmd/nctalk/main.go`), фейковый stdin/stdout.
- [ ] **Step 3: реализовать spike-check утилиту** (`internal/call/media/spike_check.go` или `cmd/spike-check`) — Goertzel/DFT-детектор тона 440 Гц + тест на синтетическом PCM.
- [ ] **Step 4: `CGO_ENABLED=0 go build -o /tmp/nctalk-call ./cmd/nctalk-call`** — бинарник собирается.
- [ ] **Step 5: написать integration_test.go** (за build-тегом) с объективным PASS-критерием (round-trip 440 Гц через spike-check).
- [ ] **Step 6: запустить spike на боевом вручную** — с собеседником в браузере, подать `--in sine440_3s.pcm --out recording.pcm`; после 30с — leave, прогнать `spike-check recording.pcm`.
- [ ] **Step 7: по результатам — обновить фикстуры/комментарии**, если обнаружены расхождения со спекой.
- [ ] **Step 8: commit** — `feat(cmd/nctalk-call): entry point + objective spike-gate (440 Hz round-trip)`.

**DoD spike'а (критерий решения R1+R2+R3):** round-trip синусоиды 440 Гц между `nctalk-call` и браузером на боевом детектируется на выходе с SNR>10 дБ и длительностью >2с. **Если да — переход к Этапу 3. Если нет — `rm -rf` и стоп.** Ручное прослушивание — дополнительно к объективному критерию, не вместо него.

---

### Task 2.10: capability-клиент — STUN/TURN-серверы из capability сервера

> **NB по нумерации:** задача получила номер 2.10 как новая (review замечание 7 санкционировало «новую задачу под новым номером — напр. 2.10 для capability»). В **DAG** (см. конец плана) она стоит **до Task 2.4** — `peer.Config.ICEServers` требует capability как вход. Документно расположена после Task 2.9 для сохранения нумерации существующих задач.

**Files:**
- Create: `internal/call/capability/capability.go`
- Create: `internal/call/capability/capability_test.go`
- Modify: `testdata/signaling/capability.json` — добавлен/уточнён в Task 2.1; здесь используется как тестовая фикстура.

**Interfaces:**
- Consumes: `internal/transport.Doer` (через `transport.DoOCS`), `internal/transport.Auth`.
- Produces:
  ```go
  // Package capability — клиент OCS-capability сервера: извлекает STUN/TURN-
  // серверы для WebRTC-ICE. Спека 2026-07-19 §5 («STUN/TURN — берётся из
  // capability сервера, не из env»). Никаких кредов в env пользователя — TURN-
  // credentials (если нужны) приходят в той же signaling-config от сервера.
  package capability

  type Auth = transport.Auth   // alias

  // Client — read-only клиент capability. Не знает про WebRTC напрямую;
  // возвращает []webrtc.ICEServer как структуру данных (URLs + опциональные
  // Username/Credential/CredentialType для TURN).
  type Client struct { /* unexported: doer, auth */ }
  func New(auth Auth, doer transport.Doer) *Client

  // ICEServers запрашивает GET /ocs/v2.php/cloud/capabilities (или
  // spreed-signaling-config эндпоинт — уточнить по реальному ответу сервера
  // в Task 2.1), извлекает spreed.signaling.stunservers / turnservers (или
  // аналогичное поле — точный путь по фикстуре), маппит в []webrtc.ICEServer.
  // Для TURN: Username/Credential/CredentialType заполняются из signaling-config
  // (если сервер их предоставляет; для STUN эти поля пустые).
  // Возвращает (nil, nil) если capability не содержит spreed-секции или там нет
  // STUN/TURN — это НЕ ошибка, pion работает с пустым ICEServers (хост в той
  // же сети). Ошибка возвращается только при сетевом/HTTP-сбое (через transport).
  func (c *Client) ICEServers(ctx context.Context) ([]webrtc.ICEServer, error)
  ```

**Inside:**
- Эндпоинт: `GET /ocs/v2.php/cloud/capabilities` (или spreed-signaling-config — точно определить по фикстуре Task 2.1, оба варианта допустимы; предпочтительно capabilities, т.к. оно уже используется другими клиентами Talk).
- Парсинг: OCS-конверт → `data.capabilities.spreed` (или `data.capabilities.spreed.signaling`). Точный JSON-путь к `stunservers`/`turnservers` зафиксировать по реальной фикстуре; если в spreed нет signaling-секции — fallback на `data.capabilities.spreed.signaling.v2` или внешнее HPB-поле (при наличии). При отсутствии и того и другого — вернуть пустой список (см. контракт `ICEServers`).
- TURN-credentials: для каждого TURN-сервера сервер может вернуть `username`/`credential`/`credentialType` (обычно `password` или `oauth`) — маппить напрямую в `webrtc.ICEServer{Username, Credential, CredentialType}`. Не логировать креды (спека §5).
- **Не архивируем креды в env/логах** — `[]webrtc.ICEServer` живёт только в памяти процесса и прокидывается в `peer.New`; после `peer.Close` — GC.
- Мокируется интерфейсом `transport.Doer` в unit-тестах (фикстура `testdata/signaling/capability.json`).

- [ ] **Step 1: unit-тесты на парсер** — на фикстуре `capability.json` (Task 2.1): извлечение STUN-списка, TURN-с-credentials, пустой spreed-секции → nil без ошибки.
- [ ] **Step 2: проверить, что тесты падают.**
- [ ] **Step 3: реализовать `capability.go`** — `ICEServers(ctx)` через `transport.DoOCS` + парсинг JSON.
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: регрессия изоляции** — `CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion` — capability зависит от `pion/webrtc/v4` (использует `webrtc.ICEServer`), но изолирована под `internal/call/`, в `cmd/nctalk` не попадает.
- [ ] **Step 6: `CGO_ENABLED=0 go test ./internal/call/capability/... -v`** — зелёный.
- [ ] **Step 7: commit** — `feat(capability): STUN/TURN client from OCS capabilities`.

**DoD:** capability-клиент возвращает `[]webrtc.ICEServer` (с TURN-credentials при наличии) из capability сервера; работает на фикстуре Task 2.1; `cmd/nctalk` по-прежнему не зависит от pion; интегрирован в Task 2.4 (`peer.Config.ICEServers`) и Task 2.9 (`cmd/nctalk-call/main.go`).

---

## Этап 3 — Модуль агента `nctalk-call` в полноценный вид

**Цель этапа:** превратить spike-код в production-качество: полные unit-тесты на все пути, обработка ошибок из спеки §10, интеграционные тесты на боевом за build-тегом. **Точка выпиливания:** если выясняется, что агентский режим не окупается (например, задержки неприемлемы) — можно выкинуть `cmd/nctalk-call` + `call/agent`, не трогая будущее TUI.

**Риск этапа:** занижение стабильности — если не покрыть retry/edge-кейсы, pipe-режим будет падать в реальных сценариях. Mitigation: явные тесты на каждый сценарий из §10.

### Task 3.1: Обработка ошибок по §10 — exit-коды и диагностика

**Files:**
- Modify: `internal/call/agent/agent.go` — маппинг ошибок в exit-коды.
- Modify: `cmd/nctalk-call/main.go` — форматирование диагностик в stderr.
- Modify: `internal/call/agent/agent_test.go` — тесты на каждый exit-код.

**Inside:**
- Сеть/HTTP/`401`/5xx на Call API → exit 1 + понятное сообщение (через `transport.SanitizeErr`).
- Call API `403` (read-only / нет прав) → exit 1 с сообщением сервера.
- Call API `404` → exit 2.
- Call API `412` (lobby) → exit 1 с пояснением «lobby».
- Signaling `401`/`403` → exit 1 без retry (уже в Task 2.2, тут проверка end-to-end).
- Signaling `404` → exit 2.
- ICE timeout / все peer'ы упали → exit 1 с диагностикой (отличать «я один в звонке» → exit 0).
- `ffmpeg` отсутствует/упал → exit 1 с указанием backend'а и как установить.
- **Single-peer failure — end-to-end проверка (review замечание 13, спека §10):** поведение «при DTLS/ICE-fail на одном peer — `peer.Close` + лог в stderr + ПРОДОЛЖАЕМ работу» реализовано в Task 2.4 (peer-side: `Failed()` канал) и Task 2.8 (agent-side: подписка на `Failed()`, cleanup соответствующего decoder'а). Здесь — end-to-end тест: симулировать N=3 пиров, уронить 1 → агент продолжает работу с 2 оставшимися, exit НЕ происходит; уронить ещё 1 → продолжает с 1; уронить последний → срабатывает Task 3.2 (`NCTALK_ICE_TIMEOUT` истёк, все peer'ы упали) → exit 1.

- [ ] **Step 1: написать тесты на каждый сценарий exit-кода** (мок signaling возвращает нужные ошибки).
- [ ] **Step 2: написать end-to-end тест на single-peer failure (N=3 → N=2 → N=1 → N=0)** — без exit на промежуточных падениях; exit 1 только когда все упали И таймаут истёк.
- [ ] **Step 3: реализовать недостающую часть маппинга** (если Task 2.4/2.8 оставили end-to-end дыры — закрыть здесь).
- [ ] **Step 4: тесты зелёные.**
- [ ] **Step 5: commit** — `feat(agent): exit codes per spec §10 + e2e single-peer failure`.

**DoD:** каждый exit-код из §10 покрыт тестом; single-peer failure (mesh с N пиров, поочерёдное падение) НЕ роняет агент до тех пор, пока не упадут ВСЕ peer'ы И не истечёт `NCTALK_ICE_TIMEOUT`.

---

### Task 3.2: ICE timeout — `NCTALK_ICE_TIMEOUT` и различение сценариев

**Files:**
- Modify: `internal/call/peer/peer.go` — таймер на установку peer'а.
- Modify: `internal/call/agent/agent.go` — решение exit 0 vs 1.
- Modify: соответствующие тесты.

**Inside:**
- По умолчанию `NCTALK_ICE_TIMEOUT=30s`. Если не один peer не установился за таймаут:
  - участников в звонке 1 (только я) → exit 0 (штатно, ждём других).
  - участники есть, но никто не ответил ICE → exit 1 с диагностикой.
- Если peer'ы были и поочерёдно упали, и ни одного живого не осталось **по истечении ICE_TIMEOUT** → exit 1. До истечения — продолжаем.

- [ ] **Step 1: unit-тесты на оба сценария** (exit 0 при одиночестве, exit 1 при отказе ICE).
- [ ] **Step 2: реализовать таймаут + логику различения.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(peer): ICE timeout + alone-vs-failed distinction`.

**DoD:** ICE timeout работает по контракту §6/§10.

---

### Task 3.3: Интеграционные тесты `nctalk-call` на боевом

**Files:**
- Create: `cmd/nctalk-call/integration_test.go` (если не создан в Task 2.9) или расширить.
- Modify: `docs/integration-run.md`.

**Inside:**
- Сценарии:
  - join + leave без участников (exit 0).
  - join + 1 собеседник (mocked browser?) — минимально: join + exchange offer/answer + leave.
  - `--out` только запись (listening-only) — `flags=1`, нет send-track.
  - `--in + --out` — `flags=3`, sendrecv.
  - Ctrl-C во время звонка — корректный leave.
- Флаги: `NCTALK_INTEGRATION_CALL=1` + `NCTALK_INTEGRATION_ROOM`.

- [ ] **Step 1: написать интеграционные тесты** (по аналогии с `internal/client/integration_test.go`).
- [ ] **Step 2: запустить вручную на боевом.**
- [ ] **Step 3: зафиксировать в `docs/integration-run.md`.**
- [ ] **Step 4: commit** — `test(cmd/nctalk-call): integration scenarios`.

**DoD:** интеграционный набор покрывает основные сценарии; документация актуальна.

---

### Task 3.4: Защита от orphan-ffmpeg-процессов

**Files:**
- Modify: `internal/call/media/codec.go` — гарантированный kill при `ctx.Done()`.
- Modify: `internal/call/agent/agent.go` — `defer cmd.Wait()` после kill.
- Create: `internal/call/media/codec_test.go` (дополнения).

**Inside:**
- `exec.CommandContext` для запуска ffmpeg; `cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }` + `cmd.WaitDelay` (Go 1.20+).
- Тест: запустить ffmpeg, отменить ctx, убедиться что процесс убит (через `pgrep` или мониторинг `cmd.ProcessState`).

- [ ] **Step 1: написать тест на orphan-protection** — cancel ctx, проверить что ffmpeg мёртв.
- [ ] **Step 2: реализовать `exec.CommandContext` + WaitDelay.**
- [ ] **Step 3: тест зелёный.**
- [ ] **Step 4: commit** — `feat(media): guarantee ffmpeg kill on ctx cancel`.

**DoD:** никаких orphan-процессов после Ctrl-C; тест подтверждает.

---

## Этап 4 — Модуль человека `nctalk-talk` (TUI + device-IO)

**Цель этапа:** добавить интерактивный режим — микрофон/динамик через системные утилиты, TUI с хоткеями. **Точка выпиливания:** если TUI/device-IO нестабилен — можно выкинуть `cmd/nctalk-talk` + `call/interactive`, оставив `cmd/nctalk-call` (pipe-режим).

**Риск этапа:** device-IO через subprocess чувствителен к платформе (macOS avfoundation vs ALSA); TUI может конфликтовать с одновременным выводом звука. Mitigation: вся device-IO за интерфейсом, переключатель `--audio-backend`.

### Task 4.1: Audio-IO через `ffmpeg`+avfoundation — `internal/call/interactive/audio_io.go`

**Files:**
- Create: `internal/call/interactive/audio_io.go`
- Create: `internal/call/interactive/audio_io_test.go`

**Interfaces:**
- Produces:
  ```go
  // AudioDeviceIn / AudioDeviceOut — реализации media.AudioSource / media.AudioSink
  // через ffmpeg-subprocess на macOS (avfoundation). Спека 2026-07-19 §8.
  type AudioDeviceIn struct { /* ... */ }   // mic → PCM → Opus
  func NewAudioDeviceIn(ctx context.Context, device string) (*AudioDeviceIn, error)
  // Implements media.AudioSource (ReadSample).

  type AudioDeviceOut struct { /* ... */ }  // Opus → PCM → speaker
  func NewAudioDeviceOut(ctx context.Context, device string) (*AudioDeviceOut, error)
  // Implements media.AudioSink (WriteSample).
  ```

**Inside:**
- Команды ffmpeg из §8 (nctalk-talk-секция):
  - capture+encode: `ffmpeg -f avfoundation -i "<device>" -ac 1 -ar 48000 -c:a libopus -application voip -f opus -`
  - decode+play: `ffmpeg -f opus -i - -f avfoundation "<device>"`
- Device default: `:0` (первый системный аудио-вход/выход). Из env: `NCTALK_AUDIO_DEVICE_IN`/`OUT`.
- SoX-fallback в отдельном типе или через флаг `--audio-backend sox`:
  - capture: `rec -q -r 48000 -c 1 -b 16 -e signed-encoding -t raw -`
  - play: `play -q -r 48000 -c 1 -b 16 -e signed-encoding -t raw -`
- Все subprocess'ы через `exec.CommandContext`, гарантированный kill.

- [ ] **Step 1: тесты** — мок subprocess через `exec.LookPath` skip если ffmpeg нет; тест на минималках (запустить, сразу отменить — проверить что не зависло).
- [ ] **Step 2: реализовать `audio_io.go` (ffmpeg + sox).**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: commit** — `feat(interactive): audio device IO via ffmpeg/sox`.

**DoD:** audio-IO через subprocess работает (манипуляция устройствами в тестах пропущена, но сборка/разбор/kill корректны).

---

### Task 4.2: TUI-цикл — список участников, mute, leave, громкость

**Files:**
- Create: `internal/call/interactive/tui.go`
- Create: `internal/call/interactive/tui_test.go`
- Modify: `internal/call/interactive/interactive.go` — связка TUI + audio + peer.

**Interfaces:**
- Produces:
  ```go
  // Run — главный loop nctalk-talk. Спека 2026-07-19 §6.
  func Run(ctx context.Context, cfg Config) error
  type Config struct {
      Cfg    config.Config
      Token  string
      Stderr io.Writer
      // TUI использует raw terminal (через golang.org/x/term? или минимально
      // через syscall на macOS — оставить решение за реализацией; stdlib-only
      // предпочтительнее, иначе добавить ещё одну зависимость)
  }
  ```

**Inside:**
- TUI рисует (минимально):
  ```
  Участники (3):
    [говорит] alice
    bob (muted)
    charlie
  Хоткеи: M=mute, Q=leave, +/−=громкость
  ```
- Хоткеи (§6):
  - `M` — mute/unmute локального источника (перестать `WriteSample` на свой track; Call API `flags` **не меняется** — спека §6).
  - `Q`/`Ctrl-C` — leave (DELETE call + закрытие peer'ов).
  - `+`/`-` — громкость вывода (применяется к PCM после Mixer'а перед device-out).
- mute-статус других участников — из signaling `usersInRoom` (поле `inCall` flags).
- Решение по TUI-библиотеке: **рекомендуется stdlib-only** через `syscall` (raw mode на macOS) + ручная отрисовка; если оказывается сложно — добавить `golang.org/x/term` (не нарушает CGO_ENABLED=0, но это вторая не-stdlib зависимость после pion — явно отметить в `go.mod`).

- [ ] **Step 1: unit-тесты на hotkey parsing и состояние mute** — таблица (ввод → состояние).
- [ ] **Step 2: реализовать `tui.go`.**
- [ ] **Step 3: тесты зелёные.**
- [ ] **Step 4: вручную проверить на боевом** — join + M + Q.
- [ ] **Step 5: commit** — `feat(interactive): TUI (participants, mute, leave, volume)`.

**DoD:** TUI работает на боевом; mute корректно глушит отправку (но не меняет flags).

---

### Task 4.3: `cmd/nctalk-talk` — точка входа + интеграция

**Files:**
- Modify: `cmd/nctalk-talk/main.go`
- Create: `cmd/nctalk-talk/integration_test.go` (build-tag `integration`)

**Inside:**
- Минимальный `main.go`: `config.Load` → `room.ResolveRoom` → `interactive.Run` с `flags=3` (sendrecv всегда — спека §6).
- Флаги: `--audio-backend sox|ffmpeg` (default ffmpeg), `--name`/positional.
- Exit-коды: те же, что у `nctalk-call`.

- [ ] **Step 1: реализовать main.go.**
- [ ] **Step 2: integration-тест** — join + 5 секунд + leave.
- [ ] **Step 3: `CGO_ENABLED=0 go build -o /tmp/nctalk-talk ./cmd/nctalk-talk`.**
- [ ] **Step 4: вручную проверить на боевом.**
- [ ] **Step 5: commit** — `feat(cmd/nctalk-talk): entry point + integration`.

**DoD:** `nctalk-talk` собирается и работает на боевом; готов к ручному тест-драйву.

---

### Task 4.4: Финальная проверка изоляции и отката

**Files:**
- Modify: (нет, это regression-тест).
- Modify: `internal/call/isolation_test.go` — расширить: после всех этапов проверить, что `rm -rf` действительно работает.

**Inside:**
- Скрипт-проверка (можно в Makefile или в `docs/rollback.md`):
  ```sh
  # Проверка отката WebRTC (спека §3):
  git stash  # или cp -r . /tmp/nctalk-backup
  rm -rf internal/call cmd/nctalk-call cmd/nctalk-talk
  go mod tidy
  CGO_ENABLED=0 go build ./cmd/nctalk
  CGO_ENABLED=0 go test ./...
  # Ожидание: всё зелёное, pion исчез из go.mod, internal/transport и internal/room остались.
  ```
- Фиксация в `docs/rollback.md`: пошаговая инструкция отката.

- [ ] **Step 1: выполнить откат вручную** (в worktree или временной копии).
- [ ] **Step 2: `go build ./cmd/nctalk && go test ./...`** — зелёное.
- [ ] **Step 3: проверить что `internal/transport` и `internal/room` на месте** (спека §3).
- [ ] **Step 4: документировать в `docs/rollback.md`.**
- [ ] **Step 5: commit** — `docs: rollback verification for WebRTC boundary`.

**DoD:** граница удаления работает как описано в спеке §3.

---

## Этап 5 (опционально, позже) — Будущее

Не входят в MVP (спека §13). Перечислены только как ориентиры:

- Нативная CoreAudio через CGO (за build-тегом) — если subprocess-вариант упрётся в latency.
- HPB/SFU для звонков >7 участников — отдельная реализация `call/signaling` (по WebSocket + Janus); интерфейсы `peer`/`media` позволяют не трогать.
- Видео (video-track).
- Windows/Linux (ALSA/PulseAudio/PipeWire).
- `--mute-updates-flags` (снимать `WITH_AUDIO` через `PUT /call/{token}`).
- Авто-детекция/конвертация входного PCM через `ffmpeg`-прелоад.
- AEC/шумоподавление в процессе.

---

## Зависимости между задачами (критический путь)

```
Task 0.1 (skeleton) ─┬─► Task 0.2 (isolation test)
                    │
                    ├─► Task 0.3 (add pion) [только для Этапа 2; НЕ зависит от Task 1.x]
                    │
                    └─► Task 1.1 (transport) ─► Task 1.2 (room)

Task 2.1 (fixtures) ─► Task 2.2 (signaling) ─► Task 2.3 (e2e signaling)
                                       │
Task 0.3 (pion) ──┐                   │
Task 2.1 ─────────┴─► Task 2.10 (capability) ──┐
Task 2.5 (OGG+codec) ─────────────────────────┤
                                              ├─► Task 2.4 (peer) ─► Task 2.6 (glare)
                                              │
                                              └─► Task 2.7 (mixer) ─► Task 2.8 (agent) ─► Task 2.9 (cmd/nctalk-call spike)
                                                                                            │
                                              ┌────────────────────────────────────────────┘ [SPIKE GATE]
                                              │
                                              ├──► Этап 3 (production agent)
                                              │
                                              └──► Этап 4 (TUI)
```

**Критический путь (без pion на старте Этапа 1):** 0.1 → 1.1 → 2.1 → 2.2 → 2.3 → 2.10 → 2.4 → 2.8 → 2.9.
**Критический путь (с pion):** 0.1 → 0.3 → 2.5 → 2.10 → 2.4 → 2.8 → 2.9 (pion-зависимая часть).
**Общий критический путь:** 0.1 → 1.1 → 2.1 → 2.2 → 2.3 → 2.10 → 2.4 → 2.8 → 2.9 (Task 0.3 и Task 2.5 идут параллельно с paths 1.x/2.1-2.3 и встречаются в Task 2.4). **Spike gate** — после Task 2.9.

**Параллелимые задачи:**
- **Task 1.1 || Task 0.3** (review замечание 11: разные подсистемы — рефакторинг HTTP-транспорта и добавление pion-зависимости; не связаны).
- **Task 1.1 || Task 1.2** (хотя 1.2 концептуально следует за 1.1, формально независимы: `internal/room` не зависит от `internal/transport`).
- **Task 2.4 || Task 2.5 || Task 2.7** (после Task 2.10 для 2.4, после Task 0.3 для 2.5 и 2.7; три независимые подсистемы — peer/codec/mixer).
- **Task 3.x параллельно между собой** (после 2.9).
- **Task 4.x параллельно** (после 2.9).

**Обоснование зависимостей (review замечание 11):**
- **Task 0.3 → Этап 2 только:** pion нужен `peer`/`media`/`capability`, не нужен `transport`/`room`. Task 0.3 ставится в DAG параллельно Этапу 1, до Task 2.4 (первого потребителя pion).
- **Task 1.1 → Task 2.2/2.10:** signaling и capability используют `transport.DoOCS`.
- **Task 2.1 → Task 2.2/2.10:** фикстуры нужны для тестов парсеров.
- **Task 2.10 → Task 2.4:** `peer.Config.ICEServers` поставляет capability.
- **Task 2.5 → Task 2.4:** `peer` импортирует `media` для интерфейсов `AudioSource`/`AudioSink` (review замечание 3: media ← peer, не наоборот).
- **Task 2.4 + Task 2.7 → Task 2.8:** agent связывает peer, mixer, decoder-per-peer.
- **Task 2.8 → Task 2.9:** cmd/nctalk-call зовёт `agent.Run`.

---

## Self-review замечания (исполнитель: игнорировать)

- Спека §3 (инвариант изоляции) — покрыт Task 0.2 (статический тест, точный префикс `pion/webrtc/v4` — review замечание 12) + Task 4.4 (динамический откат).
- Спека §4 (структура пакетов) — покрыт Task 0.1. **DAG-инвариант (review замечание 3):** `media ← peer` (media определяет `AudioSource`/`AudioSink`, peer их потребляет — Task 2.5 + Task 2.4), `media ← agent/interactive`. `peer` НЕ определяет audio-интерфейсы.
- Спека §5 (конфиг/безопасность) — наследуется от базового CLI; явная проверка в Task 1.1 (`TestRun_PasswordDoesNotLeak_CrossHostRedirect` остаётся зелёным). STUN/TURN — из capability (Task 2.10), НЕ из env.
- Спека §6 (команды) — `nctalk-call`: Task 2.8, 2.9, 3.x; `nctalk-talk`: Task 4.x. Флаги `--in`/`--out`, InFlags 1/3 — Task 2.8/2.9. Mute в TUI — Task 4.2.
- Спека §7 (signaling + peer) — Task 2.2 (polling), 2.4 (один peer, single-peer failure lifecycle), 2.6 (glare). v4 endpoint — константа в Task 2.2. ICE buffering — Task 2.4. Mixdown — Task 2.7.
- Спека §8 (codec-стратегия) — Task 2.5 (OGG с OpusHead/comment, PCM↔Opus round-trip с объективным критерием); fallback (a3)/(b) упомянуты, не отдельные задачи (только если spike провалится).
- Спека §9 (поток данных) — реализуется сборкой в Task 2.8 (encoder-single, decoder-per-peer, Mixer — review замечание 5).
- Спека §10 (обработка ошибок) — Task 3.1 (exit-коды + end-to-end single-peer failure), 3.2 (ICE timeout).
- Спека §11 (тестирование) — каждый Task имеет unit-тесты; интеграция — Task 2.3, 2.9 (с объективным spike-gate — review замечание 6), 3.3, 4.3.
- Спека §12 (поэтапный план) — Этапы 0–4 соответствуют spike-first; gate после Task 2.9 с объективным PASS-критерием (round-trip 440 Гц).
- Спека §13 (будущее) — Этап 5.

**Соответствие review-замечаниям (13 + мелочи):**
1. Task 1.1 — циклический импорт → `OCSError`/`OCSEnvelope` перенесены В `internal/transport` (Choice зафиксирован); `cli.exitFromClientErr` использует `*transport.OCSError` через `errors.As`.
2. Task 1.2 — `ResolveRoom` чистая логика, БЕЗ `render`; печать candidates — в вызывающем коде (`cli` обёртка для backcompat, `cmd/nctalk-call`/`cmd/nctalk-talk` своим способом).
3. Task 2.4/2.5 — `AudioSource`/`AudioSink` в `call/media`, НЕ в `peer`; зависимость `media ← peer`.
4. Task 2.5 — `ogg.Writer` пишет BOS с OpusHead+vorbis-comment; `ogg.Reader.NextPacket` пропускает не-audio пакеты.
5. Task 2.7/2.8 — decoder-per-peer (N декодеров в mesh → Mixer); encoder-single.
6. Task 2.9 — объективный spike-gate: round-trip синусоида 440 Гц, Goertzel/DFT, SNR>10 дБ, длительность >2с.
7. Task 2.10 (новая) — capability-клиент для STUN/TURN (`[]webrtc.ICEServer`).
8. Task 1.1 — восстановлена паника на невалидном BaseURL.
9. Task 0.3 — зафиксирована конкретная версия pion `v4.0.47` (или актуальный стабильный `v4.0.x`).
10. Task 2.4 — `Config.ICEServers []webrtc.ICEServer` (вкл. TURN credentials) вместо `STUNServers`/`TURNServers []string`.
11. DAG унифицирован: Task 1.1 НЕ зависит от Task 0.3 (параллельны); ASCII-DAG и критический путь согласованы.
12. Task 0.2 — `strings.HasPrefix(line, "github.com/pion/webrtc/v4 ")` (с пробелом) или точное равенство вместо `Contains`.
13. Task 2.4 + Task 3.1 — single-peer failure: `peer.Close` + лог + продолжаем; exit 1 только когда все peer'ы упали И `NCTALK_ICE_TIMEOUT` истёк.

**Мелочи:**
- Task 1.1 — ссылка на `redact_test.go` исправлена: тест `TestSanitizeErr_URLRedacted` лежит в `client_test.go` (файла `redact_test.go` в дереве нет).
- Task 1.1 — `transport.Auth.Timeout` удалён (мёртвый код: timeout выставляется на `http.Client`, не на `Auth`/запрос).
- Task 2.2 — отступы комментариев в `Event struct` выровнены по gofmt.

Именованные типы/функции проверены на консистентность: `transport.DoOCS`, `transport.Doer`, `transport.Auth`, `transport.OCSError`, `transport.OCSEnvelope`, `signaling.Client.PollLoop/Send/JoinCall/LeaveCall`, `signaling.Event/User/ICECandidate/Message`, `peer.Peer/Config` (с `ICEServers`/`Failed()`), `media.AudioSource/AudioSink`, `media.FFmpegEncoder/FFmpegDecoder/Mixer`, `ogg.Reader/Writer` (с OpusHead/comment), `capability.Client.ICEServers`, `agent.Run/Config`, `interactive.Run/Config`, `room.ResolveRoom/RoomLister/Result/Status`, `exit.ExitError/Exit*/FromClientErr`.
