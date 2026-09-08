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

import (
	"fmt"
	"io"
	"strings"
)

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

// cmdSpecs — единый источник правды: 8 команд базового CLI.
// Порядок соответствует общему help (спека §3). Флаги — строго по handler-ам
// (handlers_rooms.go, handlers_chat.go, handlers_reactions.go, handlers_search.go);
// --name включён только в тех командах, где handler его парсит.
//
// ВНИЗУ: каждая правка флагов в handler-е обязана сопровождаться правкой здесь;
// regression-защита — TestCmdSpecs_FlagsExactly и TestAntiDrift_HandlerMatchesDeclaration.
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
	return strings.Join(p, " ")
}

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

// HandleHelp — спека §2/§5.
//
// Проверяет args: help-запрос, no-args или обычный flow (спека §5).
// Возвращает (true, code) если args — help/no-args случай (уже напечатано
// в deps.Stdout или deps.Stderr). Deps.Client при этом НЕ используется.
// Возвращает (false, 0) — обычный flow; caller должен вызвать config.Load/cli.Run.
//
// Спека §2 (exit-коды и потоки):
//   - успешный help (с распознанным путём) → deps.Stdout, exit 0
//   - пустые args (no-args) → общий help в deps.Stderr, exit 1
//   - help с неизвестным путём → "неизвестная команда" + подсказка, deps.Stderr, exit 1
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

	// Нормализация leaf-путей (фикс UX-асимметрии для leaf-команд с positional):
	// `search foo --help` → computePath даст ["search","foo"], но search — leaf
	// без verb-уровня, её positional не должен расширять путь. Если path[0] —
	// известная leaf-команда (findSpec([path[0]]) != nil), усекаем path до [path[0]].
	// Для 2-уровневых команд (rooms/chat/reactions) findSpec([resource]) всегда
	// nil (paths в cmdSpecs — 2-токенные) → усечения не происходит, "rooms list"
	// сохраняется.
	if len(path) > 1 && findSpec(path[:1]) != nil {
		path = path[:1]
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
