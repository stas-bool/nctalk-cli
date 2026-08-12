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
