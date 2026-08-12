# `--help` для `nctalk` — Implementation Plan

> **Для исполнителя:** план исполняется через `superpowers:subagent-driven-development` (recommended) или `superpowers:executing-plans`. Каждый шаг с чекбоксом `- [ ]` — отдельное действие (2–5 минут). Ссылки `§N` указывают на разделы спеки — открывай первоисточник: `docs/superpowers/specs/2026-08-12-nctalk-help-design.md`. Эталон контракта базового CLI: `docs/superpowers/specs/2026-07-17-nctalk-cli-design.md`. Не пересказывай спеку — ей следуй.

**Goal:** добавить полноценный `--help`/`-h`/`help` (и поведение при no-args) в базовый `nctalk` — без изменения парсинга флагов команд и без участия звонковых бинарников.

**Architecture:** перехват help — в `run()` (`cmd/nctalk/main.go`), **строго до** `config.Load()`; разбор help-форм и тексты живут в новом `internal/cli/help.go` (экспортируемая функция `HandleHelp(args, deps) (handled bool, exit int)`). Единый источник правды — `cmdSpecs`: декларация 7 команд и их флагов, по которой рендерятся help-тексты И сверяются anti-drift тесты обоих типов (help↔декларация, декларация↔handler). Порядок разбора в `run()`: вызвать `HandleHelp` → если `handled=true`, выйти с её кодом; иначе обычный flow (`config.Load` → `cli.Run`).

**Tech Stack:** Go 1.21+, только stdlib. CGO_ENABLED=0 обязательно (на этой машине `dyld: missing LC_UUID` иначе).

**Сборка и тесты:**
```sh
CGO_ENABLED=0 go build ./...                                                # все бинарники
CGO_ENABLED=0 go vet ./...                                                  # линт
CGO_ENABLED=0 go test ./...                                                 # все unit-тесты
CGO_ENABLED=0 go test ./internal/cli/... -run TestHelp -v                   # только help-тесты
CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion -v   # изоляция
```

## Глобальные ограничения (действуют на каждую задачу)

- Go 1.21+; **`CGO_ENABLED=0`** на всех сборках/тестах; stdlib только (без новых внешних зависимостей).
- Help работает **без env** (`NEXTCLOUD_URL`/`LOGIN`/`PASS` могут быть пустыми) — перехват строго до `config.Load()` (спека §5).
- Изменения только в `internal/cli` (новые `help.go` + `help_test.go`) и `cmd/nctalk` (`main.go` + правка ОДНОГО теста `TestRun_NoEnv_Returns1` в `main_test.go`). Звонковые бинарники (`cmd/nctalk-call`, `cmd/nctalk-talk`, `cmd/spike-check`) и `internal/call/*` НЕ трогаются.
- Инвариант изоляции: `cmd/nctalk` не должен зависеть от pion — guard `TestCmdNctalkDoesNotDependOnPion` остаётся зелёным (спека §5/§6).
- Существующие инварианты базового CLI (`2026-07-17-...-design.md` §6/§8: redirect-политика, redact кредов, пагинация `GetChat` через `X-Chat-Last-Given`, фильтры `chat show`, маппинг exit-кодов 0/1/2/3) — НЕ меняются.
- Парсинг флагов команд не меняется: `--help`/`-h` вырезаются из args до того, как args дойдут до handler-а (аналогично `extractJSON`).
- Кириллические комментарии и тексты вывода — сохраняются.
- Коммиты **без AI-атрибуции** (никакого `Co-Authored-By: Claude`).

---

## Риски и точки решения

| # | Риск | Где проверяется | Mitigation |
|---|---|---|---|
| R1 | Дрейф help-текста от реальных флагов handler-а (уже было в review спеки: `rooms find --include-former`, `rooms search --limit`) | Task 4 — anti-drift тесты обоих типов | Единая декларация `cmdSpecs` + две независимых проверки против неё |
| R2 | Сломать `TestRun_NoEnv_Returns1` или другие существующие e2e в `cmd/nctalk/main_test.go` | Task 5 — скорректировать тест; полный прогон `go test ./...` | Спека §5 явно оговаривает правку теста: подавать args, реально доходящие до `config.Load()` (например `["rooms","list"]`) |
| R3 | Случайно затянуть pion в `cmd/nctalk` через новый импорт | Task 5 — `TestCmdNctalkDoesNotDependOnPion` | Изменения только в `internal/cli` и `cmd/nctalk/main.go`; никаких импортов кроме `internal/cli` (уже есть) и stdlib |
| R4 | Сломать существующий роутинг (особенно special-case `search`) | Task 1 (cmdSpecs покрывает все 7 команд) + Task 5 (e2e `TestRun_RoomsList_E2E` и др. остаются зелёными) | HandleHelp вызывается ДО `cli.Run`; для не-help args `cli.Run` получает args без изменений |
| R5 | Несоответствие форм `--flag value` vs `--flag=value` при вычислении пути help | Task 2 — табличный тест `computePath` | Использовать `cmdSpecs` для различения value-флагов и boolean-флагов; unknown-флаг трактовать как boolean (безопасно: не съедает следующий токен) |

---

## Структура файлов

**Создать:**
- `internal/cli/help.go` — пакетная документация + `cmdSpecs` (декларация 7 команд) + `HandleHelp` + `isHelpRequest` + `computePath` + `renderGeneral`/`renderResource`/`renderDetailed`. Один файл = одна ответственность (help).
- `internal/cli/help_test.go` — все help-тесты: покрытие маршрутов, snapshot-подобные substring-проверки, exit-коды, потоки, no-creds, anti-drift обоих типов.

**Изменить:**
- `cmd/nctalk/main.go:34-48` — в `run()` добавить вызов `cli.HandleHelp` ДО `config.Load()`; для not-handled — обычный flow без правок.
- `cmd/nctalk/main_test.go:116-143` — скорректировать `TestRun_NoEnv_Returns1` (спека §5).

**Не трогать:**
- `internal/cli/cli.go`, `internal/cli/handlers_*.go`, `internal/cli/exit.go`, `internal/cli/room.go`, `internal/client/*`, `internal/render/*`, `internal/config/*`, `internal/transport/*`, `internal/room/*`, `internal/exit/*`.
- Звонковые бинарники и `internal/call/*`.

---

### Task 1: Декларация команд `cmdSpecs` + проверка покрытия маршрутов

**Цель:** создать единый источник правды для help-текстов и anti-drift проверок — декларацию всех 7 команд и их флагов, точно совпадающую с реальными handler-ами.

**Files:**
- Create: `internal/cli/help.go`
- Create: `internal/cli/help_test.go`

**Interfaces:**
- Produces:
  ```go
  // internal/cli/help.go
  package cli

  // flagSpec — описание одного флага в декларации команды.
  type flagSpec struct {
      Name  string // "--name" (с префиксом --)
      Value string // "<имя>", "N", "<путь>" — пусто для boolean-флагов
      Desc  string // короткое описание для help-текста
  }

  // cmdSpec — декларация одной команды для help и anti-drift.
  // Path = ["rooms","list"] для двухуровневых команд, ["search"] для leaf-команд.
  type cmdSpec struct {
      Path        []string   // канонический путь команды
      Short       string     // для общего help и списка команд ресурса
      UsageExtras string     // позиционные аргументы usage (например "<room>")
      Flags       []flagSpec // строго по handler-ам; --name включён там, где handler его парсит
      Examples    []string   // 1-2 примера для детального help
  }

  // cmdSpecs — единый источник правды: 7 команд базового CLI.
  // Порядок — как в общем help (спека §3).
  var cmdSpecs = []cmdSpec{ /* см. Step 3 */ }
  ```

- [ ] **Step 1: написать failing-тест покрытия в `internal/cli/help_test.go`**

```go
package cli

import (
	"reflect"
	"sort"
	"testing"
)

// TestCmdSpecs_CoverAllRoutes — cmdSpecs обязана содержать ровно те же 7 команд,
// что и таблица routes + searchHandlerFn (спека §6 anti-drift на уровне маршрутов).
// Защита от drift: если добавить команду в routes и забыть в cmdSpecs (или наоборот) — тест падает.
func TestCmdSpecs_CoverAllRoutes(t *testing.T) {
	// Собираем ожидаемое множество путей из routes + search.
	want := map[string]bool{}
	for resource, verbs := range routes {
		for verb := range verbs {
			want[resource+" "+verb] = true
		}
	}
	want["search"] = true // special-case

	got := map[string]bool{}
	for _, cs := range cmdSpecs {
		got[joinPath(cs.Path)] = true
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("cmdSpecs drift: want %v, got %v", want, got)
	}
}

// TestCmdSpecs_OrderMatchesHelpOrder — порядок cmdSpecs соответствует
// нумерации в общем help (спека §3): rooms list, rooms find, rooms search,
// chat show, chat send, reactions get, search. drift в порядке → тест падает.
func TestCmdSpecs_OrderMatchesHelpOrder(t *testing.T) {
	want := []string{
		"rooms list", "rooms find", "rooms search",
		"chat show", "chat send",
		"reactions get",
		"search",
	}
	if len(cmdSpecs) != len(want) {
		t.Fatalf("cmdSpecs len: got %d, want %d", len(cmdSpecs), len(want))
	}
	for i, w := range want {
		if joinPath(cmdSpecs[i].Path) != w {
			t.Errorf("cmdSpecs[%d]: got %q, want %q", i, joinPath(cmdSpecs[i].Path), w)
		}
	}
}

// TestCmdSpecs_FlagsExactly — заявленные флаги по каждой команде совпадают с
// каноном спеки §4 (источник: handlers_*.go). Любой drift (flag добавили в handler
// и забыли в декларацию, или наоборот) → тест падает ещё до render-тестов.
func TestCmdSpecs_FlagsExactly(t *testing.T) {
	want := map[string][]string{
		"rooms list":     {"--type", "--unread", "--include-former"},
		"rooms find":     {"--user"},
		"rooms search":   {},
		"chat show":      {"--name", "--last", "--from", "--since", "--system"},
		"chat send":      {"--name", "--reply-to", "--reference-id", "--silent", "--file"},
		"reactions get":  {"--name"},
		"search":         {"--from", "--limit", "--all"},
	}
	for i, cs := range cmdSpecs {
		path := joinPath(cs.Path)
		wantFlags, ok := want[path]
		if !ok {
			t.Fatalf("cmdSpecs[%d]: неизвестный путь %q (нет в want)", i, path)
		}
		gotFlags := make([]string, len(cs.Flags))
		for j, f := range cs.Flags {
			gotFlags[j] = f.Name
		}
		sort.Strings(wantFlags)
		sort.Strings(gotFlags)
		if !reflect.DeepEqual(wantFlags, gotFlags) {
			t.Errorf("cmdSpecs %s: flags drift: want %v, got %v", path, wantFlags, gotFlags)
		}
	}
}
```

- [ ] **Step 2: запустить тест, убедиться в ошибке компиляции**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestCmdSpecs -v`
Expected: FAIL — `cmdSpecs undefined` (файл `help.go` ещё пустой/отсутствует).

- [ ] **Step 3: создать `internal/cli/help.go` с декларацией `cmdSpecs`**

```go
// Package cli (файл help.go) — перехват --help/-h/`help` и тексты помощи.
//
// Декомпозиция: cmdSpecs (этот файл) — единый источник правды для имён команд
// и их флагов; по этой декларации рендерятся help-тексты (renderGeneral/
// renderResource/renderDetailed) И сверяются anti-drift тесты обоих типов
// (help↔декларация и декларация↔handler, спека §6).
//
// Перехват help-запроса идет в cmd/nctalk/main.go.run() ДО config.Load(),
// поэтому help работает без NEXTCLOUD_* (спека §5).
package cli

// flagSpec — описание одного флага в декларации команды.
// Поле Value пусто для boolean-флагов (--unread, --system, --all, ...).
type flagSpec struct {
	Name  string
	Value string
	Desc  string
}

// cmdSpec — декларация одной команды базового CLI для help и anti-drift.
// Path = ["rooms","list"] для двухуровневых команд; ["search"] для leaf-команд
// (special-case роутинга, не входит в таблицу routes).
type cmdSpec struct {
	Path        []string
	Short       string // для общего help и списка команд ресурса
	UsageExtras string // позиционные аргументы usage ("<room>", "<room> <messageId>")
	Flags       []flagSpec
	Examples    []string
}

// cmdSpecs — единый источник правды: 7 команд базового CLI.
// Порядок соответствует общему help (спека §3). Флаги — строго по handler-ам
// (handlers_rooms.go, handlers_chat.go, handlers_reactions.go, handlers_search.go);
// --name включён только в тех командах, где handler его парсит.
//
// ВНИЗУ: каждая правка флагов в handler-е обязана сопровождаться правкой здесь;
// regression-защита — TestCmdSpecs_FlagsExactly и TestAntiDrift_Handler (Task 4).
var cmdSpecs = []cmdSpec{
	{
		Path:        []string{"rooms", "list"},
		Short:       "список комнат",
		UsageExtras: "",
		Flags: []flagSpec{
			{Name: "--type", Value: "<1|2|3>", Desc: "фильтр по типу комнаты"},
			{Name: "--unread", Desc: "только комнаты с непрочитанными"},
			{Name: "--include-former", Desc: "показывать former-комнаты (типы 4/5/6)"},
		},
		Examples: []string{
			"nctalk rooms list --unread",
			"nctalk rooms list --type 2",
		},
	},
	{
		Path:        []string{"rooms", "find"},
		Short:       "найти комнату по фильтру",
		UsageExtras: "<запрос>",
		Flags: []flagSpec{
			{Name: "--user", Value: "<actorId>", Desc: "точный фильтр по actorId собеседника"},
		},
		Examples: []string{
			"nctalk rooms find \"команда\"",
			"nctalk rooms find \"\" --user bob",
		},
	},
	{
		Path:        []string{"rooms", "search"},
		Short:       "поиск комнат на сервере",
		UsageExtras: "<term>",
		Flags:       nil, // без флагов; handler отвергает любое --* (limit фиксируется в 0)
		Examples: []string{
			"nctalk rooms search foo",
		},
	},
	{
		Path:        []string{"chat", "show"},
		Short:       "история чата комнаты",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--last", Value: "N", Desc: "количество сообщений (по умолчанию 20)"},
			{Name: "--from", Value: "<actorId>", Desc: "только сообщения автора"},
			{Name: "--since", Value: "<время>", Desc: "с момента времени (1h, 2d или ISO)"},
			{Name: "--system", Desc: "показывать system-сообщения (скрыты по умолчанию)"},
		},
		Examples: []string{
			"nctalk chat show abc123 --last 50",
			"nctalk chat show --name \"Команда\" --since 1h",
		},
	},
	{
		Path:        []string{"chat", "send"},
		Short:       "отправить сообщение (stdin или --file)",
		UsageExtras: "<room>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
			{Name: "--reply-to", Value: "<id>", Desc: "id сообщения, на которое это ответ"},
			{Name: "--reference-id", Value: "<uuid>", Desc: "клиентский dedup-идентификатор"},
			{Name: "--silent", Desc: "отправить без уведомления получателей"},
			{Name: "--file", Value: "<путь>", Desc: "взять тело из файла (иначе stdin)"},
		},
		Examples: []string{
			"echo \"hi\" | nctalk chat send abc123",
			"nctalk chat send --name \"Команда\" --file msg.txt",
		},
	},
	{
		Path:        []string{"reactions", "get"},
		Short:       "реакции на сообщение",
		UsageExtras: "<room> <messageId>",
		Flags: []flagSpec{
			{Name: "--name", Value: "<имя>", Desc: "разрешить комнату по имени (вместо token)"},
		},
		Examples: []string{
			"nctalk reactions get abc123 100",
		},
	},
	{
		Path:        []string{"search"},
		Short:       "глобальный поиск по сообщениям",
		UsageExtras: "<term>",
		Flags: []flagSpec{
			{Name: "--from", Value: "<actorId>", Desc: "фильтр по автору"},
			{Name: "--limit", Value: "N", Desc: "размер выборки (по умолчанию 10)"},
			{Name: "--all", Desc: "без ограничения размера (в пределах сервера)"},
		},
		Examples: []string{
			"nctalk search foo",
			"nctalk search foo --from alice --limit 25",
		},
	},
}

// findSpec возвращает *cmdSpec по пути path (1-2 токена) или nil, если пути нет.
// Используется HandleHelp, renderGeneral/renderDetailed и anti-drift тестами.
func findSpec(path []string) *cmdSpec {
	for i := range cmdSpecs {
		if pathsEqual(cmdSpecs[i].Path, path) {
			return &cmdSpecs[i]
		}
	}
	return nil
}

// pathsEqual — почленное сравнение слайсов строк.
func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// joinPath — "rooms list" для ["rooms","list"], "search" для ["search"].
// Используется в render-функциях и тестах.
func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
```

- [ ] **Step 4: добавить недостающие импорты (`sort`) — если нужно**

В `help_test.go` добавить `"sort"` (уже есть в примере выше).

- [ ] **Step 5: запустить тест, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestCmdSpecs -v`
Expected: PASS для всех трёх тестов (`CoverAllRoutes`, `OrderMatchesHelpOrder`, `FlagsExactly`).

- [ ] **Step 6: прогнать полный пакет, убедиться, что ничего не сломано**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v`
Expected: PASS — старые тести зелёные, новые тести зелёные. `cmdSpecs` пока НИКАК не используется в production-коде — это нормально для этого task'а.

- [ ] **Step 7: commit**

```sh
git add internal/cli/help.go internal/cli/help_test.go
git commit -m "feat(cli): cmdSpecs — декларация 7 команд + coverage-тесты"
```

**DoD:** `cmdSpecs` содержит ровно 7 команд в порядке общего help; флаги каждой команды точно совпадают с каноном спеки §4 (без `--include-former` у `rooms find`, без `--limit` у `rooms search`); три coverage-теста зелёные; существующие тесты `internal/cli` не сломаны.

---

### Task 2: Разбор help-запроса (`isHelpRequest`, `computePath`)

**Цель:** реализовать логику разбора args из спеки §5 (порядок разбора) и §2 (правила help-форм). Чистые функции без副作用 — лего тестируемые.

**Files:**
- Modify: `internal/cli/help.go` (добавить функции)
- Modify: `internal/cli/help_test.go` (добавить тесты)

**Interfaces:**
- Produces:
  ```go
  // isHelpRequest определяет, является ли args help-запросом (спека §5 шаги 1–2).
  // Возвращает:
  //   helpRequested=true, path — args содержит --help/-h или ведущее "help";
  //                               path уже вычислен (0/1/2 токена, спека §2).
  //   helpRequested=false, nil — args не содержит help-маркеров (обычный flow).
  //
  // Пустые args НЕ считаются help-запросом (это отдельный случай "no-args error",
  // обрабатывается в HandleHelp).
  func isHelpRequest(args []string) (helpRequested bool, path []string)

  // computePath возвращает первые 1-2 не-флаговых токена из args (спека §2).
  // --*-токены выкидываются; для известных value-флагов следующий токен
  // (значение) тоже выкидывается; unknown-флаг трактуется как boolean (safe).
  func computePath(args []string) []string

  // isValueFlag проверяет по cmdSpecs, принимает ли флаг значение.
  // Возвращает true для --name/--type/--last/--from/--since/--user/--file/
  // --reply-to/--reference-id/--limit; false для boolean-флагов и unknown.
  func isValueFlag(flagName string) bool
  ```

- [ ] **Step 1: написать failing-тесты для `isHelpRequest` и `computePath`**

Добавить в `internal/cli/help_test.go`:

```go
// TestIsHelpRequest — таблица всех форм help-запроса из спеки §2.
// Возвращает helpRequested=true и вычисленный path; иначе false, nil.
func TestIsHelpRequest(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPath []string
	}{
		// --help / -h в любой позиции
		{"help flag alone", []string{"--help"}, []string{}},
		{"h flag alone", []string{"-h"}, []string{}},
		{"help flag after cmd", []string{"rooms", "list", "--help"}, []string{"rooms", "list"}},
		{"help flag before cmd", []string{"--help", "rooms", "list"}, []string{"rooms", "list"}},
		{"h flag mid cmd", []string{"chat", "-h", "show"}, []string{"chat", "show"}},
		// leading help word
		{"help word alone", []string{"help"}, []string{}},
		{"help word resource", []string{"help", "rooms"}, []string{"rooms"}},
		{"help word cmd", []string{"help", "rooms", "list"}, []string{"rooms", "list"}},
		{"help word leaf", []string{"help", "search"}, []string{"search"}},
		// комбинированные случаи
		{"help word plus flag", []string{"help", "chat", "show", "--help"}, []string{"chat", "show"}},
		// позиционные + unknown-флаг (флаг выкинут, позиционные берутся как путь)
		{"unknown flag stripped", []string{"rooms", "list", "--nope", "--help"}, []string{"rooms", "list"}},
		// позиционные сверх двух — берутся первые 2 (класс help не меняется)
		{"extra positionals truncated", []string{"chat", "show", "abc123", "--help"}, []string{"chat", "show"}},
		// value-флаг со значением в форме --flag value (значение выкинуть)
		{"value flag space form", []string{"--name", "Команда", "rooms", "list", "--help"}, []string{"rooms", "list"}},
		// value-флаг в форме --flag=value (один токен)
		{"value flag eq form", []string{"chat", "show", "--name=Команда", "--help"}, []string{"chat", "show"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReq, gotPath := isHelpRequest(tc.args)
			if !gotReq {
				t.Fatalf("isHelpRequest(%v): want helpRequested=true, got false", tc.args)
			}
			if !pathsEqual(gotPath, tc.wantPath) {
				t.Errorf("isHelpRequest(%v): path: got %v, want %v", tc.args, gotPath, tc.wantPath)
			}
		})
	}
}

// TestIsHelpRequest_NotHelp — обычные args без help-маркеров → false, nil.
func TestIsHelpRequest_NotHelp(t *testing.T) {
	cases := [][]string{
		nil,                       // пустые args — НЕ help (отдельный случай в HandleHelp)
		{},                        // то же
		{"rooms", "list"},         // обычная команда
		{"search", "foo"},
		{"chat", "show", "tok", "--last", "50"},
		{"--json", "rooms", "list"}, // --json — не help-маркер
	}
	for _, args := range cases {
		gotReq, gotPath := isHelpRequest(args)
		if gotReq {
			t.Errorf("isHelpRequest(%v): want false, got true (path=%v)", args, gotPath)
		}
		if gotPath != nil {
			t.Errorf("isHelpRequest(%v): path: want nil, got %v", args, gotPath)
		}
	}
}

// TestComputePath — выделение первых 1-2 не-флаговых токенов (спека §2).
func TestComputePath(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{}, []string{}},
		{[]string{"rooms"}, []string{"rooms"}},
		{[]string{"rooms", "list"}, []string{"rooms", "list"}},
		{[]string{"rooms", "list", "extra"}, []string{"rooms", "list"}}, // truncate
		{[]string{"--name", "X", "rooms", "list"}, []string{"rooms", "list"}}, // value flag
		{[]string{"chat", "--name=X", "show"}, []string{"chat", "show"}},      // --flag=val
		{[]string{"--unread", "rooms", "list"}, []string{"rooms", "list"}},    // bool flag
		{[]string{"--nope", "rooms", "list"}, []string{"rooms", "list"}},      // unknown flag → bool
	}
	for _, tc := range cases {
		got := computePath(tc.args)
		if !pathsEqual(got, tc.want) {
			t.Errorf("computePath(%v): got %v, want %v", tc.args, got, tc.want)
		}
	}
}

// TestIsValueFlag — какие флаги едят значение, какие нет.
func TestIsValueFlag(t *testing.T) {
	valueFlags := []string{"--name", "--type", "--last", "--from", "--since",
		"--user", "--file", "--reply-to", "--reference-id", "--limit"}
	for _, f := range valueFlags {
		if !isValueFlag(f) {
			t.Errorf("isValueFlag(%q): want true, got false", f)
		}
	}
	boolFlags := []string{"--unread", "--include-former", "--system", "--silent", "--all", "--json"}
	for _, f := range boolFlags {
		if isValueFlag(f) {
			t.Errorf("isValueFlag(%q): want false, got true", f)
		}
	}
	if isValueFlag("--zzz-unknown") {
		t.Errorf("isValueFlag(--zzz-unknown): unknown должен трактоваться как boolean (safe)")
	}
}
```

- [ ] **Step 2: запустить, убедиться в FAIL**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestIsHelpRequest|TestComputePath|TestIsValueFlag' -v`
Expected: FAIL — функции не определены.

- [ ] **Step 3: реализовать `isHelpRequest`, `computePath`, `isValueFlag` в `help.go`**

Добавить в `internal/cli/help.go` (вместе с новым импортом `strings`):

```go
import (
	"strings"
)

// isHelpRequest — спека §5 (порядок разбора) и §2 (правила help-форм).
//
// Шаги:
//  1. Вырезать из args все --help/-h (в любой позиции); запомнить sawHelp.
//  2. Если первый оставшийся токен — "help", вырезать его и запомнить sawHelp.
//  3. Если sawHelp — вычислить путь (первые 1-2 не-флаговых токена остатка).
//
// Пустые args → НЕ help-запрос (это "no-args error" — отдельный случай в HandleHelp).
func isHelpRequest(args []string) (helpRequested bool, path []string) {
	filtered := make([]string, 0, len(args))
	sawHelp := false
	for _, a := range args {
		if a == "--help" || a == "-h" {
			sawHelp = true
			continue
		}
		filtered = append(filtered, a)
	}
	if len(filtered) > 0 && filtered[0] == "help" {
		sawHelp = true
		filtered = filtered[1:]
	}
	if !sawHelp {
		return false, nil
	}
	return true, computePath(filtered)
}

// computePath возвращает первые 1-2 не-флаговых токена (спека §2). Все --*
// выкидываются; для известных value-флагов в форме --flag (без =) следующий
// токен (значение) тоже выкидывается; unknown --flag трактуется как boolean
// (safe: не съедает возможный позиционный).
func computePath(args []string) []string {
	path := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			path = append(path, a)
			if len(path) == 2 {
				return path
			}
			continue
		}
		// Это флаг.
		if strings.Contains(a, "=") {
			continue // --flag=value — один токен, уже выкинут
		}
		if isValueFlag(a) && i+1 < len(args) {
			i++ // пропускаем значение в форме --flag value
		}
	}
	return path
}

// isValueFlag проверяет по cmdSpecs, принимает ли флаг значение. Unknown-флаг
// трактуется как boolean (safe для computePath: не съедает следующий позиционный).
func isValueFlag(flagName string) bool {
	name := strings.TrimPrefix(flagName, "--")
	for _, cs := range cmdSpecs {
		for _, f := range cs.Flags {
			if strings.TrimPrefix(f.Name, "--") == name {
				return f.Value != ""
			}
		}
	}
	return false
}
```

Импорты `help.go` после этого шага: только `"strings"` (для `strings.HasPrefix`/`Contains`/`TrimPrefix`). `sort` здесь НЕ нужен — функции Task 1–2 (`findSpec`/`pathsEqual`/`joinPath`/`isHelpRequest`/`computePath`/`isValueFlag`) сортировку не используют; `sort` остаётся только в `help_test.go` (для `TestCmdSpecs_FlagsExactly`).

- [ ] **Step 4: запустить, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run 'TestIsHelpRequest|TestComputePath|TestIsValueFlag' -v`
Expected: PASS для всех случаев.

- [ ] **Step 5: прогнать весь пакет**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v`
Expected: PASS — ничего не сломано.

- [ ] **Step 6: commit**

```sh
git add internal/cli/help.go internal/cli/help_test.go
git commit -m "feat(cli): isHelpRequest/computePath — разбор help-форм (спека §2/§5)"
```

**DoD:** все случаи из §2 (включая `--flag value`, `--flag=value`, unknown-флаг, truncate до 2 токенов) покрыты тестами и работают; обычные args (без help-маркеров, включая `--json`) возвращают `(false, nil)`.

---

### Task 3: Рендеринг help-текстов + `HandleHelp` + routing-тесты

**Цель:** реализовать три класса help-вывода (общий, по ресурсу, детальный) + `HandleHelp` как единую точку перехвата; покрыть все routing-случаи спеки §2 (exit-коды и потоки).

**Files:**
- Modify: `internal/cli/help.go`
- Modify: `internal/cli/help_test.go`

**Interfaces:**
- Produces:
  ```go
  // HandleHelp проверяет args: help-запрос, no-args или обычный flow (спека §5).
  // Возвращает (true, code) если args — help/no-args случай (уже напечатано
  // в deps.Stdout или deps.Stderr). Deps.Client при этом НЕ используется.
  // Возвращает (false, 0) — обычный flow; caller должен вызвать config.Load/cli.Run.
  //
  // Спека §2 (exit-коды и потоки):
  //   - успешный help (с распознанным путём) → deps.Stdout, exit 0
  //   - пустые args (no-args) → общий help в deps.Stderr, exit 1
  //   - help с неизвестным путём → "неизвестная команда" + подсказка, deps.Stderr, exit 1
  func HandleHelp(args []string, deps Deps) (handled bool, exit int)
  ```

- [ ] **Step 1: написать failing-тесты routing-логики `HandleHelp`**

Добавить в `internal/cli/help_test.go`:

```go
// TestHandleHelp_Routing — все случаи спеки §2 (exit-код, поток, ключевые строки).
// Детальная проверка СОДЕРЖИМОГО — в TestHandleHelp_DetailedContent (следующий шаг);
// здесь проверяем только routing: куда ушёл вывод, какой exit.
func TestHandleHelp_Routing(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStream string // "stdout" | "stderr" | "any"
	}{
		// Успешный help → stdout, exit 0
		{"help flag alone", []string{"--help"}, 0, "stdout"},
		{"h flag alone", []string{"-h"}, 0, "stdout"},
		{"help word alone", []string{"help"}, 0, "stdout"},
		{"help word resource", []string{"help", "rooms"}, 0, "stdout"},
		{"help word detailed", []string{"help", "rooms", "list"}, 0, "stdout"},
		{"help word leaf", []string{"help", "search"}, 0, "stdout"},
		{"cmd with help flag", []string{"rooms", "list", "--help"}, 0, "stdout"},
		{"cmd with h flag", []string{"chat", "show", "-h"}, 0, "stdout"},
		{"resource with help flag", []string{"rooms", "--help"}, 0, "stdout"},
		{"flag before cmd", []string{"--help", "rooms", "list"}, 0, "stdout"},

		// no-args → stderr, exit 1 (НЕ help-запрос, но HandleHelp обрабатывает)
		{"no args nil", nil, 1, "stderr"},
		{"no args empty", []string{}, 1, "stderr"},

		// help с неизвестным путём → stderr, exit 1
		{"help unknown resource", []string{"help", "nosuch"}, 1, "stderr"},
		{"help unknown verb", []string{"rooms", "nosuch", "--help"}, 1, "stderr"},
		{"help unknown leaf", []string{"help", "nosuch"}, 1, "stderr"},

		// обычный flow — HandleHelp возвращает (false, 0); тест ниже
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newTestDeps()
			handled, code := HandleHelp(tc.args, deps)
			if !handled {
				t.Fatalf("HandleHelp(%v): want handled=true, got false", tc.args)
			}
			if code != tc.wantExit {
				t.Errorf("HandleHelp(%v): exit: got %d, want %d", tc.args, code, tc.wantExit)
			}
			stdout := deps.Stdout.(*bytes.Buffer).String()
			stderr := deps.Stderr.(*bytes.Buffer).String()
			switch tc.wantStream {
			case "stdout":
				if stdout == "" {
					t.Errorf("HandleHelp(%v): want non-empty stdout, got empty (stderr=%q)", tc.args, stderr)
				}
				if stderr != "" {
					t.Errorf("HandleHelp(%v): want empty stderr, got %q", tc.args, stderr)
				}
			case "stderr":
				if stderr == "" {
					t.Errorf("HandleHelp(%v): want non-empty stderr, got empty (stdout=%q)", tc.args, stdout)
				}
				if stdout != "" {
					t.Errorf("HandleHelp(%v): want empty stdout, got %q", tc.args, stdout)
				}
			}
		})
	}
}

// TestHandleHelp_NotHandled — обычные args без help-маркеров → (false, 0).
func TestHandleHelp_NotHandled(t *testing.T) {
	cases := [][]string{
		{"rooms", "list"},
		{"search", "foo"},
		{"chat", "show", "tok", "--last", "50"},
		{"--json", "rooms", "list"}, // --json — НЕ help-маркер
	}
	for _, args := range cases {
		deps := newTestDeps()
		handled, code := HandleHelp(args, deps)
		if handled {
			t.Errorf("HandleHelp(%v): want (false,0), got handled=true code=%d", args, code)
		}
		if code != 0 {
			t.Errorf("HandleHelp(%v): code: want 0, got %d", args, code)
		}
	}
}

// TestHandleHelp_UnknownPathMessage — «неизвестная команда» + подсказка nctalk --help.
func TestHandleHelp_UnknownPathMessage(t *testing.T) {
	deps := newTestDeps()
	_, code := HandleHelp([]string{"help", "nosuch"}, deps)
	if code != 1 {
		t.Fatalf("exit: got %d, want 1", code)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	for _, want := range []string{"неизвестная команда", "nctalk --help"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr должен содержать %q; got %q", want, stderr)
		}
	}
}
```

Добавить `"bytes"`, `"strings"` в импорты `help_test.go`, если ещё нет (`bytes.Buffer` используется в type-assertion `deps.Stdout.(*bytes.Buffer).String()`; `strings.Contains` — в substring-проверках). После этого шага импорт-блок `help_test.go`: `"bytes"`, `"reflect"`, `"sort"`, `"strings"`, `"testing"`.

- [ ] **Step 2: запустить, убедиться в FAIL**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestHandleHelp -v`
Expected: FAIL — `HandleHelp` не определена.

- [ ] **Step 3: реализовать `HandleHelp` и функции рендера в `help.go`**

Добавить в `internal/cli/help.go` (с новыми импортами `"fmt"`, `"io"`):

```go
import (
	"fmt"
	"io"
	"strings"
)

// HandleHelp — спека §2/§5. См. docstring в interfaces выше.
func HandleHelp(args []string, deps Deps) (handled bool, exit int) {
	// no-args → общий help в stderr, exit 1 (отдельный случай, не help-запрос).
	if len(args) == 0 {
		renderGeneral(deps.Stderr)
		return true, ExitGeneric
	}

	helpReq, path := isHelpRequest(args)
	if !helpReq {
		return false, 0
	}

	// Класс help по длине пути (спека §2):
	//   path == [] → общий help (stdout, exit 0)
	//   path == [resource] → help по ресурсу (stdout, exit 0)
	//   path == [resource, verb] → детальный (stdout, exit 0)
	//   path == [leaf] → детальный по leaf-команде (stdout, exit 0)
	//   невалидный путь → stderr + exit 1
	switch len(path) {
	case 0:
		renderGeneral(deps.Stdout)
		return true, ExitOK
	case 1:
		// Может быть ресурсом (rooms/chat/reactions) или leaf-командой (search).
		if spec := findSpec(path); spec != nil {
			renderDetailed(deps.Stdout, spec)
			return true, ExitOK
		}
		if isResourceName(path[0]) {
			renderResource(deps.Stdout, path[0])
			return true, ExitOK
		}
	case 2:
		if spec := findSpec(path); spec != nil {
			renderDetailed(deps.Stdout, spec)
			return true, ExitOK
		}
	}
	// Невалидный путь — спека §2 последняя строка таблицы.
	fmt.Fprintf(deps.Stderr, "nctalk: неизвестная команда %q\n", strings.Join(path, " "))
	fmt.Fprintln(deps.Stderr, "смотрите: nctalk --help")
	return true, ExitGeneric
}

// isResourceName проверяет, есть ли resource в таблице routes (rooms/chat/reactions).
func isResourceName(name string) bool {
	_, ok := routes[name]
	return ok
}

// renderGeneral — общий help (спека §3).
func renderGeneral(w io.Writer) {
	fmt.Fprintln(w, "nctalk — тонкий CLI над Nextcloud Talk (Spreed)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Использование:")
	fmt.Fprintln(w, "  nctalk <команда> [flags]")
	fmt.Fprintln(w, "  nctalk <команда> --help    помощь по команде")
	fmt.Fprintln(w, "  nctalk --help              эта справка")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Команды:")
	for _, cs := range cmdSpecs {
		// Выровненные отступы: ширина = max ширина пути.
		fmt.Fprintf(w, "  %-25s %s\n", joinPath(cs.Path), cs.Short)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Глобальные флаги:")
	fmt.Fprintln(w, "  --json                    вывод в JSON (в любой позиции)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Окружение:")
	fmt.Fprintln(w, "  NEXTCLOUD_URL             адрес сервера (https://...)")
	fmt.Fprintln(w, "  NEXTCLOUD_LOGIN           логин")
	fmt.Fprintln(w, "  NEXTCLOUD_PASS            app-password (только env)")
	fmt.Fprintln(w, "  NEXTCLOUD_TIMEOUT         таймаут HTTP (по умолчанию 30s)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit-коды: 0 — успех, 1 — общая ошибка/сеть/401/5xx,")
	fmt.Fprintln(w, "           2 — не найдено, 3 — неоднозначное совпадение.")
}

// renderResource — help по ресурсу: заголовок + список verbs (спека §4 последний абзац).
func renderResource(w io.Writer, resource string) {
	fmt.Fprintf(w, "nctalk %s — команды ресурса\n", resource)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Команды:")
	for _, cs := range cmdSpecs {
		if len(cs.Path) == 2 && cs.Path[0] == resource {
			fmt.Fprintf(w, "  %s %-10s %s\n", resource, cs.Path[1], cs.Short)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Смотрите: nctalk %s <команда> --help\n", resource)
}

// renderDetailed — детальный help по команде (спека §4).
func renderDetailed(w io.Writer, spec *cmdSpec) {
	cmd := joinPath(spec.Path)
	fmt.Fprintf(w, "nctalk %s — %s\n", cmd, spec.Short)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Использование:")
	if spec.UsageExtras != "" {
		fmt.Fprintf(w, "  nctalk %s %s [flags]\n", cmd, spec.UsageExtras)
	} else {
		fmt.Fprintf(w, "  nctalk %s [flags]\n", cmd)
	}
	fmt.Fprintln(w)
	if hasRoomResolve(spec) {
		fmt.Fprintln(w, "<room> — token комнаты позиционно, либо --name для разрешения по имени.")
		fmt.Fprintln(w)
	}
	if len(spec.Flags) > 0 {
		fmt.Fprintln(w, "Флаги:")
		for _, f := range spec.Flags {
			name := f.Name
			if f.Value != "" {
				name = f.Name + " " + f.Value
			}
			fmt.Fprintf(w, "  %-22s  %s\n", name, f.Desc)
		}
		fmt.Fprintln(w, "  --json                  вывод в JSON")
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "Флаги:")
		fmt.Fprintln(w, "  --json                  вывод в JSON")
		fmt.Fprintln(w)
	}
	if len(spec.Examples) > 0 {
		fmt.Fprintln(w, "Примеры:")
		for _, ex := range spec.Examples {
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
}

// hasRoomResolve — есть ли у команды флаг --name (признак room-resolution).
func hasRoomResolve(spec *cmdSpec) bool {
	for _, f := range spec.Flags {
		if f.Name == "--name" {
			return true
		}
	}
	return false
}
```

> Замечание по форматированию: точное выравнивание пробелами в `renderDetailed` не тестируется жёстко — substring-тесты (следующий task) проверяют наличие флагов и описаний, а не ширину колонок. Если хочется более аккуратного выравнивания — реализуй через `text/tabwriter`, но это не требуется по спеке.

- [ ] **Step 4: запустить routing-тесты, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestHandleHelp_ -v`
Expected: PASS для всех routing-случаев и not-handled.

- [ ] **Step 5: добавить substring-тесты содержимого**

Добавить в `help_test.go`:

```go
// TestHandleHelp_GeneralContent — ключевые строки общего help (спека §3).
func TestHandleHelp_GeneralContent(t *testing.T) {
	deps := newTestDeps()
	_, code := HandleHelp([]string{"--help"}, deps)
	if code != 0 {
		t.Fatalf("exit: got %d, want 0", code)
	}
	stdout := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{
		"nctalk", "Nextcloud Talk",
		"Использование:",
		"rooms list", "rooms find", "rooms search",
		"chat show", "chat send",
		"reactions get", "search",
		"NEXTCLOUD_URL", "NEXTCLOUD_LOGIN", "NEXTCLOUD_PASS", "NEXTCLOUD_TIMEOUT",
		"Exit-коды",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("general help: stdout не содержит %q\nвывод=\n%s", want, stdout)
		}
	}
}

// TestHandleHelp_DetailedContent — для каждой из 7 команд проверяем: имя команды,
// заголовок, usage, каждый флаг, описание флага, --json, примеры.
func TestHandleHelp_DetailedContent(t *testing.T) {
	for _, cs := range cmdSpecs {
		t.Run(joinPath(cs.Path), func(t *testing.T) {
			deps := newTestDeps()
			args := append(append([]string{}, cs.Path...), "--help")
			_, code := HandleHelp(args, deps)
			if code != 0 {
				t.Fatalf("exit: got %d, want 0", code)
			}
			stdout := deps.Stdout.(*bytes.Buffer).String()
			// Имя команды и short-описание
			if !strings.Contains(stdout, joinPath(cs.Path)) {
				t.Errorf("не содержит имя команды %q\nвывод=\n%s", joinPath(cs.Path), stdout)
			}
			if !strings.Contains(stdout, cs.Short) {
				t.Errorf("не содержит short-описание %q\nвывод=\n%s", cs.Short, stdout)
			}
			// Usage
			if !strings.Contains(stdout, "Использование:") {
				t.Errorf("не содержит 'Использование:'\nвывод=\n%s", stdout)
			}
			// Каждый флаг и его описание
			for _, f := range cs.Flags {
				if !strings.Contains(stdout, f.Name) {
					t.Errorf("не содержит флаг %q\nвывод=\n%s", f.Name, stdout)
				}
				if !strings.Contains(stdout, f.Desc) {
					t.Errorf("не содержит описание флага %q: %q\nвывод=\n%s", f.Name, f.Desc, stdout)
				}
			}
			// --json (глобальный) присутствует всегда
			if !strings.Contains(stdout, "--json") {
				t.Errorf("не содержит глобальный --json\nвывод=\n%s", stdout)
			}
			// Примеры (если есть)
			for _, ex := range cs.Examples {
				if !strings.Contains(stdout, ex) {
					t.Errorf("не содержит пример %q\nвывод=\n%s", ex, stdout)
				}
			}
		})
	}
}

// TestHandleHelp_ResourceContent — help по ресурсу содержит его verbs.
func TestHandleHelp_ResourceContent(t *testing.T) {
	deps := newTestDeps()
	_, code := HandleHelp([]string{"help", "rooms"}, deps)
	if code != 0 {
		t.Fatalf("exit: got %d, want 0", code)
	}
	stdout := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{"rooms", "list", "find", "search", "nctalk rooms <команда> --help"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("resource help rooms: не содержит %q\nвывод=\n%s", want, stdout)
		}
	}
}

// TestHandleHelp_NoArgsGeneralToStderr — no-args → общий help в stderr (НЕ stdout).
func TestHandleHelp_NoArgsGeneralToStderr(t *testing.T) {
	deps := newTestDeps()
	_, code := HandleHelp(nil, deps)
	if code != 1 {
		t.Fatalf("exit: got %d, want 1 (no-args)", code)
	}
	stderr := deps.Stderr.(*bytes.Buffer).String()
	stdout := deps.Stdout.(*bytes.Buffer).String()
	if stdout != "" {
		t.Errorf("no-args: stdout должен быть пуст, got %q", stdout)
	}
	// stderr содержит тот же общий help (не короткое сообщение об ошибке).
	for _, want := range []string{"nctalk", "Использование:", "NEXTCLOUD_URL"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no-args stderr должен содержать %q; got %q", want, stderr)
		}
	}
}
```

- [ ] **Step 6: запустить, убедиться в PASS всех help-тестов**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestHandleHelp -v`
Expected: PASS — routing + content + no-args все зелёные.

- [ ] **Step 7: прогнать весь пакет**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v && CGO_ENABLED=0 go vet ./internal/cli/`
Expected: PASS — ничего не сломано, vet чистый.

- [ ] **Step 8: commit**

```sh
git add internal/cli/help.go internal/cli/help_test.go
git commit -m "feat(cli): HandleHelp + render general/resource/detailed (спека §2/§3/§4)"
```

**DoD:** три класса help (общий/ресурс/детальный) рендерятся из `cmdSpecs`; все routing-случаи §2 (exit-код и поток) покрыты и работают; substring-проверки каждой из 7 команд проходят; no-args даёт общий help в stderr + exit 1.

---

### Task 4: Anti-drift тесты (help↔декларация И декларация↔handler)

**Цель:** защититься от обоих классов дрейфа (см. спеку §6): «флаг есть в handler, но забыли в help» и «help упоминает флаг, которого handler не парсит» — последняя была найдена в review спеки для `rooms find --include-former` и `rooms search --limit`.

**Files:**
- Modify: `internal/cli/help_test.go`

**Interfaces:**
- Consumes: `cmdSpecs`, `HandleHelp` (из Task 1/3); `routes`, `searchHandlerFn`, `handlerFn` (из существующего `cli.go`).

- [ ] **Step 1: написать failing-тест prong 1 (help↔декларация)**

```go
// TestAntiDrift_HelpMatchesDeclaration — для каждой команды: help-текст упоминает
// РОВНО флаги из cmdSpecs (без лишних и без пропущенных). Спека §6 prong 1.
// Ловит класс «флаг в декларации, но забыт в render-логике» (или наоборот).
func TestAntiDrift_HelpMatchesDeclaration(t *testing.T) {
	for _, cs := range cmdSpecs {
		t.Run(joinPath(cs.Path), func(t *testing.T) {
			deps := newTestDeps()
			args := append(append([]string{}, cs.Path...), "--help")
			_, code := HandleHelp(args, deps)
			if code != 0 {
				t.Fatalf("exit: got %d, want 0", code)
			}
			stdout := deps.Stdout.(*bytes.Buffer).String()
			declared := map[string]bool{"--json": true} // глобальный
			for _, f := range cs.Flags {
				declared[f.Name] = true
			}
			// Каждый заявленный флаг присутствует в help.
			for name := range declared {
				if !strings.Contains(stdout, name) {
					t.Errorf("anti-drift: заявленный флаг %q отсутствует в help\nвывод=\n%s", name, stdout)
				}
			}
			// В help нет --*-флагов, не заявленных в декларации (кроме --json и --help).
			for _, token := range tokeniseFlags(stdout) {
				if token == "--json" || token == "--help" || token == "-h" {
					continue
				}
				if !declared[token] {
					t.Errorf("anti-drift: help упоминает незаявленный флаг %q (не в cmdSpecs)\nвывод=\n%s", token, stdout)
				}
			}
		})
	}
}

// tokeniseFlags — находит все --[a-z][a-z-]* токены в тексте (грубый парсинг).
func tokeniseFlags(s string) []string {
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
			flush()
			cur = "--"
			i++
			for i+1 < len(s) {
				c := s[i+1]
				if (c >= 'a' && c <= 'z') || c == '-' {
					cur += string(c)
					i++
				} else {
					break
				}
			}
			flush()
		}
	}
	return out
}
```

- [ ] **Step 2: запустить — должен PASS (тексты уже из cmdSpecs)**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestAntiDrift_HelpMatchesDeclaration -v`
Expected: PASS — это green-тест (он защищает будущее), а не red-тест на существующий баг.

> Если падает: значит, в `renderDetailed` есть захардкоженный `--help` или другой флаг, не присутствующий в декларации. Проверь, что render работает строго по `spec.Flags` + один глобальный `--json`.

- [ ] **Step 3: написать failing-тест prong 2 (декларация↔handler)**

```go
// TestAntiDrift_HandlerMatchesDeclaration — для каждой команды: handler
// принимает КАЖДЫЙ заявленный в cmdSpecs флаг (без ошибки "неизвестный флаг") И
// отвергает любой незаявленный --*-флаг (с ошибкой "неизвестный флаг"). Спека §6 prong 2.
//
// Ловит класс «флаг в декларации, но handler его не парсит» (тот самый баг с
// rooms find --include-former и rooms search --limit из review спеки) и обратный.
func TestAntiDrift_HandlerMatchesDeclaration(t *testing.T) {
	ctx := context.Background()
	for _, cs := range cmdSpecs {
		t.Run(joinPath(cs.Path), func(t *testing.T) {
			handler := lookupHandlerByPath(cs.Path)
			if handler == nil {
				t.Fatalf("lookupHandlerByPath(%v) = nil", cs.Path)
			}

			// 1. Каждый ЗАЯВЛЕННЫЙ флаг handler принимает.
			for _, f := range cs.Flags {
				deps := newTestDeps()
				// chat send читает тело ДО ResolveRoom: при nil Stdin (дефолт
				// newTestDeps) handler fallback-ает на os.Stdin — в CI обычно
				// мгновенный EOF («тело пусто» → ExitGeneric), но на tty stdin
				// может зависнуть, и semantics prong 1 ослабляется (handler
				// падает до проверки семантики флага). Даём непустое Stdin-тело
				// для chat send — handler доходит до mockTalkClient.SendMessage
				// (errMock, тоже не «неизвестный флаг»). Для --file это shimmer
				// (handler идёт через os.ReadFile, но Stdin безвреден).
				if joinPath(cs.Path) == "chat send" {
					deps.Stdin = strings.NewReader("anti-drift body")
				}
				args := buildArgsForFlag(cs, f)
				ee := handler(ctx, deps, args, false)
				// Допустимо: любая ошибка, КРОМЕ "неизвестный флаг".
				if ee.Err != nil && strings.Contains(ee.Err.Error(), "неизвестный флаг") {
					t.Errorf("anti-drift: заявленный флаг %q отвергнут handler-ом: %v\nargs=%v", f.Name, ee.Err, args)
				}
			}

			// 2. НЕЗАЯВЛЕННЫЙ флаг handler отвергает.
			deps := newTestDeps()
			positional := buildPositionalArgs(cs)
			args := append(positional, "--zzz-undeclared-test-flag")
			ee := handler(ctx, deps, args, false)
			if ee.Err == nil {
				t.Errorf("anti-drift: незаявленный флаг не отвергнут handler-ом (ожидалась ошибка 'неизвестный флаг'); args=%v", args)
			} else if !strings.Contains(ee.Err.Error(), "неизвестный флаг") {
				t.Errorf("anti-drift: незаявленный флаг дал другую ошибку (не 'неизвестный флаг'): %v\nargs=%v", ee.Err, args)
			}
		})
	}
}

// lookupHandlerByPath — возвращает handler-функцию для пути команды.
// search → searchHandlerFn; ["rooms","list"] → routes["rooms"]["list"].
func lookupHandlerByPath(path []string) handlerFn {
	if len(path) == 1 && path[0] == "search" {
		return searchHandlerFn
	}
	if len(path) == 2 {
		if verbs, ok := routes[path[0]]; ok {
			return verbs[path[1]]
		}
	}
	return nil
}

// buildPositionalArgs — минимальный набор позиционных для команды, чтобы handler
// не упал с "ожидается <positional>" ДО парсинга флагов. Флаги парсятся ранее
// positionals во всех handler-ах, но для надёжности даём валидные positionals.
func buildPositionalArgs(cs cmdSpec) []string {
	switch joinPath(cs.Path) {
	case "rooms find":
		return []string{"query"}
	case "rooms search":
		return []string{"term"}
	case "chat show", "chat send":
		return []string{"tok123"}
	case "reactions get":
		return []string{"tok123", "1"}
	case "search":
		return []string{"term"}
	default:
		return nil
	}
}

// buildArgsForFlag — позиционные + конкретный флаг (с валидным значением для value-флага).
func buildArgsForFlag(cs cmdSpec, f flagSpec) []string {
	args := buildPositionalArgs(cs)
	if f.Value == "" {
		// boolean flag
		args = append(args, f.Name)
	} else {
		// value flag — подбираем валидное значение по имени флага
		args = append(args, f.Name, valueForFlag(f.Name))
	}
	return args
}

// valueForFlag — валидное значение для конкретного value-флага, которое handler
// примёт без ошибки парсинга (например, --type 2, --last 5, --reply-to 1).
func valueForFlag(name string) string {
	switch name {
	case "--type":
		return "2"
	case "--last", "--limit":
		return "5"
	case "--reply-to":
		return "1"
	case "--since":
		return "1h" // валидный относительный формат — parseSinceAt разберёт, handler дойдёт до ResolveRoom
	default:
		return "x" // произвольная строка для --name/--from/--user/--file/--reference-id
	}
}
```

Добавить `"context"` в импорты `help_test.go`, если ещё нет.

- [ ] **Step 4: запустить, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestAntiDrift -v`
Expected: PASS — все 7 команд принимают свои флаги и отвергают `--zzz-undeclared-test-flag`.

> Если падает на чём-то кроме тривиальных опечаток: это регрессия поведения handler-а или декларации — НЕ правь тест, разбирайся. Возможные причины:
> - в `cmdSpecs` флаг указан, но handler его не парсит (тот самый баг review-спеки);
> - handler принимает "unknown" флаг (например, молча игнорирует) — это поломка контракта;
> - `valueForFlag` даёт невалидное значение (например, `--type x` вместо `--type 2`).

- [ ] **Step 5: верификация — намеренно сломать декларацию и убедиться, что тест падает**

Это проверка, что anti-drift действительно работает (а не всегда зелёный). Временно убери `--user` из декларации `rooms find` в `cmdSpecs`, прогони тест, убедись, что `TestAntiDrift_HandlerMatchesDeclaration/rooms_find` падает (handler принимает `--user`, но в декларации его нет — prong 2 ловит). **Верни декларацию обратно.**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -run TestAntiDrift_HandlerMatchesDeclaration/rooms_find -v`
Expected (с временной правкой): FAIL. После отката: PASS.

- [ ] **Step 6: прогнать весь пакет**

Run: `CGO_ENABLED=0 go test ./internal/cli/ -v && CGO_ENABLED=0 go vet ./internal/cli/`
Expected: PASS — все help-тесты и anti-drift зелёные; существующие тесты не сломаны.

- [ ] **Step 7: commit**

```sh
git add internal/cli/help_test.go
git commit -m "test(cli): anti-drift help↔декларация и декларация↔handler (спека §6)"
```

**DoD:** оба класса drift защищены; ручная верификация (Step 5) показала, что тест действительно падает при рассинхроне; все 7 команд покрыты.

---

### Task 5: Интеграция в `cmd/nctalk/main.go` + правка `TestRun_NoEnv_Returns1` + e2e

**Цель:** подключить `HandleHelp` в `run()` строго до `config.Load()`, скорректировать существующий тест (`TestRun_NoEnv_Returns1`), добавить e2e-тесты help-без-кредов и убедиться, что изоляционный guard остаётся зелёным.

**Files:**
- Modify: `cmd/nctalk/main.go:34-48` (функция `run`)
- Modify: `cmd/nctalk/main_test.go:116-143` (тест `TestRun_NoEnv_Returns1`)

**Interfaces:**
- Consumes: `cli.HandleHelp`, `cli.Deps` (из Task 3).
- Produces: `run()`, который вызывает `cli.HandleHelp` до `config.Load()`.

- [ ] **Step 1: написать failing-тест help-без-кредов в `cmd/nctalk/main_test.go`**

Добавить в конец `cmd/nctalk/main_test.go`:

```go
// TestRun_HelpWithoutEnv_WorksWithoutCreds — спека §5: help работает БЕЗ
// NEXTCLOUD_*. HandleHelp вызывается строго до config.Load, поэтому пустые env
// не мешают; клиент не создаётся; код 0 (или 1 для no-args); в stderr/stdout — help.
func TestRun_HelpWithoutEnv_WorksWithoutCreds(t *testing.T) {
	// Явно опустошаем env (как TestRun_NoEnv_Returns1).
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("NEXTCLOUD_LOGIN", "")
	t.Setenv("NEXTCLOUD_PASS", "")
	t.Setenv("NEXTCLOUD_TIMEOUT", "")

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // "stdout" | "stderr"
	}{
		{"help flag", []string{"--help"}, 0, "stdout"},
		{"h flag", []string{"-h"}, 0, "stdout"},
		{"help word", []string{"help"}, 0, "stdout"},
		{"help rooms list", []string{"help", "rooms", "list"}, 0, "stdout"},
		{"rooms list --help", []string{"rooms", "list", "--help"}, 0, "stdout"},
		{"search --help", []string{"search", "--help"}, 0, "stdout"},
		{"no args", nil, 1, "stderr"},
		{"help unknown", []string{"help", "nosuch"}, 1, "stderr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tc.args, &out, &errOut, nil)
			if code != tc.wantCode {
				t.Fatalf("exit: got %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, out.String(), errOut.String())
			}
			switch tc.wantOut {
			case "stdout":
				if out.String() == "" {
					t.Errorf("want non-empty stdout, got empty (stderr=%q)", errOut.String())
				}
			case "stderr":
				if errOut.String() == "" {
					t.Errorf("want non-empty stderr, got empty (stdout=%q)", out.String())
				}
			}
		})
	}
}
```

- [ ] **Step 2: запустить — должен FAIL (сейчас `run()` идёт в `config.Load` первым)**

Run: `CGO_ENABLED=0 go test ./cmd/nctalk/ -run TestRun_HelpWithoutEnv -v`
Expected: FAIL — `--help` без env даёт exit 1 с config-ошибкой (а не exit 0 с help).

- [ ] **Step 3: изменить `run()` в `cmd/nctalk/main.go` — перехват help ДО `config.Load()`**

```go
// run — связка help → config → client → cli (спека 2026-08-12 §5).
//
// help перехватывается СТРОГО ДО config.Load: nctalk --help / nctalk help ... /
// nctalk <cmd> --help / nctalk (no-args) работают без NEXTCLOUD_* и не создают
// клиент. Deps.Client при help-вызове не нужен (HandleHelp его не трогает).
//
// Возвращает exit-код для os.Exit. Ошибки config.Load печатаются в stderr одной
// строкой с префиксом "nctalk:" (без значений env-переменных — см. config.Load).
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// Спека §5: HandleHelp решает «help-запрос / no-args / обычный flow».
	// При handled=true — выход с кодом help; config.Load/client/cli.Run не идут.
	if handled, code := cli.HandleHelp(args, cli.Deps{
		Stdout: stdout,
		Stderr: stderr,
	}); handled {
		return code
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "nctalk: "+err.Error())
		return 1
	}
	c := client.NewTalkClient(cfg)
	return cli.Run(args, cli.Deps{
		Client: c,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  stdin,
		Now:    time.Now,
	})
}
```

> `cli.Deps` без `Client` — это валидный частичный struct. HandleHelp не обращается к `deps.Client`, поэтому nil-поле безопасно на этом пути. `Now` тоже не нужно для help.

- [ ] **Step 4: прогнать новый тест, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./cmd/nctalk/ -run TestRun_HelpWithoutEnv -v`
Expected: PASS.

- [ ] **Step 5: скорректировать `TestRun_NoEnv_Returns1` по спеке §5**

Старый тест подавал `nil` и проверял config-ошибку со словом «обязателен». После правки `nil` → общий help в stderr + exit 1 (НЕ config-ошибка). Скорректировать: подавать args, реально доходящие до `config.Load()` (например `["rooms","list"]`), и проверять config-ошибку на этом пути.

Заменить тело `TestRun_NoEnv_Returns1` в `cmd/nctalk/main_test.go`:

```go
// TestRun_NoEnv_Returns1 — спека §5 (после правки help): пустые NEXTCLOUD_*
// при обычной команде (НЕ help-запрос) → config.Load возвращает ошибку, run
// пишет её в stderr с префиксом "nctalk:" и возвращает 1. stdout остаётся пустым.
//
// ВАЖНО: раньше этот тест подавал nil args и проверял config-ошибку; но после
// перехвата help в run() пустые args → общий help в stderr + exit 1 (не config).
// Поэтому теперь тест подаёт реальную команду, которая минует HandleHelp и
// доходит до config.Load.
func TestRun_NoEnv_Returns1(t *testing.T) {
	// Явно опустошаем все четыре env-переменные (t.Setenv восстанавливает
	// значения после теста).
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("NEXTCLOUD_LOGIN", "")
	t.Setenv("NEXTCLOUD_PASS", "")
	t.Setenv("NEXTCLOUD_TIMEOUT", "")

	var out, errOut bytes.Buffer
	// Реальная команда, минует HandleHelp (там нет --help/help) → доходит до config.Load.
	code := run([]string{"rooms", "list"}, &out, &errOut, nil)
	if code != 1 {
		t.Fatalf("ожидался exit 1, получен %d (stderr=%q)", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout должен быть пуст, получено: %q", out.String())
	}
	stderr := errOut.String()
	if !strings.HasPrefix(stderr, "nctalk:") {
		t.Errorf("stderr должен начинаться с 'nctalk:', получено: %q", stderr)
	}
	if !strings.Contains(stderr, "обязателен") {
		t.Errorf("stderr должен упоминать обязательную переменную: %q", stderr)
	}
}
```

- [ ] **Step 6: прогнать ВСЕ e2e-тесты `cmd/nctalk`, убедиться в PASS**

Run: `CGO_ENABLED=0 go test ./cmd/nctalk/ -v`
Expected: PASS — все существующие e2e (`TestRun_RoomsList_E2E`, `TestRun_ChatShow_E2E`, `TestRun_PasswordDoesNotLeak_CrossHostRedirect`, `TestRun_ChatShow_OCS404_Exit2`, `TestRun_ReactionsGet_OCS404_Exit2`, `TestRun_ChatShow_OCS401_Exit1`, `TestRun_RoomsFind_Empty_Exit0`) + новый help-тест + скорректированный `TestRun_NoEnv_Returns1`.

> Если что-то падает: вероятно, `HandleHelp` ошибочно возвращает `handled=true` для какого-то не-help-args случая — проверь граничные условия `isHelpRequest`. Особенно: `--json` без `--help`/`help` должен возвращать `(false, nil)`.

- [ ] **Step 7: проверка изоляционного guard'а**

Run: `CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion -v`
Expected: PASS — `cmd/nctalk` по-прежнему не зависит от pion.

- [ ] **Step 8: финальная полная верификация**

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
```

Expected: build OK, vet чистый, все unit-тесты (не integration) зелёные.

Дополнительная ручная проверка (по желанию, для уверенности):
```sh
CGO_ENABLED=0 go build -o /tmp/nctalk ./cmd/nctalk
NEXTCLOUD_URL= NEXTCLOUD_LOGIN= NEXTCLOUD_PASS= /tmp/nctalk --help
NEXTCLOUD_URL= NEXTCLOUD_LOGIN= NEXTCLOUD_PASS= /tmp/nctalk help chat show
NEXTCLOUD_URL= NEXTCLOUD_LOGIN= NEXTCLOUD_PASS= /tmp/nctalk search --help
NEXTCLOUD_URL= NEXTCLOUD_LOGIN= NEXTCLOUD_PASS= /tmp/nctalk
echo "exit=$?"
```
Ожидаемые exit-коды: 0, 0, 0, 1 (последний — no-args).

- [ ] **Step 9: commit**

```sh
git add cmd/nctalk/main.go cmd/nctalk/main_test.go
git commit -m "feat(nctalk): перехват --help/help в run() до config.Load (спека §5)"
```

**DoD:** `nctalk --help` / `nctalk help` / `nctalk <cmd> --help` работают без `NEXTCLOUD_*` env и без создания клиента; `nctalk` без args даёт общий help в stderr + exit 1; `nctalk help nosuch` даёт сообщение + подсказку + exit 1; все существующие e2e `cmd/nctalk` (включая `TestRun_PasswordDoesNotLeak_CrossHostRedirect`) зелёные; `TestRun_NoEnv_Returns1` скорректирован и зелёный; изоляционный guard зелёный; полный `go test ./...` зелёный.

---

## Финальная верификация (после всех 5 задач)

- [ ] `CGO_ENABLED=0 go build ./...` — без ошибок.
- [ ] `CGO_ENABLED=0 go vet ./...` — без предупреждений.
- [ ] `CGO_ENABLED=0 go test ./...` — все unit-тесты зелёные.
- [ ] `CGO_ENABLED=0 go test ./internal/call/... -run TestCmdNctalkDoesNotDependOnPion -v` — изоляция сохранена.
- [ ] Ручной smoke (по желанию): `nctalk --help`, `nctalk help chat show`, `nctalk search --help`, `nctalk` (no-args → stderr exit 1).

## Что не делается в этом плане (явно за рамками)

- Help для звонковых бинарников (`nctalk-call`/`nctalk-talk`) — там `flag`-пакет, help уже работает.
- Авто-генерация help из декларативной схемы флагов (`flag.FlagSet`-migration) — спека §7.
- `--version` / shell-completion / man-страница — спека §7.
- Изменение существующих флагов команд (`--name`, `--last`, фильтры `chat show` и т.д.) — инварианты `2026-07-17-...-design.md` §6/§8 сохраняются.
