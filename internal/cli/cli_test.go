package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// errMock — маркер: ни один stub-handler сейчас не должен доходить до клиентских
// вызовов. Если mockTalkClient что-то вернул — значит handler попытался звать
// клиент (не должен на стадии stub'ов).
var errMock = errors.New("mock: client не должен вызываться из stub-handler'ов")

// mockTalkClient — заглушка TalkClient для тестов cli-слоя. Все методы
// возвращают errMock: stub-handler'ы Task 4.1 не должны звать клиент вообще.
type mockTalkClient struct{}

func (m *mockTalkClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	return nil, errMock
}
func (m *mockTalkClient) FindRooms(_ context.Context, _, _ string) ([]client.Room, error) {
	return nil, errMock
}
func (m *mockTalkClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	return nil, errMock
}
func (m *mockTalkClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	return nil, errMock
}
func (m *mockTalkClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	return 0, errMock
}
func (m *mockTalkClient) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
func (m *mockTalkClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	return nil, errMock
}
func (m *mockTalkClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
func (m *mockTalkClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	return nil, errMock
}

// compile-time проверка: mockTalkClient реализует TalkClient.
var _ TalkClient = (*mockTalkClient)(nil)

// newTestDeps — Deps с mock-клиентом и буферами для Stdout/Stderr. Now зафиксирован
// (чтобы тесты не зависели от реального времени).
func newTestDeps() Deps {
	return Deps{
		Client: &mockTalkClient{},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	}
}

// TestRunRoutesAllStubs — все 8 команд доходят до своих stub-handler'ов и
// возвращают ExitGeneric (1). Проверяет базовую маршрутизацию по таблице routes
// и special-case search.
func TestRunRoutesAllStubs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"rooms list", []string{"rooms", "list"}},
		{"rooms find", []string{"rooms", "find", "foo"}},
		{"rooms search", []string{"rooms", "search", "foo"}},
		{"chat show", []string{"chat", "show", "tok"}},
		{"chat send", []string{"chat", "send", "tok", "--text", "hi"}},
		{"chat edit", []string{"chat", "edit", "tok", "1", "--text", "hi"}},
		{"reactions get", []string{"reactions", "get", "tok", "1"}},
		{"search (special-case)", []string{"search", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newTestDeps()
			code := Run(tc.args, deps)
			if code != ExitGeneric {
				t.Fatalf("Run(%v) code: got %d, want %d (ExitGeneric, т.к. handler — stub)", tc.args, code, ExitGeneric)
			}
			// Stub пишет текст "not implemented" в stderr через invokeHandler.
			stderr := deps.Stderr.(*bytes.Buffer).String()
			if stderr == "" {
				t.Fatalf("Run(%v): ожидался текст ошибки stub'а в stderr, получил пусто", tc.args)
			}
		})
	}
}

// spyExitError — sentinel-возврат от spy-handler'а, чтобы отличить его от
// случайного совпадения кода.
var spyExitError = ExitError{Code: ExitOK, Err: nil}

// installSpy подменяет handler handlerName в таблице routes (или searchHandlerFn
// при handlerName="search") на шпиона, который записывает args/jsonOut и
// возвращает spyExitError. Возвращает restore-функцию для defer.
func installSpy(t *testing.T, handlerName string) (gotArgs *[]string, gotJSON *bool, restore func()) {
	args := []string{}
	js := false
	spy := func(_ context.Context, _ Deps, a []string, j bool) ExitError {
		// Копируем, чтобы позже модификации среза caller'ом не меняли запись.
		args = append([]string(nil), a...)
		js = j
		return spyExitError
	}

	if handlerName == "search" {
		orig := searchHandlerFn
		searchHandlerFn = spy
		return &args, &js, func() { searchHandlerFn = orig }
	}

	// Парсим "rooms/list" → resource=rooms, verb=list.
	resource, verb, ok := splitHandlerName(handlerName)
	if !ok {
		t.Fatalf("installSpy: неизвестное имя handler'а %q", handlerName)
	}
	verbMap, ok := routes[resource]
	if !ok {
		t.Fatalf("installSpy: неизвестный resource %q", resource)
	}
	orig := verbMap[verb]
	verbMap[verb] = spy
	return &args, &js, func() { routes[resource][verb] = orig }
}

// splitHandlerName разбирает "rooms/list" → ("rooms", "list").
func splitHandlerName(name string) (string, string, bool) {
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			return name[:i], name[i+1:], true
		}
	}
	return "", "", false
}

// TestRunRoutesToCorrectHandler — подменяем каждый handler на шпиона и
// проверяем, что Run вызывает именно его (стаб spy возвращает ExitOK=0,
// любой другой handler вернул бы ExitGeneric=1). Гарантирует корректность
// отображения resource/verb → handler.
func TestRunRoutesToCorrectHandler(t *testing.T) {
	cases := []struct {
		name       string // имя spy (handler)
		args       []string
		wantArgs   []string // ожидаемые args, переданные handler'у
		wantArgsAs string  // когда wantArgs не задан, только факт вызова
	}{
		{"rooms/list", []string{"rooms", "list"}, []string{}, ""},
		{"rooms/find", []string{"rooms", "find", "foo"}, []string{"foo"}, ""},
		{"rooms/search", []string{"rooms", "search", "bar"}, []string{"bar"}, ""},
		{"chat/show", []string{"chat", "show", "TOK123"}, []string{"TOK123"}, ""},
		{"chat/send", []string{"chat", "send", "TOK123", "--text", "hi"}, []string{"TOK123", "--text", "hi"}, ""},
		{"chat/edit", []string{"chat", "edit", "TOK123", "42"}, []string{"TOK123", "42"}, ""},
		{"reactions/get", []string{"reactions", "get", "TOK123", "42"}, []string{"TOK123", "42"}, ""},
		{"search", []string{"search", "baz"}, []string{"baz"}, ""}, // special-case: rest передаётся целиком
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, _, restore := installSpy(t, tc.name)
			defer restore()

			deps := newTestDeps()
			code := Run(tc.args, deps)
			// Spy возвращает ExitOK=0. Если Run выбрал не тот handler — код будет
			// ExitGeneric=1 (от stub'а) и gotArgs останется пустым.
			if code != ExitOK {
				t.Fatalf("Run(%v): spy не вызвался; code: got %d, want %d", tc.args, code, ExitOK)
			}
			if !equalStrings(*gotArgs, tc.wantArgs) {
				t.Fatalf("Run(%v) args handler'а: got %v, want %v", tc.args, *gotArgs, tc.wantArgs)
			}
		})
	}
}

// TestRunJSONParsedAnywhere — глобальный --json извлекается из любой позиции и
// доходит до handler'а как jsonOut=true. Варианты: в начале/посередине/в конце.
func TestRunJSONParsedAnywhere(t *testing.T) {
	cases := []struct {
		name     string
		handler  string
		args     []string
		wantArgs []string
	}{
		{"rooms: --json в конце", "rooms/list", []string{"rooms", "list", "--json"}, []string{}},
		{"rooms: --json в начале (до verb)", "rooms/list", []string{"rooms", "--json", "list"}, []string{}},
		{"rooms: --json в середине args", "rooms/find", []string{"rooms", "find", "--json", "foo"}, []string{"foo"}},
		{"chat: --json в конце", "chat/show", []string{"chat", "show", "tok", "--json"}, []string{"tok"}},
		{"search: --json в конце", "search", []string{"search", "foo", "--json"}, []string{"foo"}},
		{"search: --json в начале (до term)", "search", []string{"search", "--json", "foo"}, []string{"foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotJSON, restore := installSpy(t, tc.handler)
			defer restore()

			deps := newTestDeps()
			code := Run(tc.args, deps)
			if code != ExitOK {
				t.Fatalf("Run(%v): spy не вызвался; code: got %d, want %d", tc.args, code, ExitOK)
			}
			if !*gotJSON {
				t.Fatalf("Run(%v): jsonOut: got false, want true", tc.args)
			}
			if !equalStrings(*gotArgs, tc.wantArgs) {
				t.Fatalf("Run(%v) args handler'а: got %v, want %v", tc.args, *gotArgs, tc.wantArgs)
			}
		})
	}
}

// TestRunJSONAbsent — без --json handler получает jsonOut=false.
func TestRunJSONAbsent(t *testing.T) {
	gotArgs, gotJSON, restore := installSpy(t, "rooms/list")
	defer restore()

	deps := newTestDeps()
	code := Run([]string{"rooms", "list"}, deps)
	if code != ExitOK {
		t.Fatalf("Run: spy не вызвался; code: got %d, want %d", code, ExitOK)
	}
	if *gotJSON {
		t.Fatalf("jsonOut: got true, want false (--json не передавался)")
	}
	if len(*gotArgs) != 0 {
		t.Fatalf("args handler'а: got %v, want empty", *gotArgs)
	}
}

// TestRunSearchSpecialCaseNoVerbLevel — `search` НЕ использует verb-уровень:
// вся часть после `search` (term + флаги) передаётся как args целиком. Если бы
// роутер трактовал search как двухуровневый (verb=первый аргумент), первый
// аргумент ("foo") был бы съеден как verb и не попал бы в args handler'а.
func TestRunSearchSpecialCaseNoVerbLevel(t *testing.T) {
	gotArgs, _, restore := installSpy(t, "search")
	defer restore()

	deps := newTestDeps()
	code := Run([]string{"search", "foo", "--limit", "5"}, deps)
	if code != ExitOK {
		t.Fatalf("Run: search spy не вызвался; code: got %d, want %d", code, ExitOK)
	}
	want := []string{"foo", "--limit", "5"}
	if !equalStrings(*gotArgs, want) {
		t.Fatalf("Run(search ...) args: got %v, want %v (весь хвост без verb-уровня)", *gotArgs, want)
	}
}

// TestRunUnknownCommand — неизвестный resource → exit 1 (ExitGeneric).
func TestRunUnknownCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown resource", []string{"unknown"}},
		{"unknown resource с args", []string{"unknown", "foo", "bar"}},
		{"пустой args", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newTestDeps()
			code := Run(tc.args, deps)
			if code != ExitGeneric {
				t.Fatalf("Run(%v) code: got %d, want %d", tc.args, code, ExitGeneric)
			}
			stderr := deps.Stderr.(*bytes.Buffer).String()
			if stderr == "" {
				t.Fatalf("Run(%v): ожидалось сообщение об ошибке в stderr", tc.args)
			}
		})
	}
}

// TestRunUnknownVerb — известный resource, неизвестный verb → exit 1.
func TestRunUnknownVerb(t *testing.T) {
	deps := newTestDeps()
	code := Run([]string{"rooms", "weirdverb"}, deps)
	if code != ExitGeneric {
		t.Fatalf("Run code: got %d, want %d", code, ExitGeneric)
	}
}

// TestRunMissingVerb — известный resource без verb → exit 1 (rooms/chat/reactions
// требуют verb).
func TestRunMissingVerb(t *testing.T) {
	for _, resource := range []string{"rooms", "chat", "reactions"} {
		t.Run(resource, func(t *testing.T) {
			deps := newTestDeps()
			code := Run([]string{resource}, deps)
			if code != ExitGeneric {
				t.Fatalf("Run(%q) code: got %d, want %d", resource, code, ExitGeneric)
			}
		})
	}
}

// TestRunNilNowDefaultsToTimeNow — если deps.Now == nil, Run не падает и
// подставляет time.Now (требование спецификации).
func TestRunNilNowDefaultsToTimeNow(t *testing.T) {
	deps := Deps{
		Client: &mockTalkClient{},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    nil, // Run должен подставить time.Now сам
	}
	// На stub-handler'е это не паникует и возвращает ExitGeneric.
	code := Run([]string{"rooms", "list"}, deps)
	if code != ExitGeneric {
		t.Fatalf("Run с nil Now: code: got %d, want %d", code, ExitGeneric)
	}
}

// TestExtractJSON — unit-тест для extractJSON: --json вырезается из любой
// позиции, порядок остальных args сохраняется.
func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantArgs []string
		wantJSON bool
	}{
		{"нет --json", []string{"rooms", "list"}, []string{"rooms", "list"}, false},
		{"--json в конце", []string{"rooms", "list", "--json"}, []string{"rooms", "list"}, true},
		{"--json в начале", []string{"--json", "rooms", "list"}, []string{"rooms", "list"}, true},
		{"--json посередине", []string{"rooms", "--json", "list"}, []string{"rooms", "list"}, true},
		{"два --json", []string{"--json", "rooms", "list", "--json"}, []string{"rooms", "list"}, true},
		{"только --json", []string{"--json"}, []string{}, true},
		{"пустой args", []string{}, []string{}, false},
		{"--json=false НЕ поддерживается (проходит как обычный arg)", []string{"--json=false"}, []string{"--json=false"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotJSON := extractJSON(tc.in)
			if gotJSON != tc.wantJSON {
				t.Errorf("jsonOut: got %v, want %v", gotJSON, tc.wantJSON)
			}
			if !equalStrings(gotArgs, tc.wantArgs) {
				t.Errorf("args: got %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

// equalStrings — сравнение слайсов строк; nil и []string{} считаются равными.
func equalStrings(a, b []string) bool {
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
