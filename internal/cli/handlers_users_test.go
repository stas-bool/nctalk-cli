package cli

// handlers_users_test.go — тесты usersShowHandler (спека-дельта 2026-10-22
// `users show`, §2, §5, §6).
//
// Spy-клиент (usersSpyClient) записывает userId вызовов GetProfile и
// возвращает преднастроенные результат/ошибку. Остальные методы TalkClient
// возвращают errMock (users-тестам не нужны).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// usersSpyClient — заглушка TalkClient для users-тестов.
type usersSpyClient struct {
	profileCalls  []string // userId каждого вызова GetProfile
	profileResult *client.Profile
	profileErr    error
}

func (m *usersSpyClient) GetProfile(_ context.Context, userId string) (*client.Profile, error) {
	m.profileCalls = append(m.profileCalls, userId)
	return m.profileResult, m.profileErr
}

// Неиспользуемые в users-тестах методы — возвращают errMock (маркер).
func (m *usersSpyClient) ListRooms(_ context.Context, _ client.ListRoomsOpts) ([]client.Room, error) {
	return nil, errMock
}
func (m *usersSpyClient) FindRooms(_ context.Context, _, _ string) ([]client.Room, error) {
	return nil, errMock
}
func (m *usersSpyClient) SearchRooms(_ context.Context, _ string, _ int) ([]client.ConversationResult, error) {
	return nil, errMock
}
func (m *usersSpyClient) GetChat(_ context.Context, _ string, _ client.GetChatOpts) ([]client.Message, error) {
	return nil, errMock
}
func (m *usersSpyClient) SendMessage(_ context.Context, _ string, _ client.SendMessageOpts) (int, error) {
	return 0, errMock
}
func (m *usersSpyClient) SendFile(_ context.Context, _ string, _ client.FileUpload, _ client.SendFileOpts) (client.SendFileResult, error) {
	return client.SendFileResult{}, errMock
}
func (m *usersSpyClient) EditMessage(_ context.Context, _ string, _ int, _ client.EditMessageOpts) (int, error) {
	return 0, errMock
}
func (m *usersSpyClient) GetReactions(_ context.Context, _ string, _ int) (map[string][]client.ReactionActor, error) {
	return nil, errMock
}
func (m *usersSpyClient) GetParticipants(_ context.Context, _ string) ([]client.Participant, error) {
	return nil, errMock
}
func (m *usersSpyClient) ListThreads(_ context.Context, _ string, _ int) ([]client.ThreadInfo, error) {
	return nil, errMock
}
func (m *usersSpyClient) SearchMessages(_ context.Context, _ string, _ client.SearchMessagesOpts) ([]client.MessageResult, error) {
	return nil, errMock
}

// compile-time гарантия: usersSpyClient реализует TalkClient.
var _ TalkClient = (*usersSpyClient)(nil)

// newUsersDeps — Deps с переданным клиентом, буферами и фиксированным Now
// (08:41 UTC — местное время пояса проверяемо).
func newUsersDeps(c TalkClient) Deps {
	return Deps{
		Client: c,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 10, 22, 8, 41, 0, 0, time.UTC) },
	}
}

// profileFixture — профиль для handler-тестов (обезличенный).
func profileFixture() *client.Profile {
	return &client.Profile{
		UserId:         "ivan.p",
		DisplayName:    "Иван Петров",
		Role:           "Генеральный директор",
		Address:        "Калининград",
		Timezone:       "Europe/Kaliningrad",
		TimezoneOffset: 7200,
		Contacts:       []client.ProfileContact{{Type: "email", Value: "ivan.p@example.org"}},
	}
}

// TestUsersShowHandlerText — успех, текст: userId уходит в клиент как есть,
// блок профиля с местным временем от deps.Now.
func TestUsersShowHandlerText(t *testing.T) {
	spy := &usersSpyClient{profileResult: profileFixture()}
	deps := newUsersDeps(spy)

	ee := usersShowHandler(context.Background(), deps, []string{"ivan.p"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.profileCalls) != 1 || spy.profileCalls[0] != "ivan.p" {
		t.Fatalf("GetProfile: got %v, want [ivan.p]", spy.profileCalls)
	}
	out := deps.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{
		"Иван Петров", "ivan.p", "Генеральный директор", "Калининград",
		"Europe/Kaliningrad (UTC+2), сейчас 2026-10-22 10:41",
		"email:", "ivan.p@example.org",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout не содержит %q:\n%s", want, out)
		}
	}
}

// TestUsersShowHandlerJSON — успех, --json: каноническая модель, contacts —
// массив; местное время в JSON не вычисляется.
func TestUsersShowHandlerJSON(t *testing.T) {
	spy := &usersSpyClient{profileResult: profileFixture()}
	deps := newUsersDeps(spy)

	ee := usersShowHandler(context.Background(), deps, []string{"ivan.p"}, true)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	var got client.Profile
	out := deps.Stdout.(*bytes.Buffer).Bytes()
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout не JSON: %v\n%s", err, out)
	}
	if got.UserId != "ivan.p" || got.TimezoneOffset != 7200 || len(got.Contacts) != 1 {
		t.Errorf("JSON: got %+v", got)
	}
	if strings.Contains(string(out), "сейчас") {
		t.Errorf("местное время в JSON не выводится: %s", out)
	}
}

// TestUsersShowHandlerNoUserId — нет userId (нет аргументов или пустая
// строка) → exit 1 без вызова клиента.
func TestUsersShowHandlerNoUserId(t *testing.T) {
	for _, args := range [][]string{nil, {""}} {
		spy := &usersSpyClient{profileResult: profileFixture()}
		deps := newUsersDeps(spy)

		ee := usersShowHandler(context.Background(), deps, args, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("args %q: code: got %d, want %d", args, ee.Code, ExitGeneric)
		}
		if ee.Err == nil || ee.Err.Error() != "users show: укажите userId пользователя" {
			t.Fatalf("args %q: err: got %v, want «users show: укажите userId пользователя»", args, ee.Err)
		}
		if len(spy.profileCalls) != 0 {
			t.Fatalf("args %q: GetProfile не должен вызываться; got %v", args, spy.profileCalls)
		}
	}
}

// TestUsersShowHandlerUnknownFlag — любой --* → exit 1 «неизвестный флаг»,
// клиент не звался (флагов у команды нет).
func TestUsersShowHandlerUnknownFlag(t *testing.T) {
	for _, args := range [][]string{{"ivan.p", "--name", "x"}, {"--user=ivan.p"}} {
		spy := &usersSpyClient{profileResult: profileFixture()}
		deps := newUsersDeps(spy)

		ee := usersShowHandler(context.Background(), deps, args, false)
		if ee.Code != ExitGeneric {
			t.Fatalf("args %q: code: got %d, want %d", args, ee.Code, ExitGeneric)
		}
		if ee.Err == nil || !strings.HasPrefix(ee.Err.Error(), "users show: неизвестный флаг") {
			t.Fatalf("args %q: err: got %v, want «users show: неизвестный флаг …»", args, ee.Err)
		}
		if len(spy.profileCalls) != 0 {
			t.Fatalf("args %q: GetProfile не должен вызываться; got %v", args, spy.profileCalls)
		}
	}
}

// TestUsersShowHandlerExtraPositionals — лишние позиционные игнорируются.
func TestUsersShowHandlerExtraPositionals(t *testing.T) {
	spy := &usersSpyClient{profileResult: profileFixture()}
	deps := newUsersDeps(spy)

	ee := usersShowHandler(context.Background(), deps, []string{"ivan.p", "extra", "more"}, false)
	if ee.Code != ExitOK {
		t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, ExitOK, ee.Err)
	}
	if len(spy.profileCalls) != 1 || spy.profileCalls[0] != "ivan.p" {
		t.Fatalf("GetProfile: got %v, want [ivan.p]", spy.profileCalls)
	}
}

// TestUsersShowHandlerErrors — маппинг ошибок клиента (спека-дельта §2):
// тексты с префиксом «users show:» (без «nctalk»), коды 1/2.
func TestUsersShowHandlerErrors(t *testing.T) {
	guardErr := errors.New(`client: GetProfile: недопустимый userId "a/b" (пустой, «.», «..» или содержит «/»)`)
	cases := []struct {
		name     string
		err      error
		wantExit int
		wantText string
	}{
		{"OCS 404 пустой Message → 2, не найден",
			&client.OCSError{Code: http.StatusNotFound, Message: ""},
			ExitNotFound, `users show: пользователь "ivan.p" не найден или недоступен`},
		{"404 с Message (нет маршрута) → 2, profile-API недоступен",
			&client.OCSError{Code: http.StatusNotFound, Message: "Invalid query, please check the syntax."},
			ExitNotFound, "users show: profile-API недоступен на сервере (нужен Nextcloud 31+): client: Invalid query"},
		{"OCS 400 → 1, профиль отключён",
			&client.OCSError{Code: http.StatusBadRequest, Message: ""},
			ExitGeneric, `users show: профиль "ivan.p" отключён`},
		{"OCS 500 → 1, текст сервера",
			&client.OCSError{Code: http.StatusInternalServerError, Message: "internal"},
			ExitGeneric, "client: internal"},
		{"HTTP 429 (decode-ошибка транспорта) → 1",
			errors.New("client: не удалось декодировать OCS-ответ (HTTP 429): EOF"),
			ExitGeneric, "HTTP 429"},
		{"guard клиента (не-OCS ошибка) → 1, текст клиента",
			guardErr,
			ExitGeneric, "недопустимый userId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &usersSpyClient{profileErr: tc.err}
			deps := newUsersDeps(spy)

			ee := usersShowHandler(context.Background(), deps, []string{"ivan.p"}, false)
			if ee.Code != tc.wantExit {
				t.Fatalf("code: got %d, want %d (err=%v)", ee.Code, tc.wantExit, ee.Err)
			}
			if ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.wantText) {
				t.Fatalf("err: got %v, want содержит %q", ee.Err, tc.wantText)
			}
			if strings.HasPrefix(ee.Err.Error(), "nctalk") {
				t.Errorf("err: префикс «nctalk» не нужен: %v", ee.Err)
			}
			if out := deps.Stdout.(*bytes.Buffer).String(); out != "" {
				t.Errorf("stdout при ошибке: got %q, want пусто", out)
			}
		})
	}
}
