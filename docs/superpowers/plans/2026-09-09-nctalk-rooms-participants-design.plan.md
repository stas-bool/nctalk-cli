# `nctalk rooms participants` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить в `nctalk` девятую команду `rooms participants <room>` — список участников комнаты (read-only GET room-API), закрывающую строку TODO.

**Architecture:** Дельта к базовому CLI по спеке `docs/superpowers/specs/2026-09-09-nctalk-rooms-participants-design.md` (дизайн утверждён; эндпоинт сверен с живым сервером 2026-09-09). Слои затрагиваются как у всех существующих команд: `internal/client` (новый файл `participants.go`: канонический `Participant` + `GetParticipants` над `doOCS`), `internal/cli` (handler `roomsParticipantsHandler` + метод в интерфейсе `TalkClient` + 6 моков + роутинг + help-декларация `cmdSpecs` + канон-тесты), `internal/render` (`ParticipantsTable`/`ParticipantsJSON`), интеграционный read-only тест за build-тегом. Сортировка и fallback имени — клиентская пост-обработка в handler (прецедент — фильтры `rooms list`); маппинг ролей 1–6 → текст живёт в render (представление, не модель).

**Tech Stack:** Go 1.21+, только stdlib. Новых зависимостей нет.

## Global Constraints

- **`CGO_ENABLED=0` обязательно** для всех `go build` / `go test` / `go vet` (macOS Tahoe 26.5 + Go 1.21.4: без него dyld-падение `missing LC_UUID`).
- Только stdlib; новых импортов вне stdlib не вводить.
- **Кириллические комментарии в существующем коде сохранять**; новые комментарии писать на русском в стиле окружающего файла (конвенция всего репозитория).
- **Коммиты: Conventional Commits обычным текстом, БЕЗ emoji** (`feat: ...`, не `✨ feat: ...`) и **БЕЗ какой-либо AI-атрибуции** — никакого `Co-Authored-By: Claude` (юридическое требование владельца репозитория, перекрывает дефолты ассистента).
- Секреты/личные URL в код, тесты, фикстуры и коммиты не допускать (публичный репозиторий `stas-bool/nctalk-cli`); фикстуры — обезличенные, реального формата эндпоинта.
- Эталон контракта — спека `2026-09-09-nctalk-rooms-participants-design.md`; базовый контракт exit-кодов — `2026-07-17-nctalk-cli-design.md` §7, без изменений: `0` успех (в т.ч. пустой список), `1` общая (сеть/401/5xx/неизвестный флаг/оба пусты), `2` not found (OCS 404 / 0 совпадений `--name`), `3` неоднозначное `--name` (>1 совпадение, кандидаты в stderr).
- API-контракт (сверено с живым сервером 2026-09-09, спека §3): `GET /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants`, заголовки OCS (Basic-auth + `OCS-APIRequest: true` + `Accept: application/json`) через существующий `doOCS`; пагинации нет; `sessionIds` — массив строк (непустой = онлайн).
- Изоляция от WebRTC не нарушается: правки только в `internal/client`, `internal/cli`, `internal/render`, `testdata/`, доки. `internal/room` не трогать (там свой минимальный `RoomLister`).
- Правка каждого файла — с сохранением его стиля (ручной scan флагов без flag-пакета; handler-ы возвращают `ExitError`; таблицы через `text/tabwriter` с параметрами `newTabwriter`).

## Риски и альтернативы

**Риски (по спеке, снятие — в задачах):**

1. **Формат гостевых полей — assumption (спека §4):** `actorId` вида `"guest::<anon-id>"` и пустой `displayName` гостя на этом эндпоинте живьем не сверены (источник — reactions-эндпоинт `ReactionActor` + документация). Поэтому гостевая фикстура — отдельная, с пометкой «synthetic» в имени файла, и НЕ строкой внутри основной фикстуры «реального формата» (инвариант CLAUDE.md: фикстуры обязаны отражать реальный формат эндпоинта). При первом живом прогоне комнаты с гостем — сверить фактические поля и заменить синтетику обезличенным реальным ответом (Task 4, Step 4).
2. **`data: null` обнуляет слайс:** `json.Unmarshal([]byte("null"), &out)` оставляет nil-слайс, а `RawMessage("null")` проходит guard `len(data)>0` — прецедент nil-map у `GetReactions`. Guard `out == nil → []Participant{}` обязателен (Task 1). Поведение проверено на Go 1.21: `data: []` декодируется в non-nil пустой слайс, `data: null` — в nil; guard покрывает оба случая идемпотентно.
3. **`sessionIds: null` внутри участника** → nil-поле → `--json` напечатал бы `"sessionIds": null`. Нормализация циклом после decode (Task 1), контракт — детерминированный `[]`.
4. **Расширение интерфейса `cli.TalkClient` ломает все 6 тестовых моков** — пакет `cli` не соберётся с тестами, пока стаб не добавлен в каждый. Это первый шаг Task 2 (compile-правка), отдельным атомарным куском.
5. **Format-drift `participantType`:** серверные константы 4–6 в живой выборке не встречались (из документации Talk); значения > 6 могут появиться в будущем — роль печатается самим числом (спека §2), тест покрывает (Task 2).
6. **Former-комната, разрезолвленная через `--name`:** запрашивающий — не участник → ожидаем OCS-ошибку → exit `1`/`2` по общему контракту, спецобработки нет (спека §1 Out of scope; проверка на живом — вне дельты).

**Альтернативы (отклонены при брейншторме, спека §1 — не реализовать):**

- B: флаг `--participants` у `rooms list`/`find` — ломает семантику list (список комнат, не людей).
- C: новый top-level ресурс — ломает паттерн `<resource> <verb>`.
- Проксировать сырой ответ API в `--json` — отклонено: тонкий клиент отдаёт каноническую модель (как `Room`), семь полей.

---

### Task 1: client — `Participant`, `GetParticipants` + фикстуры реального формата

**Files:**
- Create: `internal/client/participants.go`
- Create: `testdata/room_participants.json` (корневой `testdata/`, конвенция репо)
- Create: `testdata/room_participants_synthetic_guest.json` (synthetic, помечена в имени)
- Create: `internal/client/participants_test.go`

**Interfaces:**
- Consumes: `c.doOCS(ctx, method, p string, query url.Values, body io.Reader, mutate bool, out any) (http.Header, error)` — существующий транспорт (`internal/client/client.go:80`); константа `pathRooms = "/ocs/v2.php/apps/spreed/api/v4/room"` (`internal/client/paths.go`); хелперы тестов `testCfg`/`wantAuth`/`newOCSServer` из `client_test.go`/`ocs_test.go`.
- Produces (Task 2 завязан на эти имена): тип `client.Participant` с json-тегами `actorType, actorId, displayName, participantType, sessionIds, inCall, lastPing`; метод `func (c *TalkClient) GetParticipants(ctx context.Context, token string) ([]Participant, error)` — по ошибке `*client.OCSError`, по пустоте — non-nil пустой слайс.

- [ ] **Step 1: Создать фикстуру `testdata/room_participants.json`** — реальный формат эндпоинта (обезличено; поля и их набор — по живому ответу 2026-09-09, спека §3; все 14 наблюдаемых полей присутствуют, клиент декодирует только 7). Участники: владелец (PT=1, офлайн), модератор (PT=2, 2 сессии), участник (PT=3, 1 сессия — онлайн). Гостей в «реальном формате» нет (живая выборка их не содержала):

```json
{
  "ocs": {
    "meta": {
      "status": "ok",
      "statuscode": 200,
      "message": "OK"
    },
    "data": [
      {
        "roomToken": "tok-team",
        "inCall": 0,
        "lastPing": 1757401100,
        "sessionIds": [],
        "participantType": 1,
        "attendeeId": 101,
        "actorType": "users",
        "actorId": "anna.s",
        "displayName": "Анна Смирнова",
        "permissions": 251,
        "attendeePermissions": 0,
        "attendeePin": "",
        "phoneNumber": "",
        "callId": ""
      },
      {
        "roomToken": "tok-team",
        "inCall": 0,
        "lastPing": 1757401300,
        "sessionIds": ["sess-2a", "sess-2b"],
        "participantType": 2,
        "attendeeId": 102,
        "actorType": "users",
        "actorId": "boris.k",
        "displayName": "Борис Крылов",
        "permissions": 0,
        "attendeePermissions": 0,
        "attendeePin": "",
        "phoneNumber": "",
        "callId": ""
      },
      {
        "roomToken": "tok-team",
        "inCall": 0,
        "lastPing": 1757401200,
        "sessionIds": ["sess-1a"],
        "participantType": 3,
        "attendeeId": 103,
        "actorType": "users",
        "actorId": "vera.t",
        "displayName": "Вера Тимофеева",
        "permissions": 0,
        "attendeePermissions": 0,
        "attendeePin": "",
        "phoneNumber": "",
        "callId": ""
      }
    ]
  }
}
```

- [ ] **Step 2: Создать фикстуру `testdata/room_participants_synthetic_guest.json`** — SYNTHETIC (guest-формат — assumption спеки §4, живьем на этом эндпоинте не сверен; при первом живом прогоне комнаты с гостем — заменить обезличенным реальным ответом):

```json
{
  "ocs": {
    "meta": {
      "status": "ok",
      "statuscode": 200,
      "message": "OK"
    },
    "data": [
      {
        "roomToken": "tok-team",
        "inCall": 0,
        "lastPing": 1757401100,
        "sessionIds": [],
        "participantType": 1,
        "attendeeId": 101,
        "actorType": "users",
        "actorId": "anna.s",
        "displayName": "Анна Смирнова",
        "permissions": 251,
        "attendeePermissions": 0,
        "attendeePin": "",
        "phoneNumber": "",
        "callId": ""
      },
      {
        "roomToken": "tok-team",
        "inCall": 0,
        "lastPing": 1757401900,
        "sessionIds": ["sess-g1"],
        "participantType": 4,
        "attendeeId": 104,
        "actorType": "guests",
        "actorId": "guest::anon-1",
        "displayName": "",
        "permissions": 0,
        "attendeePermissions": 0,
        "attendeePin": "",
        "phoneNumber": "",
        "callId": ""
      }
    ]
  }
}
```

- [ ] **Step 3: Написать падающие тесты** — новый файл `internal/client/participants_test.go` целиком:

```go
package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// participantsTestServer поднимает httptest-сервер, отдавая содержимое
// указанной фикстуры на любой запрос (по образцу reactionsTestServer).
// Фикстуры лежат в корневом testdata/ — путь ../../testdata/<fixture>.
func participantsTestServer(t *testing.T, fixture string) *httptest.Server {
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

// TestGetParticipants — фикстура реального формата: декод всех семи
// канонических полей, метод/путь/заголовки запроса (спека §3–4).
func TestGetParticipants(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotOCS, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "room_participants.json"))
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
	ps, err := c.GetParticipants(context.Background(), "tok-team")
	if err != nil {
		t.Fatalf("GetParticipants: %v", err)
	}

	// Запрос: GET /v4/room/{token}/participants + OCS-заголовки (как ListRooms).
	if gotMethod != http.MethodGet {
		t.Errorf("method: got %q, want GET", gotMethod)
	}
	if want := "/ocs/v2.php/apps/spreed/api/v4/room/tok-team/participants"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
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

	// Декод: 3 участника фикстуры; первое — полное совпадение всех семи полей.
	if got, want := len(ps), 3; got != want {
		t.Fatalf("len(ps): got %d, want %d", got, want)
	}
	first := ps[0]
	if first.ActorType != "users" || first.ActorId != "anna.s" ||
		first.DisplayName != "Анна Смирнова" || first.ParticipantType != 1 ||
		first.InCall != 0 || first.LastPing != 1757401100 {
		t.Errorf("ps[0]: got %+v, want все семь полей anna.s (владелец)", first)
	}
	if first.SessionIds == nil || len(first.SessionIds) != 0 {
		t.Errorf("ps[0].SessionIds: got %v, want non-nil пустой ([] в фикстуре)", first.SessionIds)
	}
	// Второй — модератор с двумя сессиями, третий — участник онлайн.
	if ps[1].ParticipantType != 2 || len(ps[1].SessionIds) != 2 {
		t.Errorf("ps[1]: got ParticipantType=%d SessionIds=%v, want 2 и 2 сессии", ps[1].ParticipantType, ps[1].SessionIds)
	}
	if ps[2].ParticipantType != 3 || len(ps[2].SessionIds) != 1 {
		t.Errorf("ps[2]: got ParticipantType=%d SessionIds=%v, want 3 и 1 сессия", ps[2].ParticipantType, ps[2].SessionIds)
	}
}

// TestGetParticipants_PathEscape — token с пробелом эскейпится в path-сегменте
// (url.PathEscape): запрос уходит как /room/tok%20team/participants.
func TestGetParticipants_PathEscape(t *testing.T) {
	var gotEscaped string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscaped = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.GetParticipants(context.Background(), "tok team"); err != nil {
		t.Fatalf("GetParticipants: %v", err)
	}
	if want := "/room/tok%20team/participants"; !strings.HasSuffix(gotEscaped, want) {
		t.Errorf("escaped path: got %q, want suffix %q", gotEscaped, want)
	}
}

// TestGetParticipants_SessionIdsNull — "sessionIds": null у участника
// нормализуется в non-nil пустой слайс (детерминированный --json: [] не null).
func TestGetParticipants_SessionIdsNull(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[` +
		`{"actorType":"users","actorId":"bob","displayName":"Bob","participantType":3,` +
		`"sessionIds":null,"inCall":0,"lastPing":1757400000}]}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	ps, err := c.GetParticipants(context.Background(), "tok-team")
	if err != nil {
		t.Fatalf("GetParticipants: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("len(ps): got %d, want 1", len(ps))
	}
	if ps[0].SessionIds == nil {
		t.Fatal("ps[0].SessionIds: got nil, want non-nil пустой слайс (нормализация)")
	}
	if len(ps[0].SessionIds) != 0 {
		t.Errorf("ps[0].SessionIds: got %v, want пустой", ps[0].SessionIds)
	}
}

// TestGetParticipants_EmptyData — data:[] И data:null → пустой non-nil слайс,
// nil error (спека §4). null проходит guard len(data)>0 и обнуляет слайс —
// прецедент nil-map у GetReactions; guard ниже переинициализирует.
func TestGetParticipants_EmptyData(t *testing.T) {
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
		ps, err := c.GetParticipants(context.Background(), "tok-team")
		if err != nil {
			t.Fatalf("data %q: got err %v, want nil (пусто — НЕ ошибка)", body, err)
		}
		if ps == nil {
			t.Fatalf("data %q: got nil слайс, want non-nil пустой", body)
		}
		if len(ps) != 0 {
			t.Errorf("data %q: len(ps) = %d, want 0", body, len(ps))
		}
		ts.Close()
	}
}

// TestGetParticipants_NotFound — OCS 404 → *client.OCSError{Code:404}
// (cli-слой маппит в exit 2).
func TestGetParticipants_NotFound(t *testing.T) {
	ts := newOCSServer(t, 404, "room not found")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	_, err := c.GetParticipants(context.Background(), "no-such")
	if err == nil {
		t.Fatal("GetParticipants: got nil err, want *OCSError{Code:404}")
	}
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != 404 {
		t.Fatalf("err: got %v, want *OCSError{Code:404}", err)
	}
}

// TestGetParticipants_SyntheticGuest — SYNTHETIC-фикстура (guest-формат —
// assumption спеки §4, живьем не сверен): participantType=4, пустой
// displayName, actorId "guest::<anon-id>", sessionIds непустой (онлайн).
func TestGetParticipants_SyntheticGuest(t *testing.T) {
	ts := participantsTestServer(t, "room_participants_synthetic_guest.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	ps, err := c.GetParticipants(context.Background(), "tok-team")
	if err != nil {
		t.Fatalf("GetParticipants: %v", err)
	}
	if got, want := len(ps), 2; got != want {
		t.Fatalf("len(ps): got %d, want %d", got, want)
	}
	g := ps[1]
	if g.ActorType != "guests" || g.ActorId != "guest::anon-1" || g.DisplayName != "" ||
		g.ParticipantType != 4 || len(g.SessionIds) != 1 {
		t.Errorf("ps[1] (гость): got %+v, want guests/guest::anon-1/пустое имя/PT=4/1 сессия", g)
	}
}
```

- [ ] **Step 4: Прогнать тесты, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run TestGetParticipants -v`
Expected: FAIL (compile error: `undefined: c.GetParticipants` / `ps undefined` — метод ещё не существует).

- [ ] **Step 5: Реализовать `internal/client/participants.go`** — новый файл целиком:

```go
package client

import (
	"context"
	"net/http"
	"net/url"
)

// participants.go — участники комнаты (спека-дельта 2026-09-09 §3–4):
// GET /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants.
// Своя группа эндпоинта — по аналогии с reactions.go; в rooms.go НЕ добавлять.

// Participant — каноническое представление участника комнаты (спека-дельта
// §4). Из ответа декодируются только эти семь полей; остальные наблюдаемые
// поля эндпоинта (roomToken, attendeeId, permissions, attendeePermissions,
// attendeePin, phoneNumber, callId) игнорируются — тонкий клиент отдаёт
// каноническую модель (как Room), сырой ответ НЕ проксируется.
type Participant struct {
	ActorType       string   `json:"actorType"`       // "users" / "guests" / "emails" / ...
	ActorId         string   `json:"actorId"`         // для guests — "guest::<anon-id>" (assumption, спека §4)
	DisplayName     string   `json:"displayName"`     // может быть пустым (гость без имени)
	ParticipantType int      `json:"participantType"` // 1–6, см. спека §3; неизвестное — само число в выводе
	SessionIds      []string `json:"sessionIds"`      // непустой = есть живая сессия (онлайн)
	InCall          int      `json:"inCall"`          // 0 = не в звонке
	LastPing        int64    `json:"lastPing"`        // СЕКУНДЫ Unix, 0 = никогда
}

// GetParticipants возвращает список участников комнаты token (спека-дельта
// §3–4). Read-only GET без guard-ов до сети — как GetReactions (непустоту
// token гарантирует cli.ResolveRoom).
//
// Запрос: GET pathRooms + "/" + url.PathEscape(token) + "/participants".
// Разбор: doOCS → []Participant.
//
// Нормализация после decode (контракт детерминированного --json):
//   - ocs.data:null — json.Unmarshal обнуляет слайс в nil («null» проходит
//     guard len(data)>0, т.к. RawMessage("null") имеет len 4) — прецедент
//     nil-map у GetReactions; переинициализируем в пустой non-nil слайс
//     (data:[] уже декодируется в non-nil, guard идемпотентен);
//   - SessionIds == null у отдельного участника → пустой слайс, не nil
//     (иначе --json напечатал бы "sessionIds": null).
//
// Пустой ответ сервера (data:[] ИЛИ data:null) → пустой (non-nil) слайс,
// nil error. OCS-ошибка — стандартная обработка doOCS → *OCSError
// (404 → exit 2 в cli).
func (c *TalkClient) GetParticipants(ctx context.Context, token string) ([]Participant, error) {
	// PathEscape на token — защита от специальных символов в path-сегменте
	// (пробел → %20 и т.п.), единообразно с GetReactions.
	p := pathRooms + "/" + url.PathEscape(token) + "/participants"

	var out []Participant
	if _, err := c.doOCS(ctx, http.MethodGet, p, nil, nil, false, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Participant{}
	}
	for i := range out {
		if out[i].SessionIds == nil {
			out[i].SessionIds = []string{}
		}
	}
	return out, nil
}
```

- [ ] **Step 6: Прогнать тесты пакета + vet, убедиться в зелёном**

Run: `CGO_ENABLED=0 go test ./internal/client/ -run TestGetParticipants -v && CGO_ENABLED=0 go test ./internal/client/ && CGO_ENABLED=0 go vet ./internal/client/`
Expected: все PASS, vet чист.

- [ ] **Step 7: Commit**

```sh
git add internal/client/participants.go internal/client/participants_test.go testdata/room_participants.json testdata/room_participants_synthetic_guest.json
git commit -m "feat(client): GetParticipants — участники комнаты (GET /v4/room/{token}/participants)

Каноническая модель Participant (7 полей), нормализация data:null и
sessionIds:null в non-nil пустые значения. Фикстуры: реальный формат
(обезличенный живой ответ 2026-09-09) + синтетический гость (assumption
спеки, живьем не сверен). Спека-дельта 2026-09-09 §3-4."
```

---

### Task 2: cli — render-функции, `roomsParticipantsHandler`, метод в `TalkClient`, моки, handler-тесты

**Files:**
- Modify: `internal/cli/cli.go` (интерфейс `TalkClient`, строки 16–28; комментарий-счётчик)
- Modify: `internal/cli/handlers_rooms.go` (новый handler в конец файла, после `roomsSearchHandler`)
- Modify: `internal/render/render.go` (`ParticipantsTable` + мапа ролей в конец файла)
- Modify: `internal/render/render_json.go` (`ParticipantsJSON` в конец файла)
- Modify (стабы по одному методу): `internal/cli/cli_test.go` (`mockTalkClient`), `internal/cli/handlers_chat_test.go` (`chatSpyClient`), `internal/cli/handlers_reactions_test.go` (`reactionsSpyClient`), `internal/cli/handlers_search_test.go` (`searchSpyClient`), `internal/cli/room_test.go` (`roomMockClient`)
- Modify: `internal/cli/handlers_rooms_test.go` (spy-поля `roomsSpyClient` + новые тесты в конец)

**Interfaces:**
- Consumes: `client.Participant`/`client.GetParticipants` (Task 1); `ResolveRoom(ctx, client, positional, nameFlag, stderr) (string, error)` (`internal/cli/room.go`) — коды 1/2/3 сохраняются; `exitFromClientErr(err)`; `render`-паттерны `newTabwriter`/`writeJSON`.
- Produces (Task 3 завязан): `roomsParticipantsHandler(ctx, deps, args, jsonOut) ExitError` — имя для `routes["rooms"]["participants"]`; `render.ParticipantsTable(w io.Writer, ps []client.Participant) error`; `render.ParticipantsJSON(w io.Writer, ps []client.Participant) error`.

- [ ] **Step 1: Написать падающие тесты** — дописать в конец `internal/cli/handlers_rooms_test.go`. Сначала расширить spy (поля — в struct `roomsSpyClient`, метод — рядом с остальными):

```go
// Spy-поля для rooms participants (спека-дельта 2026-09-09 §6):
// GetParticipants записывает token каждого вызова и возвращает
// преднастроенный результат/ошибку.
type participantsCall struct{ token string }
```

(в struct `roomsSpyClient` добавить поля):

```go
	participantsCalls  []participantsCall
	participantsResult []client.Participant
	participantsErr    error
```

(метод `roomsSpyClient` — вместо простого стаба errMock, чтобы новые тесты видели вызовы):

```go
func (m *roomsSpyClient) GetParticipants(_ context.Context, token string) ([]client.Participant, error) {
	m.participantsCalls = append(m.participantsCalls, participantsCall{token: token})
	return m.participantsResult, m.participantsErr
}
```

Затем тесты — в конец файла:

```go
// -----------------------------------------------------------------------------
// rooms participants (спека-дельта 2026-09-09)
// -----------------------------------------------------------------------------

// participantsFixture — перемешанный серверный порядок: покрывает сортировку
// роль→имя, fallback имени (пустой displayName → actorId), все тексты ролей
// включая неизвестное число (9), онлайн да/нет, гостя с пустым именем.
func participantsFixture() []client.Participant {
	return []client.Participant{
		{ActorType: "users", ActorId: "vera.t", DisplayName: "Вера Тимофеева", ParticipantType: 3, SessionIds: []string{"sess-1"}, LastPing: 1757401200},
		{ActorType: "users", ActorId: "anna.s", DisplayName: "Анна Смирнова", ParticipantType: 1, SessionIds: []string{}, LastPing: 1757401100},
		{ActorType: "users", ActorId: "boris.k", DisplayName: "Борис Крылов", ParticipantType: 2, SessionIds: []string{"sess-2", "sess-3"}, LastPing: 1757401300},
		{ActorType: "guests", ActorId: "guest::anon-9", DisplayName: "", ParticipantType: 3, SessionIds: nil},
		{ActorType: "guests", ActorId: "guest::anon-1", DisplayName: "", ParticipantType: 4, SessionIds: nil},
		{ActorType: "users", ActorId: "eva.l", DisplayName: "Ева Ссылкина", ParticipantType: 6, SessionIds: nil},
		{ActorType: "users", ActorId: "zed.u", DisplayName: "Зиновий", ParticipantType: 9, SessionIds: nil},
	}
}

// TestRoomsParticipantsHandlerText — текстовый вывод: заголовок капсом,
// сортировка роль→имя (fallback имени ДО сортировки — латиница раньше
// кириллицы: guest::anon-9 перед Верой в одной роли), тексты ролей
// 1–6 и неизвестного числа, онлайн да/нет.
func TestRoomsParticipantsHandlerText(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-abc"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-abc" {
		t.Fatalf("GetParticipants calls: got %+v, want 1 вызов с tok-abc", spy.participantsCalls)
	}

	out := deps.Stdout.(*bytes.Buffer).String()
	lines := strings.Split(out, "\n")
	if len(lines) != 9 { // заголовок + 7 строк + хвост после финального \n
		t.Fatalf("строк вывода: got %d, want 9\nвывод=\n%s", len(lines), out)
	}
	// Заголовок капсом, 4 колонки.
	for _, h := range []string{"ИМЯ", "РОЛЬ", "ОНЛАЙН", "ID"} {
		if !strings.Contains(lines[0], h) {
			t.Errorf("заголовок не содержит %q\nвывод=\n%s", h, out)
		}
	}
	// Ожидаемый порядок строк (роль asc → итоговое имя case-insensitive asc):
	// Анна(1) → Борис(2) → guest::anon-9(3, латиница) → Вера(3) →
	// guest::anon-1(4, гость без имени) → Ева(6) → Зиновий(9 → печатается «9»).
	wantRows := []struct{ name, role, online, id string }{
		{"Анна Смирнова", "владелец", "нет", "anna.s"},
		{"Борис Крылов", "модератор", "да", "boris.k"},
		{"guest::anon-9", "участник", "нет", "guest::anon-9"},
		{"Вера Тимофеева", "участник", "да", "vera.t"},
		{"guest::anon-1", "гость", "нет", "guest::anon-1"},
		{"Ева Ссылкина", "гость-модератор", "нет", "eva.l"},
		{"Зиновий", "9", "нет", "zed.u"},
	}
	for i, w := range wantRows {
		line := lines[i+1]
		for _, part := range []string{w.name, w.role, w.online, w.id} {
			if !strings.Contains(line, part) {
				t.Errorf("строка %d: не содержит %q\nстрока=%q\nвывод=\n%s", i+1, part, line, out)
			}
		}
	}
}

// TestRoomsParticipantsHandlerRole5 — значение participantType=5 из
// документации (спека §3): текст «по ссылке».
func TestRoomsParticipantsHandlerRole5(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: []client.Participant{
		{ActorType: "users", ActorId: "link.u", DisplayName: "Линк", ParticipantType: 5, SessionIds: nil},
	}}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if out := deps.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "по ссылке") {
		t.Fatalf("stdout: должен содержать роль \"по ссылке\"; got %q", out)
	}
}

// TestRoomsParticipantsHandlerJSON — --json: отсортированный массив
// канонических Participant; sessionIds печатается как [], не null.
func TestRoomsParticipantsHandlerJSON(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: []client.Participant{
		{ActorType: "users", ActorId: "u-b", DisplayName: "Борис", ParticipantType: 2, SessionIds: []string{}},
		{ActorType: "users", ActorId: "u-a", DisplayName: "Анна", ParticipantType: 1, SessionIds: []string{"s1"}},
	}}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	raw := deps.Stdout.(*bytes.Buffer).String()
	var got []client.Participant
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("stdout не валидный JSON: %v; raw=%s", err, raw)
	}
	// Сортировка применена и к --json: владелец (1) первым.
	if len(got) != 2 || got[0].ActorId != "u-a" || got[0].ParticipantType != 1 {
		t.Fatalf("JSON порядок: got %+v, want u-a (PT=1) первым", got)
	}
	// sessionIds офлайн-участника — [] (не null).
	if !strings.Contains(raw, `"sessionIds": []`) {
		t.Errorf("JSON: ожидается \"sessionIds\": [] (не null); raw=%s", raw)
	}
	if strings.Contains(raw, `"sessionIds": null`) {
		t.Errorf("JSON: sessionIds не должен быть null; raw=%s", raw)
	}
}

// TestRoomsParticipantsHandlerEmpty — пустой список → пустой stdout, exit 0
// (поисковая семантика rooms list; одинаково для текста и --json).
func TestRoomsParticipantsHandlerEmpty(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		spy := &roomsSpyClient{participantsResult: []client.Participant{}}
		deps := newRoomsDeps(spy)

		ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, jsonOut)
		if ee.Code != ExitOK {
			t.Fatalf("jsonOut=%v: code: got %d, want %d", jsonOut, ee.Code, ExitOK)
		}
		if buf := deps.Stdout.(*bytes.Buffer); buf.Len() != 0 {
			t.Fatalf("jsonOut=%v: stdout: got %q, want пусто", jsonOut, buf.String())
		}
	}
}

// TestRoomsParticipantsHandlerNameResolution — --name: 1 совпадение →
// exit 0 и GetParticipants с резолвнутым token; >1 → exit 3 (кандидаты в
// stderr, GetParticipants не звался); 0 → exit 2 (тоже без вызова).
func TestRoomsParticipantsHandlerNameResolution(t *testing.T) {
	cases := []struct {
		name         string
		findResult   []client.Room
		wantExit     int
		wantToken    string // ожидаемый token GetParticipants ("" — вызова не было)
		wantCalls    int
	}{
		{
			name:       "--name 1 совпадение → 0",
			findResult: []client.Room{{Type: 2, Token: "tok-1", DisplayName: "Команда"}},
			wantExit:   ExitOK,
			wantToken:  "tok-1",
			wantCalls:  1,
		},
		{
			name: "--name >1 совпадений → 3, GetParticipants не звался",
			findResult: []client.Room{
				{Type: 2, Token: "tokA", DisplayName: "Команда X"},
				{Type: 2, Token: "tokB", DisplayName: "Команда Y"},
			},
			wantExit:  ExitAmbiguous,
			wantCalls: 0,
		},
		{
			name:       "--name 0 совпадений → 2, GetParticipants не звался",
			findResult: []client.Room{},
			wantExit:   ExitNotFound,
			wantCalls:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &roomsSpyClient{findResult: tc.findResult, participantsResult: participantsFixture()}
			deps := newRoomsDeps(spy)

			ee := roomsParticipantsHandler(context.Background(), deps, []string{"--name", "Команда"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantExit, ee.Err)
			}
			if len(spy.participantsCalls) != tc.wantCalls {
				t.Fatalf("GetParticipants вызовов: got %d, want %d", len(spy.participantsCalls), tc.wantCalls)
			}
			if tc.wantCalls == 1 && spy.participantsCalls[0].token != tc.wantToken {
				t.Fatalf("GetParticipants token: got %q, want %q", spy.participantsCalls[0].token, tc.wantToken)
			}
		})
	}
	// При неоднозначности кандидаты печатаются в stderr (render.Candidates).
	spy := &roomsSpyClient{findResult: []client.Room{
		{Type: 2, Token: "tokA", DisplayName: "Команда X"},
		{Type: 2, Token: "tokB", DisplayName: "Команда Y"},
	}}
	deps := newRoomsDeps(spy)
	roomsParticipantsHandler(context.Background(), deps, []string{"--name", "Команда"}, false)
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "tokA") {
		t.Errorf("stderr при exit 3: должен содержать кандидата tokA; got %q", stderr)
	}
}

// TestRoomsParticipantsHandlerClientError — OCS 404 → exit 2; OCS 403 →
// exit 1 с текстом сервера (спека §2, базовый контракт §7).
func TestRoomsParticipantsHandlerClientError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantExit int
		wantText string
	}{
		{"OCS 404 → 2", &client.OCSError{Code: http.StatusNotFound, Message: "room not found"}, ExitNotFound, "room not found"},
		{"OCS 403 → 1 с текстом сервера", &client.OCSError{Code: http.StatusForbidden, Message: "нет доступа к комнате"}, ExitGeneric, "нет доступа к комнате"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &roomsSpyClient{participantsErr: tc.err}
			deps := newRoomsDeps(spy)

			ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d", ee.Code, tc.wantExit)
			}
			if ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.wantText) {
				t.Fatalf("err: got %v, want содержит %q", ee.Err, tc.wantText)
			}
		})
	}
}

// TestRoomsParticipantsHandlerUnknownFlag — неизвестный --flag → exit 1,
// клиент не звался.
func TestRoomsParticipantsHandlerUnknownFlag(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok", "--no-such"}, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if len(spy.participantsCalls) != 0 {
		t.Fatalf("GetParticipants не должен вызываться; got %d calls", len(spy.participantsCalls))
	}
}

// TestRoomsParticipantsHandlerConflict — positional + --name: приоритет у
// positional, --name игнорируется с предупреждением в stderr (поведение
// chat show через ResolveRoom), FindRooms не звался.
func TestRoomsParticipantsHandlerConflict(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-pos", "--name", "Команда"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-pos" {
		t.Fatalf("GetParticipants: got %+v, want 1 вызов с tok-pos (приоритет positional)", spy.participantsCalls)
	}
	if len(spy.findCalls) != 0 {
		t.Fatalf("FindRooms не должен вызываться при positional; got %d calls", len(spy.findCalls))
	}
	if stderr := deps.Stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, "приоритет у token") {
		t.Errorf("stderr: должен содержать предупреждение о конфликте; got %q", stderr)
	}
}

// TestRoomsParticipantsHandlerExtraPositionals — лишние позиционные (второй
// и далее) игнорируются — единообразно с остальными командами.
func TestRoomsParticipantsHandlerExtraPositionals(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, []string{"tok-main", "extra", "more"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "tok-main" {
		t.Fatalf("GetParticipants: got %+v, want 1 вызов с tok-main", spy.participantsCalls)
	}
}

// TestRoomsParticipantsHandlerEmptyInput — нет ни <room>, ни --name →
// exit 1 «укажите token позиционно или --name» (ResolveRoom StatusEmptyInput),
// без единого вызова клиента.
func TestRoomsParticipantsHandlerEmptyInput(t *testing.T) {
	spy := &roomsSpyClient{}
	deps := newRoomsDeps(spy)

	ee := roomsParticipantsHandler(context.Background(), deps, nil, false)
	if ee.Code != ExitGeneric {
		t.Fatalf("code: got %d, want %d", ee.Code, ExitGeneric)
	}
	if ee.Err == nil || !strings.Contains(ee.Err.Error(), "укажите token позиционно или --name") {
		t.Fatalf("err: got %v, want «укажите token позиционно или --name»", ee.Err)
	}
	if len(spy.findCalls) != 0 || len(spy.participantsCalls) != 0 {
		t.Fatalf("клиент не должен зваться: find=%d participants=%d", len(spy.findCalls), len(spy.participantsCalls))
	}
}
```

Внимание: в `handlers_rooms_test.go` после этого нужен импорт `"net/http"` (для `http.StatusNotFound`/`http.StatusForbidden`) — добавить в блок импортов файла.

- [ ] **Step 2: Прогнать, убедиться в падении**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestRoomsParticipants -v`
Expected: FAIL на компиляции — единственная ошибка `undefined: roomsParticipantsHandler` (поля и метод spy уже добавлены Step 1, `client.Participant` существует с Task 1). Это красная фаза.

- [ ] **Step 3: Добавить `GetParticipants` в интерфейс `TalkClient` и моки** — без этого шага пакет `cli` не соберётся с тестами (спека §6).

`internal/cli/cli.go` — в интерфейсе после `GetReactions` (строка 26) добавить строку, и одновременно поправить комментарий-счётчик над интерфейсом (строка 16: «8 методов» → «9 методов», правка из §5 спеки — по месту):

```go
// TalkClient — минимальный интерфейс, покрывающий 9 методов реального
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
	SearchMessages(ctx context.Context, term string, opts client.SearchMessagesOpts) ([]client.MessageResult, error)
}
```

Compile-time проверка `var _ TalkClient = (*client.TalkClient)(nil)` (строка 32) остаётся как есть — она и ловит рассинхрон.

Стаб `errMock` — в пять остальных моков (у `roomsSpyClient` — полноценный spy из Step 1). В каждый файл добавить по одному методу рядом с `GetReactions`:

`internal/cli/cli_test.go` (`mockTalkClient`):

```go
func (m *mockTalkClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
```

`internal/cli/handlers_chat_test.go` (`chatSpyClient`):

```go
func (m *chatSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
```

`internal/cli/handlers_reactions_test.go` (`reactionsSpyClient`):

```go
func (m *reactionsSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
```

`internal/cli/handlers_search_test.go` (`searchSpyClient`):

```go
func (m *searchSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
```

`internal/cli/room_test.go` (`roomMockClient`) — стаб errMock (по букве спеки §6 — errMock во все моки, кроме roomsSpyClient, ему spy; в этом файле `EditMessage` уже использует тот же паттерн):

```go
func (m *roomMockClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
```

- [ ] **Step 4: Реализовать render-функции**

`internal/render/render.go` — в конец файла:

```go
// participantRoleNames — тексты ролей по participantType (спека-дельта
// 2026-09-09 §2–3: 1–6; значения 4–6 — из официальной документации Talk,
// живой выборкой подтверждены только 1–3). Маппинг живёт в render — это
// представление, не модель.
var participantRoleNames = map[int]string{
	1: "владелец",
	2: "модератор",
	3: "участник",
	4: "гость",
	5: "по ссылке",
	6: "гость-модератор",
}

// participantRole — текст роли; неизвестное число (format-drift: новые
// значения сервера) печатается самим числом, чтобы не ломать вывод.
func participantRole(t int) string {
	if s, ok := participantRoleNames[t]; ok {
		return s
	}
	return strconv.Itoa(t)
}

// participantName — итоговое имя колонки ИМЯ: displayName, при пустом
// (гость без имени) — actorId (тот же fallback, что в reactions get).
func participantName(p client.Participant) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ActorId
}

// ParticipantsTable выводит участников табличным форматом (спека-дельта
// 2026-09-09 §2):
//
//	ИМЯ | РОЛЬ | ОНЛАЙН | ID
//
// Сортировку делает вызывающий (cli-слой) — здесь только представление.
// ОНЛАЙН: непустой sessionIds = есть живая сессия.
func ParticipantsTable(w io.Writer, ps []client.Participant) error {
	tw := newTabwriter(w)
	if _, err := fmt.Fprintln(tw, "ИМЯ\tРОЛЬ\tОНЛАЙН\tID"); err != nil {
		return err
	}
	for _, p := range ps {
		online := "нет"
		if len(p.SessionIds) > 0 {
			online = "да"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			participantName(p), participantRole(p.ParticipantType), online, p.ActorId); err != nil {
			return err
		}
	}
	return tw.Flush()
}
```

`internal/render/render_json.go` — в конец файла:

```go
// ParticipantsJSON выводит []Participant как JSON-массив (спека-дельта
// 2026-09-09 §2, --json). Сырой ответ API не проксируется — каноническая
// модель, json-теги структур client определяют имена полей.
func ParticipantsJSON(w io.Writer, ps []client.Participant) error {
	return writeJSON(w, ps)
}
```

- [ ] **Step 5: Реализовать `roomsParticipantsHandler`** — дописать в конец `internal/cli/handlers_rooms.go` (в док-комментарий файла в шапке добавить `rooms participants` в перечисление команд):

```go
// roomsParticipantsHandler — реализация `rooms participants <room>` (спека-
// дельта 2026-09-09 §2, §5). Read-only: без мутаций, без мутационных env-
// флагов.
//
// Порядок шагов (дизайн §5):
//  1. ручной scan флагов (--name, обе формы; неизвестный --* → exit 1);
//  2. первый позиционный = room, лишние игнорируются;
//  3. ResolveRoom — коды 1/2/3 сохраняются (оба пусты → exit 1
//     «укажите token позиционно или --name»; конфликт positional+--name →
//     предупреждение в stderr, приоритет у positional);
//  4. GetParticipants;
//  5. сортировка ParticipantType asc → итоговое имя (displayName, при
//     пустом — actorId) case-insensitive asc; SliceStable — равные
//     сохраняют порядок сервера;
//  6. вывод render.ParticipantsTable / render.ParticipantsJSON по jsonOut;
//     пустой список → пустой stdout, exit 0 (как rooms list).
func roomsParticipantsHandler(ctx context.Context, deps Deps, args []string, jsonOut bool) ExitError {
	// 1-2. Разбор флагов и позиционных: ручной scan (без flag-пакета, спека §3).
	var positionals []string
	var nameFlag string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name":
			if i+1 >= len(args) {
				return ExitError{Code: ExitGeneric, Err: errors.New("rooms participants: --name требует значение")}
			}
			nameFlag = args[i+1]
			i++ // пропускаем значение на следующей итерации
		case strings.HasPrefix(a, "--name="):
			nameFlag = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--"):
			return ExitError{Code: ExitGeneric, Err: fmt.Errorf("rooms participants: неизвестный флаг %q", a)}
		default:
			positionals = append(positionals, a)
		}
	}

	// Первый позиционный — room (token, primary); лишние игнорируются.
	var positional string
	if len(positionals) > 0 {
		positional = positionals[0]
	}

	// 3. Разрешение <room>. ResolveRoom сам печатает кандидаты в stderr при
	// неоднозначности и возвращает ExitAmbiguous/ExitNotFound/ExitGeneric
	// как ExitError — пробрасываем код.
	token, err := ResolveRoom(ctx, deps.Client, positional, nameFlag, deps.Stderr)
	if err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee
		}
		return ExitError{Code: ExitGeneric, Err: err}
	}

	// 4. Список участников (клиент возвращает non-nil слайс для «пусто»).
	ps, err := deps.Client.GetParticipants(ctx, token)
	if err != nil {
		// OCS 404 (комната не найдена) → exit 2; прочие (401/403/5xx/сеть) → 1.
		return exitFromClientErr(err)
	}

	// 5. Клиентская сортировка (прецедент — фильтры rooms list): роль asc →
	// итоговое имя asc. Fallback actorId применяется ДО сортировки, чтобы
	// безымянные гости не скучковались как пустая строка.
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].ParticipantType != ps[j].ParticipantType {
			return ps[i].ParticipantType < ps[j].ParticipantType
		}
		return strings.ToLower(participantSortName(ps[i])) < strings.ToLower(participantSortName(ps[j]))
	})

	// 6. Пустой список → пустой stdout, exit 0 (поисковая семантика; без
	// заголовка таблицы и без [] в --json — одинаково для обоих режимов).
	if len(ps) == 0 {
		return ExitError{Code: ExitOK}
	}
	if jsonOut {
		if err := render.ParticipantsJSON(deps.Stdout, ps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	} else {
		if err := render.ParticipantsTable(deps.Stdout, ps); err != nil {
			return ExitError{Code: ExitGeneric, Err: err}
		}
	}
	return ExitError{Code: ExitOK}
}

// participantSortName — ключ сортировки по имени: displayName, при пустом —
// actorId (дублирует render.participantName, но cli не должен зависеть от
// внутренностей render; ключ — та же логика, что в колонке ИМЯ).
func participantSortName(p client.Participant) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ActorId
}
```

В блок импортов `handlers_rooms.go` добавить `"sort"` (текущий набор: context, errors, fmt, strconv, strings, client, render).

- [ ] **Step 6: Прогнать тесты — зелёный, затем весь репозиторий**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestRoomsParticipants -v`
Expected: все PASS.

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: все пакеты PASS (help/анти-drift пока не знают о команде — она ещё не в routes/cmdSpecs, это Task 3), vet чист. Изоляция не тронута (`TestCmdNctalkDoesNotDependOnPion` в общем прогоне — PASS).

- [ ] **Step 7: Commit**

```sh
git add internal/cli/cli.go internal/cli/handlers_rooms.go internal/cli/handlers_rooms_test.go \
  internal/cli/cli_test.go internal/cli/handlers_chat_test.go internal/cli/handlers_reactions_test.go \
  internal/cli/handlers_search_test.go internal/cli/room_test.go \
  internal/render/render.go internal/render/render_json.go
git commit -m "feat(cli): roomsParticipantsHandler + ParticipantsTable/ParticipantsJSON

Сортировка роль->имя (fallback actorId до сортировки), маппинг ролей
1-6 + неизвестное число (render), пустой список -> пустой stdout exit 0.
GetParticipants в интерфейсе TalkClient, стабы во все 6 моков тестов.
Спека-дельта 2026-09-09 §5-6."
```

---

### Task 3: cli — роутинг + help-декларация + каноны анти-drift

**Files:**
- Modify: `internal/cli/cli_test.go` (кейсы в `TestRunRoutesAllStubs` и `TestRunRoutesToCorrectHandler`, комментарии-счётчики)
- Modify: `internal/cli/help_test.go` (`TestCmdSpecs_OrderMatchesHelpOrder`, `TestCmdSpecs_FlagsExactly`, `buildPositionalArgs`, комментарии-счётчики)
- Modify: `internal/cli/cli.go` (routes + подсказка verb в `Run`)
- Modify: `internal/cli/help.go` (запись в `cmdSpecs` после `rooms search`, комментарий-счётчик)
- Test: `internal/cli/handlers_rooms_test.go` (Run-level тест новой команды)

**Interfaces:**
- Consumes: `roomsParticipantsHandler` (Task 2); `installSpy(t, "rooms/participants")` — существующий хелпер `cli_test.go` (работает для любого `resource/verb` из `routes`).
- Produces: `routes["rooms"]["participants"]`; `cmdSpecs`-запись `["rooms","participants"]`; счётчики «9 команд/9 методов» по §5 спеки.

- [ ] **Step 1: Обновить канон-тесты (сначала красные)**

`internal/cli/help_test.go`:

1. `TestCmdSpecs_OrderMatchesHelpOrder` (строка 39) — want-список, вставить `"rooms participants"` после `"rooms search"`:

```go
	want := []string{
		"rooms list", "rooms find", "rooms search", "rooms participants",
		"chat show", "chat send", "chat edit",
		"reactions get",
		"search",
	}
```

(тест сверяет и длину, и порядок — пока записи в `cmdSpecs` нет, он красный).

2. `TestCmdSpecs_FlagsExactly` (строка 59) — в map `want` добавить ключ:

```go
		"rooms participants": {"--name"},
```

3. `buildPositionalArgs` (строка 572) — добавить кейс (иначе handler получает пустой позиционный → exit 1, и prong-и анти-drift'а вырождаются; спека §6):

```go
	case "rooms participants":
		return []string{"tok123"}
```

4. Комментарии-счётчики (§5 спеки): строка 12 `TestCmdSpecs_CoverAllRoutes` — «ровно те же 8 команд» → «ровно те же 9 команд»; строка 322 `TestHandleHelp_DetailedContent` — «для каждой из 8 команд» → «для каждой из 9 команд».

`internal/cli/cli_test.go`:

1. `TestRunRoutesAllStubs` (строка 61) — комментарий «все 8 команд» → «все 9 команд» и кейс в таблицу после `{"rooms search", ...}`:

```go
		{"rooms participants", []string{"rooms", "participants", "tok"}},
```

(mockTalkClient вернёт errMock → handler даст ExitGeneric=1 и текст в stderr — инвариант теста соблюдён.)

2. `TestRunRoutesToCorrectHandler` (строка 145) — кейс после `{"rooms/search", ...}`:

```go
		{"rooms/participants", []string{"rooms", "participants", "TOK123"}, []string{"TOK123"}, ""},
```

3. В конец `internal/cli/handlers_rooms_test.go` — Run-level e2e новой команды:

```go
// TestRoomsParticipantsViaRun — `rooms participants TOK123` через Run:
// роутинг, handler, сортировка и таблица (end-to-end без сети).
func TestRoomsParticipantsViaRun(t *testing.T) {
	spy := &roomsSpyClient{participantsResult: participantsFixture()}
	deps := newRoomsDeps(spy)

	code := Run([]string{"rooms", "participants", "TOK123"}, deps)
	if code != ExitOK {
		t.Fatalf("Run code: got %d, want %d", code, ExitOK)
	}
	if len(spy.participantsCalls) != 1 || spy.participantsCalls[0].token != "TOK123" {
		t.Fatalf("GetParticipants: got %+v, want 1 вызов с TOK123", spy.participantsCalls)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "ИМЯ") || !strings.Contains(out, "anna.s") {
		t.Fatalf("stdout: ожидается таблица участников; got %q", out)
	}
}
```

- [ ] **Step 2: Прогнать, убедиться в падении канонов**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestCmdSpecs_OrderMatchesHelpOrder|TestCmdSpecs_FlagsExactly|TestRunRoutesAllStubs|TestRunRoutesToCorrectHandler|TestRoomsParticipantsViaRun' -v`
Expected: FAIL — прогон роняет паника в `TestRoomsParticipantsViaRun` (nil-handler): restore у `installSpy` (кейс `rooms/participants` в `TestRunRoutesToCorrectHandler`) присваивает `routes["rooms"]["participants"] = nil`, не удалив ключ, и `Run` зовёт nil-функцию; паника завершает тест-бинарник, поэтому `TestCmdSpecs_*` (файл `help_test.go` идёт после `handlers_rooms_test.go`) не выполняются вовсе. При отдельном запуске красный только `TestCmdSpecs_OrderMatchesHelpOrder`: `cmdSpecs len: got 8, want 9` (в `cmdSpecs` сейчас 8 записей). `TestCmdSpecs_FlagsExactly` и оба Run-канона в красной фазе зелёные: FlagsExactly циклит по `cmdSpecs` (лишний ключ want-словаря не проверяется), AllStubs'у отсутствие роутинга даёт «неизвестный verb» → ровно exit 1 + непустой stderr, а ToCorrectHandler'у spy в routes ставит сам `installSpy`. Паника и nil-остаток уйдут в Step 3 (роутинг появится).

- [ ] **Step 3: Добавить роутинг, подсказку verb и декларацию**

`internal/cli/cli.go` — routes (строки 63–68), после `"search": roomsSearchHandler,`:

```go
var routes = map[string]map[string]handlerFn{
	"rooms": {
		"list":         roomsListHandler,
		"find":         roomsFindHandler,
		"search":       roomsSearchHandler,
		"participants": roomsParticipantsHandler,
	},
	"chat": {
		"show": chatShowHandler,
		"send": chatSendHandler,
		"edit": chatEditHandler,
	},
	"reactions": {
		"get": reactionsGetHandler,
	},
}
```

Подсказка в `Run` (строка 121) — дополнить список verb-ов (§5 спеки):

```go
		fmt.Fprintf(deps.Stderr, "nctalk %s: ожидается verb (list/find/search/participants/show/send/edit/get)\n", resource)
```

`internal/cli/help.go`:

1. Запись в `cmdSpecs` — сразу после блока `rooms search` (строки 71–79), перед `chat show` (порядок слайса = порядок общего help):

```go
	{
		Path:        []string{"rooms", "participants"},
		Short:       "участники комнаты",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
		},
		Examples: []string{
			"nctalk rooms participants abc123",
			"nctalk rooms participants --name \"Команда\"",
		},
	},
```

2. Комментарий-счётчик (строка 37, §5 спеки): «единый источник правды: 8 команд базового CLI» → «единый источник правды: 9 команд базового CLI».

- [ ] **Step 4: Прогнать help/anti-drift + весь cli-пакет**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v`
Expected: все PASS. `TestCmdSpecs_CoverAllRoutes`, `TestHandleHelp_DetailedContent`, `TestHandleHelp_ResourceContent`, `TestAntiDrift_HelpMatchesDeclaration`, `TestAntiDrift_HandlerMatchesDeclaration` подхватывают новую команду автоматически по декларации (§5 спеки) — убедиться, что они зелёные (если `TestAntiDrift_HandlerMatchesDeclaration` падает на prong «незаявленный флаг отвергает» — проверить, что handler действительно возвращает ошибку с текстом «неизвестный флаг», как в Step 5 Task 2).

Run: `CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./...`
Expected: всё PASS, vet чист.

- [ ] **Step 5: Ручная smoke-проверка help (верификация, не тест)**

```sh
CGO_ENABLED=0 go build -o /tmp/nctalk-smoke ./cmd/nctalk
/tmp/nctalk-smoke rooms participants --help
/tmp/nctalk-smoke --help
/tmp/nctalk-smoke rooms 2>&1 || true
```

Expected: детальный help показывает `nctalk rooms participants — участники комнаты`, `Использование: nctalk rooms participants <room> [flags]`, `<room> — token комнаты позиционно, либо --name...`, флаг `--name`, `--json`, оба примера; общий help содержит строку `rooms participants` после `rooms search`; подсказка без verb — `(list/find/search/participants/show/send/edit/get)`. Удалить /tmp/nctalk-smoke после проверки.

- [ ] **Step 6: Commit**

```sh
git add internal/cli/cli.go internal/cli/cli_test.go internal/cli/help.go internal/cli/help_test.go internal/cli/handlers_rooms_test.go
git commit -m "feat(cli): роутинг и help-декларация rooms participants + каноны

routes[rooms][participants], verb-подсказка, cmdSpecs после rooms search,
каноны Order/FlagsExactly/buildPositionalArgs и Run-level кейсы; счётчики
8->9. Спека-дельта 2026-09-09 §5-6."
```

---

### Task 4: Интеграционный тест `TestIntegration_GetParticipants` (read-only)

**Files:**
- Modify: `internal/client/integration_test.go` (новый тест после `TestIntegration_GetReactions`, перед `TestIntegration_SendMessage`)

**Interfaces:**
- Consumes: `integrationClient(t)`, `integrationTimeout` (существующие хелперы файла); `c.ListRooms`/`c.GetParticipants` (Task 1).
- Produces: `TestIntegration_GetParticipants` — read-only, БЕЗ мутационных env-флагов (по образцу `TestIntegration_GetChat`).

- [ ] **Step 1: Написать тест** — дописать в `internal/client/integration_test.go`:

```go
// TestIntegration_GetParticipants проверяет participants-эндпоинт (спека-
// дельта 2026-09-09 §3, §6). Read-only GET, мутационных флагов НЕ требует —
// как TestIntegration_GetChat. Берёт token первой комнаты из ListRooms;
// ожидания: >=1 участник (сам запрашивающий), у каждого непустые
// actorId/actorType, хотя бы один participantType=1 (владелец — инвариант
// комнаты). Логи — обезличенные первые строки для диагностики.
func TestIntegration_GetParticipants(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	rooms, err := c.ListRooms(ctx, ListRoomsOpts{})
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) == 0 {
		t.Skip("нет комнат — пропуск GetParticipants")
	}
	token := rooms[0].Token
	t.Logf("выбрана комната token=%s", token)

	ps, err := c.GetParticipants(ctx, token)
	if err != nil {
		t.Fatalf("GetParticipants(%s): %v", token, err)
	}
	if len(ps) < 1 {
		t.Fatalf("GetParticipants: 0 участников — ожидается >=1 (сам запрашивающий)")
	}
	t.Logf("получено участников: %d", len(ps))

	hasOwner := false
	for i, p := range ps {
		if p.ActorId == "" {
			t.Errorf("participant[%d]: ActorId пуст", i)
		}
		if p.ActorType == "" {
			t.Errorf("participant[%d]: ActorType пуст", i)
		}
		if p.ParticipantType == 1 {
			hasOwner = true
		}
		// Первые строки — диагностика (обезличенно: без лишних деталей).
		if i < 3 {
			t.Logf("participant[%d]: actorType=%s actorId=%s displayName=%q participantType=%d online=%v",
				i, p.ActorType, p.ActorId, p.DisplayName, p.ParticipantType, len(p.SessionIds) > 0)
		}
	}
	if !hasOwner {
		t.Errorf("ни одного участника с participantType=1 (владелец) — инвариант комнаты нарушен")
	}
}
```

- [ ] **Step 2: Компиляция integration-сьюта + отсутствие в обычном прогоне**

Run: `CGO_ENABLED=0 go test -tags=integration ./internal/client/... -run TestIntegration_GetParticipants -v` (без env-кредов — ожидаем SKIP «пропуск интеграционного теста: ...», это доказывает компиляцию и корректный gate)
Expected: `--- SKIP: TestIntegration_GetParticipants` (не FAIL).

Run: `CGO_ENABLED=0 go test ./...`
Expected: в выводе нет `TestIntegration_*` (build-тег работает).

- [ ] **Step 3: (Опционально; при наличии живых кредов) Живой прогон читающих сценариев**

```sh
NEXTCLOUD_URL=... NEXTCLOUD_LOGIN=... NEXTCLOUD_PASS=... \
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_GetParticipants'
```

Expected: PASS. Заодно сверить живой ответ с фикстурой `testdata/room_participants.json` (набор/имена полей). Если в комнате есть гость — сверить фактические поля гостя с assumption §4 (`actorId` "guest::<anon-id>", пустой `displayName`) и при расхождении заменить синтетическую фикстуру `room_participants_synthetic_guest.json` обезличенным реальным ответом (+ поправить ожидания `TestGetParticipants_SyntheticGuest`, убрать пометку synthetic из комментария — коммит отдельной строкой в этом же таске).

- [ ] **Step 4: Commit**

```sh
git add internal/client/integration_test.go
git commit -m "test(client): интеграционный TestIntegration_GetParticipants (read-only)

Token из ListRooms; >=1 участник, непустые actorId/actorType, есть
participantType=1. Без мутационных флагов. Спека-дельта 2026-09-09 §6."
```

---

### Task 5: Сопроводительная документация (§5 спеки)

**Files:**
- Modify: `README.md` (секция `rooms participants` после `rooms search`)
- Modify: `CLAUDE.md` (строка 5: «8 команд» → «9 команд»)
- Modify: `TODO.md` (удалить строку про участников)
- Modify: `docs/integration-run.md` (таблица тестов + batch-регулярка + per-test пример + список читающих)
- Modify (файл ВНЕ репозитория, НЕ коммитится): `~/.claude/skills/nctalk/SKILL.md`

**Interfaces:**
- Consumes: контракт команды из Task 1–3 (вывод, флаги, exit-коды).
- Produces: документация, синхронная с кодом; счётчики 8→9 полностью вычищены.

- [ ] **Step 1: README — секция после `rooms search`** (после строки «Через Unified Search (`talk-conversations`). Минимальная длина `term` — 1 символ.», перед `### chat show`), по образцу `rooms find`:

```markdown
### `rooms participants <room>` — участники комнаты

```sh
nctalk rooms participants kytxaiyc             # <room> = token позиционно
nctalk rooms participants --name "review team" # ...или по имени
nctalk rooms participants kytxaiyc --json      # каноническая модель (actorId и др.)
```

Колонки: `ИМЯ` (у гостя без имени — `actorId`), `РОЛЬ` (владелец/модератор/участник/гость/по ссылке/гость-модератор), `ОНЛАЙН` (есть живая сессия), `ID` (`actorId` — для упоминаний и `--from`). Сортировка: роль → имя. `--json` — массив объектов `actorType, actorId, displayName, participantType, sessionIds, inCall, lastPing`. Пусто → exit `0`.
```

Отдельного списка всех команд в README нет (только заголовки секций) — больше ничего перечислять не нужно; проверить: `grep -n "rooms search" README.md` — секция одна.

- [ ] **Step 2: CLAUDE.md** — строка 5, фразу «только примитивы (8 команд)» заменить на «только примитивы (9 команд)». Больше ничего про participants в CLAUDE.md не добавлять (поведенческие детали — в спеке).

- [ ] **Step 3: TODO.md** — удалить строку (задача закрыта этой дельтой):

```markdown
- Нет списка участников комнаты: присутствие человека приходится доказывать косвенно (system-сообщения `user_added`) — нужен `rooms participants <token>`.
```

- [ ] **Step 4: `docs/integration-run.md`** — четыре правки (иначе документированный прогон «все read-only одной командой» не включит новый тест; мутационная семантика не меняется — флагов не добавляется):

1. Batch-регулярка «Все читающие сценарии» — добавить `|GetParticipants`:

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v \
  -run 'TestIntegration_(ListRooms|SearchRooms|GetChat|SearchMessages|GetReactions|GetParticipants)'
```

2. Per-test примеры — добавить строку после `TestIntegration_GetReactions`:

```sh
CGO_ENABLED=0 go test -tags=integration ./internal/client/... -v -run TestIntegration_GetParticipants
```

3. Раздел «Безопасность сценариев» — в перечень читающих добавить `GetParticipants`:

```markdown
- **Читающие** (`ListRooms`, `SearchRooms`, `GetChat`, `SearchMessages`,
  `GetReactions`, `GetParticipants`) — GET-запросы, состояние сервера не меняют.
```

4. Таблица «Что проверяет каждый тест» — строка после `TestIntegration_GetReactions`:

```markdown
| `TestIntegration_GetParticipants` | `/ocs/v2.php/apps/spreed/api/v4/room/{token}/participants` | `>= 1` участник; непустые `actorId`/`actorType`; есть `participantType=1` (владелец). |
```

- [ ] **Step 5: SKILL.md — skill-обёртка `nctalk`** (файл `~/.claude/skills/nctalk/SKILL.md`, ВНЕ репозитория — не коммитить; заявленный потребитель — агент через skill, без правки skill не узнает о девятой команде). Три правки:

1. Строка 8 (первый абзац): «Тонкий CLI: 8 примитивов (комнаты, чтение, отправка, правка, реакции, поиск).» → «Тонкий CLI: 9 примитивов (комнаты, чтение, отправка, правка, реакции, поиск, участники).»

2. Правило 4 «Люди — по actorId» (строка 23) — дополнить список источников; итоговый текст правила:

```markdown
4. **Люди — по actorId** (userId, напр. `daiana.zhukotskaia`), не по displayName. Самый прямой источник участников комнаты: `rooms participants --json` (колонка/поле `actorId`). Прочие источники: `chat show --json`, `rooms list --json` (у личных чатов type=1 собеседник — в поле `name`; `actorId` там — всегда ты сам).
```

3. Шпаргалка (таблица, после строки `| rooms search <term> | ...`):

```markdown
| `rooms participants <room>` | участники комнаты | имя/роль/онлайн/ID; `--json` — самый прямой источник actorId |
```

- [ ] **Step 6: Проверить отсутствие оставшихся счётчиков «8»**

```sh
grep -rn "8 команд\|8 методов\|8 примитивов" --include="*.go" --include="*.md" . ~/.claude/skills/nctalk/SKILL.md | grep -v "_testdata\|docs/superpowers"
```

Expected: пусто (счётчики в коде обновлены в Task 2/3: cli.go, help.go, cli_test.go, help_test.go; CLAUDE.md и SKILL.md — в этом таске; строки в docs/superpowers — исторические спеки/планы, не трогать).

- [ ] **Step 7: Финальный полный прогон**

```sh
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...
```

Expected: всё зелёное. Плюс compile-проверка integration-сьюта: `CGO_ENABLED=0 go test -tags=integration ./internal/client/... -run TestIntegration_GetParticipants` → SKIP без кредов.

- [ ] **Step 8: Commit** (только файлы репозитория — SKILL.md вне git):

```sh
git add README.md CLAUDE.md TODO.md docs/integration-run.md
git commit -m "docs: rooms participants — README/CLAUDE/TODO/integration-run

README: секция команды; CLAUDE.md 8->9 команд; TODO — строка про
участников закрыта; integration-run: read-only тест в batch-регулярку,
per-test примеры и таблицу. Спека-дельта 2026-09-09 §5."
```

---

## Definition of Done (сводка проверок)

- `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...` — зелёные; integration-сьюта компилируется (`-tags=integration`), без тега в прогон не попадает.
- `nctalk rooms participants <token>` / `--name` / `--json` работают; `--help` показывает новую команду; exit-коды 0/1/2/3 по таблице спеки §2.
- Все канон-тесты (`TestCmdSpecs_*`, `TestRunRoutes*`, `TestAntiDrift_*`, `TestHandleHelp_*`) зелёные; счётчики «9 команд/9 методов» честные.
- README/CLAUDE.md/TODO.md/integration-run.md синхронны; `~/.claude/skills/nctalk/SKILL.md` обновлён (вне репо).
- Фикстуры в корневом `testdata/` обезличены; гостевая — synthetic с пометкой в имени, при первом живом прогоне с гостем сверена (Task 4 Step 3).
