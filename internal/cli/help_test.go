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
