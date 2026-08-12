package cli

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strings"
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
// примет без ошибки парсинга (например, --type 2, --last 5, --reply-to 1).
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
