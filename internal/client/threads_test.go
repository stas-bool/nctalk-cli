package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// threadsTestServer поднимает httptest-сервер, отдавая содержимое указанной
// фикстуры на любой запрос (по образцу participantsTestServer). Фикстуры лежат
// в корневом testdata/ — путь ../../testdata/<fixture>.
func threadsTestServer(t *testing.T, fixture string) *httptest.Server {
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

// TestListThreads — фикстура реального формата (снята с живого сервера,
// Task 1): метод/путь/заголовки/query limit, декод всех полей Thread и
// first/last. Нормализованные значения фикстуры: корень 1234, ответ 1300,
// title «Фикстурный тред», roomToken tok-team.
func TestListThreads(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotOCS, gotAccept, gotLimit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotOCS = r.Header.Get("OCS-APIRequest")
		gotAccept = r.Header.Get("Accept")
		gotLimit = r.URL.Query().Get("limit")
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "threads_recent.json"))
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
	tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}

	// Запрос: GET chat/{token}/threads/recent?limit=N + OCS-заголовки.
	if gotMethod != http.MethodGet {
		t.Errorf("method: got %q, want GET", gotMethod)
	}
	if want := "/ocs/v2.php/apps/spreed/api/v1/chat/tok-team/threads/recent"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
	}
	if gotLimit != "20" {
		t.Errorf("limit: got %q, want %q", gotLimit, "20")
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

	// Декод: хотя бы один тред; первый — с нормализованными значениями фикстуры.
	if len(tsInfo) < 1 {
		t.Fatalf("len(tsInfo): got %d, want >= 1", len(tsInfo))
	}
	first := tsInfo[0]
	if first.Thread.Id != 1234 || first.Thread.RoomToken != "tok-team" ||
		first.Thread.LastMessageId != 1300 || first.Thread.Title != "Фикстурный тред" {
		t.Errorf("tsInfo[0].Thread: got %+v, want 1234/tok-team/1300/Фикстурный тред", first.Thread)
	}
	if first.Thread.NumReplies < 1 {
		t.Errorf("tsInfo[0].Thread.NumReplies: got %d, want >= 1 (фикстура снята после ответа в тред)", first.Thread.NumReplies)
	}
	if first.Thread.LastActivity <= 0 {
		t.Errorf("tsInfo[0].Thread.LastActivity: got %d, want > 0 (Unix-секунды)", first.Thread.LastActivity)
	}
	if first.Attendee.NotificationLevel < 0 || first.Attendee.NotificationLevel > 3 {
		t.Errorf("tsInfo[0].Attendee.NotificationLevel: got %d, want 0..3", first.Attendee.NotificationLevel)
	}
	if first.First == nil || first.Last == nil {
		t.Fatalf("tsInfo[0]: first/last должны декодироваться (в фикстуре не null); got first=%v last=%v", first.First, first.Last)
	}
	if first.First.Id != 1234 || first.Last.Id != 1300 {
		t.Errorf("tsInfo[0]: first.Id=%d last.Id=%d, want 1234/1300", first.First.Id, first.Last.Id)
	}
}

// TestListThreads_NullableFirstLast — first/last == null (корень истёк/удалён,
// пустой тред) → nil-указатели, НЕ ошибка декодирования. Контракт nullable из
// §3.1; живой захват null-случая не воспроизводим (корень не истёк) — тело
// собрано по этому контракту, структура элемента идентична живой фикстуре.
func TestListThreads_NullableFirstLast(t *testing.T) {
	const body = `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[` +
		`{"thread":{"id":1234,"roomToken":"tok-team","lastMessageId":1300,"numReplies":1,` +
		`"lastActivity":1760000000,"title":"Фикстурный тред"},` +
		`"attendee":{"notificationLevel":0},"first":null,"last":null}]}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(tsInfo) != 1 {
		t.Fatalf("len: got %d, want 1", len(tsInfo))
	}
	if tsInfo[0].First != nil || tsInfo[0].Last != nil {
		t.Errorf("first/last: got %v/%v, want nil-указатели при null", tsInfo[0].First, tsInfo[0].Last)
	}
}

// TestListThreads_PathEscape — token с пробелом эскейпится в path-сегменте
// при сериализации URL в transport.DoOCS (клиент НЕ делает предварительный
// url.PathEscape — тот дал бы двойной эскейп, инвариант GetParticipants):
// запрос уходит как /chat/tok%20team/threads/recent.
func TestListThreads_PathEscape(t *testing.T) {
	var gotEscaped string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscaped = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`))
	}))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.ListThreads(context.Background(), "tok team", 20); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if want := "/chat/tok%20team/threads/recent"; !strings.HasSuffix(gotEscaped, want) {
		t.Errorf("escaped path: got %q, want suffix %q", gotEscaped, want)
	}
}

// TestListThreads_EmptyData — data:[] И data:null → пустой non-nil слайс,
// nil error (спека §4; «null» проходит len-guard doOCS и обнуляет слайс —
// guard переинициализирует, прецедент GetParticipants).
func TestListThreads_EmptyData(t *testing.T) {
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
		tsInfo, err := c.ListThreads(context.Background(), "tok-team", 20)
		if err != nil {
			t.Fatalf("data %q: got err %v, want nil (пусто — НЕ ошибка)", body, err)
		}
		if tsInfo == nil {
			t.Fatalf("data %q: got nil слайс, want non-nil пустой", body)
		}
		if len(tsInfo) != 0 {
			t.Errorf("data %q: len = %d, want 0", body, len(tsInfo))
		}
		ts.Close()
	}
}

// TestListThreads_NotFound — OCS 404 → *client.OCSError{Code:404}
// (cli-слой маппит в exit 2).
func TestListThreads_NotFound(t *testing.T) {
	ts := newOCSServer(t, 404, "room not found")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	_, err := c.ListThreads(context.Background(), "no-such", 20)
	if err == nil {
		t.Fatal("ListThreads: got nil err, want *OCSError{Code:404}")
	}
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != 404 {
		t.Fatalf("err: got %v, want *OCSError{Code:404}", err)
	}
}

// TestListThreads_MessageParamsObject — инвариант CLAUDE.md в уточнённой
// формулировке живого сервера (захваты Task 1): в chat-API messageParameters
// у сообщений БЕЗ параметров — пустой массив [] (PHP-сериализация пустых
// параметров, в т.ч. у first/last в threads/recent), у НЕПУСТЫХ параметров —
// ОБЪЕКТ; непустой массив [{...}] — формат /v4/room lastMessage, в chat-API
// недопустим. Декодер (MsgParams) трактует оба формата без ошибки, поэтому
// проверяем по сырому JSON живых фикстур (drift-guard).
func TestListThreads_MessageParamsObject(t *testing.T) {
	// 1) Ни одна живая фикстура тредов не содержит НЕПУСТОЙ массив
	// messageParameters — его появление означало бы format-drift к формату
	// /v4/room (массив), в chat-API запрещённому.
	fixtures := []string{"threads_recent.json", "chat_thread_history.json", "chat_send_thread_reply.json"}
	for _, fixture := range fixtures {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", fixture))
		if err != nil {
			t.Fatalf("read fixture %s: %v", fixture, err)
		}
		// Проверка нечувствительна к форматированию фикстуры: пробел после
		// двоеточия не гарантирован (ручная правка или jq-compact дадут
		// "messageParameters":[ без пробела) — сравниваем по строке без пробелов.
		s := strings.ReplaceAll(string(raw), " ", "")
		if strings.Contains(s, `"messageParameters":[{`) {
			t.Errorf("фикстура %s: непустой messageParameters-массив в chat-API запрещён (формат /v4/room, инвариант CLAUDE.md)", fixture)
		}
	}

	// 2) Объектная форма представлена в живом захвате: system-сообщение
	// thread_created в выдаче треда несёт непустые параметры (actor, title) —
	// непустые параметры сервер отдаёт объектом.
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "chat_thread_history.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if s := strings.ReplaceAll(string(raw), " ", ""); !strings.Contains(s, `"messageParameters":{`) {
		t.Errorf("фикстура chat_thread_history: ожидается объектный messageParameters у system thread_created (непустые параметры — объект)")
	}
}

// TestChatThreadHistory_Decode — фикстура реального формата chat?threadId=N
// (спека §3.2/§6): корень в выдаче (threadId == id, threadReplies >= 1,
// threadTitle непуст), ответ несёт threadId корня (инварианты §1).
func TestChatThreadHistory_Decode(t *testing.T) {
	ts := threadsTestServer(t, "chat_thread_history.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok-team", GetChatOpts{Limit: 50, ThreadId: 1234})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	var root, reply *Message
	for i := range msgs {
		if msgs[i].ThreadId == msgs[i].Id && msgs[i].ThreadReplies >= 1 && root == nil {
			root = &msgs[i]
		}
		if msgs[i].ThreadId != msgs[i].Id && reply == nil {
			reply = &msgs[i]
		}
	}
	if root == nil {
		t.Fatal("в выдаче треда нет корня (threadId == id && threadReplies >= 1) — инвариант §1 нарушен")
	}
	if reply == nil {
		t.Fatal("в выдаче треда нет ответа (threadId != id)")
	}
	if reply.ThreadId != root.Id {
		t.Errorf("ответ: ThreadId=%d, want id корня %d", reply.ThreadId, root.Id)
	}
	if root.ThreadTitle == "" {
		t.Errorf("корень: ThreadTitle пуст (сервер должен загрузить объект треда, §3.4)")
	}
}

// TestMessage_ThreadFieldsOptional — threadTitle/threadReplies отсутствуют в
// JSON (quote-ответ без треда, §3.4) → нулевые значения; присутствуют →
// декодируются. omitempty — про сериализацию, тут проверяем десериализацию.
func TestMessage_ThreadFieldsOptional(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"id":7,"threadId":7,"message":"x"}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.ThreadTitle != "" || m.ThreadReplies != 0 {
		t.Errorf("поля отсутствуют: got ThreadTitle=%q ThreadReplies=%d, want \"\"/0", m.ThreadTitle, m.ThreadReplies)
	}

	var m2 Message
	if err := json.Unmarshal([]byte(`{"id":7,"threadId":7,"threadTitle":"Тема","threadReplies":3}`), &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m2.ThreadTitle != "Тема" || m2.ThreadReplies != 3 {
		t.Errorf("поля присутствуют: got ThreadTitle=%q ThreadReplies=%d, want Тема/3", m2.ThreadTitle, m2.ThreadReplies)
	}
}

// TestSendMessage_ThreadReplyResponse — фикстура реального ответа POST chat
// с threadId (Task 1): клиент разбирает только id, существующий разбор не
// меняется (§3.3 — новых полей разбирать не нужно).
func TestSendMessage_ThreadReplyResponse(t *testing.T) {
	ts := threadsTestServer(t, "chat_send_thread_reply.json")
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	id, err := c.SendMessage(context.Background(), "tok-team", SendMessageOpts{
		Message:  "nctalk threads fixture — ответ",
		ThreadId: 1234,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id != 1300 {
		t.Errorf("id: got %d, want 1300 (нормализованный id ответа фикстуры)", id)
	}
}
