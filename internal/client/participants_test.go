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
// при сериализации URL в transport.DoOCS (клиент НЕ делает предварительный
// url.PathEscape — тот дал бы двойной эскейп): запрос уходит как
// /room/tok%20team/participants.
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
