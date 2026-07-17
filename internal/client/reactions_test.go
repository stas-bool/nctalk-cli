package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// reactionsTestServer поднимает httptest-сервер, отдавая содержимое указанной
// фикстуры на любой запрос. Фикстуры лежат в корневом testdata/ (рядом с
// go.mod), поэтому из директории пакета путь — ../../testdata/<fixture>.
func reactionsTestServer(t *testing.T, fixture string) *httptest.Server {
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

// TestGetReactions проверяет, что непустой ответ (reactions.json) маппится в
// map[emoji][]ReactionActor со всеми полями актёров (ActorType/ActorId/
// ActorDisplayName/Timestamp), и что messageId подставляется в URL запроса
// (путь содержит /{messageId}).
func TestGetReactions(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "reactions.json"))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	const messageId = 8001
	reactions, err := c.GetReactions(context.Background(), "tok-team", messageId)
	if err != nil {
		t.Fatalf("GetReactions: %v", err)
	}

	// messageId должен быть в пути запроса как суффикс /{messageId}.
	if wantSuffix := "/" + strconv.Itoa(messageId); !strings.HasSuffix(gotPath, wantSuffix) {
		t.Errorf("request path: got %q, want suffix %q", gotPath, wantSuffix)
	}
	// token тоже должен присутствовать в пути.
	if !strings.Contains(gotPath, "/tok-team/") {
		t.Errorf("request path: got %q, want contains '/tok-team/'", gotPath)
	}

	// Ожидаем 2 эмодзи в фикстуре.
	if got, want := len(reactions), 2; got != want {
		t.Fatalf("len(reactions) = %d, want %d", got, want)
	}

	// 👍 — 2 актёра; проверяем первое (alice) на полное маппинг полей.
	thumbs, ok := reactions["👍"]
	if !ok {
		t.Fatal(`reactions["👍"] отсутствует`)
	}
	if got, want := len(thumbs), 2; got != want {
		t.Fatalf(`len(reactions["👍"]) = %d, want %d`, got, want)
	}
	a := thumbs[0]
	if a.ActorType != "users" || a.ActorId != "alice" || a.ActorDisplayName != "Alice" || a.Timestamp != 1700000000 {
		t.Errorf(`reactions["👍"][0]: got %+v, want {users alice Alice 1700000000}`, a)
	}

	// ❤️ — 1 актёр (guest), проверяем guest-формат actorId.
	hearts, ok := reactions["❤️"]
	if !ok {
		t.Fatal(`reactions["❤️"] отсутствует`)
	}
	if got, want := len(hearts), 1; got != want {
		t.Fatalf(`len(reactions["❤️"]) = %d, want %d`, got, want)
	}
	if hearts[0].ActorType != "guests" || hearts[0].ActorId != "guest::anon-42" {
		t.Errorf(`reactions["❤️"][0]: got %+v, want actorType=guests actorId=guest::anon-42`, hearts[0])
	}
}

// TestGetReactions_Empty проверяет, что пустой data={} (reactions_empty.json)
// возвращает пустую (non-nil) map и nil error — спека §6: exit 0, «реакций нет».
func TestGetReactions_Empty(t *testing.T) {
	ts := reactionsTestServer(t, "reactions_empty.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	reactions, err := c.GetReactions(context.Background(), "tok-team", 8001)
	if err != nil {
		t.Fatalf("GetReactions: got err %v, want nil (пустой data={} — НЕ ошибка)", err)
	}
	if reactions == nil {
		t.Fatal("reactions = nil, want non-nil пустая map")
	}
	if len(reactions) != 0 {
		t.Errorf("len(reactions) = %d, want 0", len(reactions))
	}
}
