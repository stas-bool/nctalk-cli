package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// genMessages собирает фикстуру из сообщений с id от start до end (включительно),
// упорядоченных от НОВОГО к СТАРОМУ (id и timestamp убывают). Timestamp = base+id,
// так что большему id соответствует больший timestamp (более свежее сообщение).
// Используется для программной генерации страниц chat-истории без testdata-файлов.
func genMessages(start, end int) []Message {
	const base = 1700000000
	msgs := make([]Message, 0, start-end+1)
	for id := start; id >= end; id-- {
		msgs = append(msgs, Message{
			Id:        id,
			Message:   fmt.Sprintf("msg-%d", id),
			Timestamp: base + int64(id),
			Token:     "tok",
			ActorType: "users",
			ActorId:   "alice",
		})
	}
	return msgs
}

// chatReq — слепок query+path одного запроса к chat-эндпоинту, для asserts.
type chatReq struct {
	path               string
	limit              string
	lookIntoFuture     string
	lastKnownMessageId string
}

// chatReqLog — потокобезопасный лог запросов, полученных тест-сервером.
type chatReqLog struct {
	mu   sync.Mutex
	reqs []chatReq
}

func (l *chatReqLog) record(r chatReq) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r)
}

func (l *chatReqLog) snapshot() []chatReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]chatReq, len(l.reqs))
	copy(out, l.reqs)
	return out
}

// chatPagesServer поднимает httptest-сервер, раздающий две страницы истории по
// lastKnownMessageId: пустой/0 → page1, "801" → page2, прочее → пустой массив
// (достигли начала чата). Возвращает сервер и лог запросов для проверок.
func chatPagesServer(t *testing.T, page1, page2 []Message) (*httptest.Server, *chatReqLog) {
	t.Helper()
	log := &chatReqLog{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		log.record(chatReq{
			path:               r.URL.Path,
			limit:              q.Get("limit"),
			lookIntoFuture:     q.Get("lookIntoFuture"),
			lastKnownMessageId: q.Get("lastKnownMessageId"),
		})
		w.Header().Set("Content-Type", "application/json")
		var data []Message
		switch q.Get("lastKnownMessageId") {
		case "", "0":
			data = page1
		case "801":
			data = page2
		default:
			data = nil
		}
		if data == nil {
			data = []Message{}
		}
		_, _ = w.Write(ocsBody(t, 200, "OK", data))
	})), log
}

// assertChatQueryCommon проверяет, что каждый запрос в логе несёт каноничные
// query-параметры (limit=200, lookIntoFuture=0) и стучится на путь нужного токена.
func assertChatQueryCommon(t *testing.T, reqs []chatReq) {
	t.Helper()
	for i, r := range reqs {
		if r.limit != "200" {
			t.Errorf("req[%d].limit: got %q, want %q", i, r.limit, "200")
		}
		if r.lookIntoFuture != "0" {
			t.Errorf("req[%d].lookIntoFuture: got %q, want %q", i, r.lookIntoFuture, "0")
		}
		if r.path != "/ocs/v2.php/apps/spreed/api/v1/chat/tok" {
			t.Errorf("req[%d].path: got %q, want /.../chat/tok", i, r.path)
		}
	}
}

// TestGetChat_SinglePage_TruncatesToLimit — потолок Limit=20: клиент тянет ОДНУ
// страницу (сервер отдаёт 200), но обрезает выборку до 20. Пагинация не нужна.
// Проверяем: ровно 1 запрос, первый без lastKnownMessageId, результат — срез
// [0:20] (id 1000..981).
func TestGetChat_SinglePage_TruncatesToLimit(t *testing.T) {
	ts, log := chatPagesServer(t, genMessages(1000, 801), genMessages(800, 751))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok", GetChatOpts{Limit: 20})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	reqs := log.snapshot()
	assertChatQueryCommon(t, reqs)
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d (Limit=20 → одна страница достаточно)", got, want)
	}
	if reqs[0].lastKnownMessageId != "" {
		t.Errorf("req[0].lastKnownMessageId: got %q, want пусто (старт с последнего)", reqs[0].lastKnownMessageId)
	}
	if got, want := len(msgs), 20; got != want {
		t.Fatalf("len(msgs): got %d, want %d", got, want)
	}
	if msgs[0].Id != 1000 {
		t.Errorf("msgs[0].Id: got %d, want 1000 (самое свежее)", msgs[0].Id)
	}
	if msgs[19].Id != 981 {
		t.Errorf("msgs[19].Id: got %d, want 981 (срез [0:20])", msgs[19].Id)
	}
}

// TestGetChat_TwoPages_RollsLastKnownMessageId — Limit=210: одна страница (200)
// не покрывает потолок, клиент прокручивает lastKnownMessageId=801 и тянет
// вторую страницу (50). Итог 250 → обрезаем до 210.
func TestGetChat_TwoPages_RollsLastKnownMessageId(t *testing.T) {
	ts, log := chatPagesServer(t, genMessages(1000, 801), genMessages(800, 751))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok", GetChatOpts{Limit: 210})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	reqs := log.snapshot()
	assertChatQueryCommon(t, reqs)
	if got, want := len(reqs), 2; got != want {
		t.Fatalf("запросов: got %d, want %d (Limit=210 → две страницы)", got, want)
	}
	if reqs[0].lastKnownMessageId != "" {
		t.Errorf("req[0].lastKnownMessageId: got %q, want пусто", reqs[0].lastKnownMessageId)
	}
	if reqs[1].lastKnownMessageId != "801" {
		t.Errorf("req[1].lastKnownMessageId: got %q, want %q (id самого старого из page1)",
			reqs[1].lastKnownMessageId, "801")
	}
	if got, want := len(msgs), 210; got != want {
		t.Fatalf("len(msgs): got %d, want %d", got, want)
	}
	if msgs[0].Id != 1000 {
		t.Errorf("msgs[0].Id: got %d, want 1000", msgs[0].Id)
	}
	if msgs[209].Id != 791 {
		t.Errorf("msgs[209].Id: got %d, want 791 (срез [0:210] от 1000)", msgs[209].Id)
	}
}

// TestGetChat_EarlyStop — StopBeforeTs между сообщениями page1: на первой же
// странице попадаем на сообщение строго старше порога, обрезаем выборку до него
// (не включая) и прекращаем пагинацию. Вторая страница НЕ запрашивается.
//
// Порог = timestamp(id=900) = 1700000900. Сообщения с timestamp < порога
// (id 899 и старше) отбрасываются; остаются id 1000..900 (101 штука).
func TestGetChat_EarlyStop(t *testing.T) {
	ts, log := chatPagesServer(t, genMessages(1000, 801), genMessages(800, 751))
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok", GetChatOpts{
		Limit:        0, // без потолка — пагинацию должен остановить ранний стоп
		StopBeforeTs: 1700000900,
	})
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	reqs := log.snapshot()
	assertChatQueryCommon(t, reqs)
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d (ранний стоп на page1 → без page2)", got, want)
	}
	if got, want := len(msgs), 101; got != want {
		t.Fatalf("len(msgs): got %d, want %d (id 1000..900)", got, want)
	}
	if msgs[0].Id != 1000 {
		t.Errorf("msgs[0].Id: got %d, want 1000", msgs[0].Id)
	}
	if msgs[100].Id != 900 {
		t.Errorf("msgs[100].Id: got %d, want 900 (последнее перед порогом)", msgs[100].Id)
	}
	for _, m := range msgs {
		if m.Timestamp < 1700000900 {
			t.Errorf("msgs содержит сообщение строго старше порога: id=%d ts=%d", m.Id, m.Timestamp)
		}
	}
}

// TestGetChat_EmptyChat — пустой чат: сервер отдаёт [] на первый запрос.
// Контракт: пустой срез, nil error, ровно 1 запрос (НЕ зацикливаемся).
func TestGetChat_EmptyChat(t *testing.T) {
	ts, log := chatPagesServer(t, []Message{}, nil)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	msgs, err := c.GetChat(context.Background(), "tok", GetChatOpts{Limit: 0})
	if err != nil {
		t.Fatalf("GetChat: got error %v, want nil", err)
	}
	reqs := log.snapshot()
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d (пустой чат → один запрос, без цикла)", got, want)
	}
	if msgs == nil {
		t.Fatal("msgs = nil, want non-nil пустой срез")
	}
	if got, want := len(msgs), 0; got != want {
		t.Fatalf("len(msgs): got %d, want %d", got, want)
	}
}

// chatSendReq — слепок одного POST-запроса к chat-эндпоинту, для asserts SendMessage.
type chatSendReq struct {
	method string
	path   string
	body   string
}

// chatSendReqLog — потокобезопасный лог POST-запросов к chat-эндпоинту.
type chatSendReqLog struct {
	mu   sync.Mutex
	reqs []chatSendReq
}

func (l *chatSendReqLog) record(r chatSendReq) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r)
}

func (l *chatSendReqLog) snapshot() []chatSendReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]chatSendReq, len(l.reqs))
	copy(out, l.reqs)
	return out
}

// chatSendServer поднимает httptest-сервер, отвечающий на POST chat фиксированным
// OCS-ответом (status/msg/idResp в data.id) и логирующий тела запросов. Возвращает
// сервер и лог для asserts. Один сервер — один сценарий (см. отдельные тесты).
func chatSendServer(t *testing.T, status int, msg string, idResp int) (*httptest.Server, *chatSendReqLog) {
	t.Helper()
	log := &chatSendReqLog{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		log.record(chatSendReq{method: r.Method, path: r.URL.Path, body: string(body)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(ocsBody(t, status, msg, map[string]any{"id": idResp}))
	})), log
}

// TestSendMessage_Success — сервер ответил 200 c data.id=12345 → функция вернула
// 12345; тело запроса — валидный JSON с обязательными message и silent.
// silent=true intentionally — проверяем, что флаг кодируется в теле. replyTo при
// ReplyTo=0 НЕ должен попасть в тело (omitempty).
func TestSendMessage_Success(t *testing.T) {
	ts, log := chatSendServer(t, 200, "OK", 12345)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	id, err := c.SendMessage(context.Background(), "tok", SendMessageOpts{
		Message: "hello",
		Silent:  true,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got, want := id, 12345; got != want {
		t.Errorf("id: got %d, want %d", got, want)
	}
	reqs := log.snapshot()
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d", got, want)
	}
	r := reqs[0]
	if r.method != http.MethodPost {
		t.Errorf("method: got %q, want POST", r.method)
	}
	const wantPath = "/ocs/v2.php/apps/spreed/api/v1/chat/tok"
	if r.path != wantPath {
		t.Errorf("path: got %q, want %q", r.path, wantPath)
	}
	// message и silent — обязательные поля тела (спека §6 chat send).
	if !strings.Contains(r.body, `"message":"hello"`) {
		t.Errorf("body: нет \"message\":\"hello\": %q", r.body)
	}
	if !strings.Contains(r.body, `"silent":true`) {
		t.Errorf("body: нет \"silent\":true: %q", r.body)
	}
	// replyTo с omitempty при ReplyTo=0 должен отсутствовать.
	if strings.Contains(r.body, `"replyTo"`) {
		t.Errorf("body: replyTo должен отсутствовать при ReplyTo=0: %q", r.body)
	}
}

// TestSendMessage_WithReplyTo — ReplyTo=42 → тело содержит "replyTo":42.
// Также проверяем, что message сохраняется.
func TestSendMessage_WithReplyTo(t *testing.T) {
	ts, log := chatSendServer(t, 200, "OK", 99)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	if _, err := c.SendMessage(context.Background(), "tok", SendMessageOpts{
		Message: "reply-please",
		ReplyTo: 42,
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := log.snapshot()
	if got, want := len(reqs), 1; got != want {
		t.Fatalf("запросов: got %d, want %d", got, want)
	}
	body := reqs[0].body
	if !strings.Contains(body, `"replyTo":42`) {
		t.Errorf("body: нет \"replyTo\":42: %q", body)
	}
	if !strings.Contains(body, `"message":"reply-please"`) {
		t.Errorf("body: нет message: %q", body)
	}
}

// TestSendMessage_EmptyMessage_ClientError — пустой Message → клиентская ошибка
// ДО сетевого вызова. Счётчик запросов на тест-сервере = 0 (сеть не дёргаем).
func TestSendMessage_EmptyMessage_ClientError(t *testing.T) {
	ts, log := chatSendServer(t, 200, "OK", 1)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	_, err := c.SendMessage(context.Background(), "tok", SendMessageOpts{Message: ""})
	if err == nil {
		t.Fatal("err = nil, want client error for empty Message")
	}
	reqs := log.snapshot()
	if got, want := len(reqs), 0; got != want {
		t.Errorf("запросов: got %d, want %d (пустое message → сеть не дёргаем)", got, want)
	}
}

// TestSendMessage_OCSError — сервер вернул OCS-error (meta.statusCode=400,
// message="invalid replyTo") → функция возвращает ошибку, содержащую этот текст.
// Стандартная обработка doOCS (спека §9): meta.message прокидывается в err.
func TestSendMessage_OCSError(t *testing.T) {
	ts, _ := chatSendServer(t, 400, "invalid replyTo", 0)
	defer ts.Close()

	c := NewTalkClient(testCfg(ts.URL))
	_, err := c.SendMessage(context.Background(), "tok", SendMessageOpts{
		Message: "x",
		ReplyTo: 999,
	})
	if err == nil {
		t.Fatal("err = nil, want error containing 'invalid replyTo'")
	}
	if !strings.Contains(err.Error(), "invalid replyTo") {
		t.Errorf("err: got %q, want contains 'invalid replyTo'", err.Error())
	}
}
