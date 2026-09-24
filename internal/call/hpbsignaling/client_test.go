package hpbesignaling

// client_test.go — транспортные сценарии против мок WS-сервера
// (httptest + websocket.Accept), без сети и без pion. Мок обязан зеркалить
// живой HPB (спайк Task 1, фикстуры testdata/hpb): КАЖДАЯ сессия открывается
// welcome-кадром (без id, version — версия сервера, features) — по features
// клиент выбирает версию hello; settings-ответ мока несёт ОБА ключа
// helloAuthParams (реальный формат, testdata/signaling/capability_external.json).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// fakeConn — серверная сторона одного WS-соединения мока.
type fakeConn struct {
	t  *testing.T
	ws *websocket.Conn
	f  *fakeHPB
}

func (fc *fakeConn) readFrame() (clientFrame, error) {
	var f clientFrame
	// Reader (не Read) + полный ReadAll: reader валиден ровно одно сообщение,
	// недочитанный хвост (json.Encoder пишет '\n') ломает следующий Reader.
	_, r, err := fc.ws.Reader(context.Background())
	if err != nil {
		return f, err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

// sendWelcome — первый кадр живого HPB (фикстура welcome.json): без id,
// version — ВЕРСИЯ СЕРВЕРА, features — список возможностей. v2=true добавляет
// hello-v2 — так мок явно решает, какую версию hello выберет клиент.
func (fc *fakeConn) sendWelcome(v2 bool) {
	features := []string{"incall-all"}
	if v2 {
		features = append(features, "hello-v2")
	}
	fc.send(map[string]any{"type": "welcome", "welcome": map[string]any{
		"version": "2.1.1", "features": features,
	}})
}

// awaitHello читает кадры до hello; логирует его в fakeHPB (mu-защищённо —
// assert'ы читают сразу, не дожидаясь конца сессии) и возвращает кадр целиком
// (версия/auth.url/params).
func (fc *fakeConn) awaitHello() *clientFrame {
	fc.t.Helper()
	for {
		f, err := fc.readFrame()
		if err != nil {
			fc.t.Fatalf("awaitHello: %v", err)
		}
		if f.Type == "hello" && f.Hello != nil {
			fc.f.recordHello(f.Hello)
			return &f
		}
	}
}

func (fc *fakeConn) send(v any) {
	fc.t.Helper()
	w, err := fc.ws.Writer(context.Background(), websocket.MessageText)
	if err != nil {
		fc.t.Fatalf("мок writer: %v", err)
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fc.t.Fatalf("мок encode: %v", err)
	}
	_ = w.Close()
}

func (fc *fakeConn) sendJSON(raw string) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		fc.t.Fatalf("мок sendJSON: %v", err)
	}
	fc.send(v)
}

func (fc *fakeConn) replyHello(sessionid string) {
	fc.send(map[string]any{"id": idHello, "type": "hello", "hello": map[string]any{
		"version": helloV1, "sessionid": sessionid, "resumeid": "res-" + sessionid, "userid": "alice",
	}})
}

func (fc *fakeConn) awaitRoom() string {
	fc.t.Helper()
	for {
		f, err := fc.readFrame()
		if err != nil {
			fc.t.Fatalf("awaitRoom: %v", err)
		}
		if f.Type == "room" && f.Room != nil {
			return f.Room.RoomId
		}
	}
}

func (fc *fakeConn) replyRoomAck(token string) {
	fc.send(map[string]any{"id": idRoom, "type": "room", "room": map[string]any{"roomid": token}})
}

func (fc *fakeConn) drop() {
	_ = fc.ws.Close(websocket.StatusInternalError, "мок: обрыв")
}

// fakeHPB — мок signaling-сервера: каждое принятое соединение обслуживается
// session(n) (n — 0-based счётчик коннектов). Hello-кадры логируются в момент
// получения (helloLog/authLog) — assert'ы версий/ticket не гоняются с концом
// сессии (сессия живёт дольше теста, удерживая соединение sleep'ом).
type fakeHPB struct {
	srv     *httptest.Server
	session func(fc *fakeConn, n int)

	mu       sync.Mutex
	connects int
	authLog  []helloParams
	helloLog []*helloFrame
}

func newFakeHPB(t *testing.T, session func(fc *fakeConn, n int)) *fakeHPB {
	f := &fakeHPB{session: session}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "done")
		f.mu.Lock()
		n := f.connects
		f.connects++
		f.mu.Unlock()
		session(&fakeConn{t: t, ws: ws, f: f}, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHPB) recordHello(h *helloFrame) {
	f.mu.Lock()
	f.authLog = append(f.authLog, h.Auth.Params)
	f.helloLog = append(f.helloLog, h)
	f.mu.Unlock()
}

func (f *fakeHPB) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeHPB) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

// lastAuth — params последнего hello (ticket/token-assert'ы).
func (f *fakeHPB) lastAuth() helloParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authLog) == 0 {
		return helloParams{}
	}
	return f.authLog[len(f.authLog)-1]
}

// lastHello — последний hello-кадр целиком (версия/auth.url-assert'ы).
func (f *fakeHPB) lastHello() *helloFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.helloLog) == 0 {
		return nil
	}
	return f.helloLog[len(f.helloLog)-1]
}

// fakeOCS — мок Nextcloud для settings-refetch и JoinCall-делегирования.
type fakeOCS struct {
	srv *httptest.Server
	mu  sync.Mutex
	// tickets раздаются по порядку запросов settings; после исчерпания —
	// последний (стабильные ответы для reconnect-циклов).
	tickets []string
	i       int
	// v2token — значение helloAuthParams["2.0"].token ("" — ключ есть, токена нет).
	v2token string
	// v1UseridEmpty — settings шлют helloAuthParams["1.0"].userid="" при
	// заполненном корневом userId (ревью #4: пустой v1-userid не должен
	// затирать корневой).
	v1UseridEmpty bool
	// joinCallPaths фиксирует пути POST /call/{token}.
	joinCallPaths []string
	// onCall — опциональный колбэк в момент POST /call/{token} (тест порядка
	// canonical flow: ws-room ДО ocs-call, ревью плана #4).
	onCall func()
}

func newFakeOCS(t *testing.T, tickets ...string) *fakeOCS {
	o := &fakeOCS{tickets: tickets}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/signaling/settings"):
			o.mu.Lock()
			tk := ""
			if len(o.tickets) > 0 {
				if o.i < len(o.tickets) {
					tk = o.tickets[o.i]
				} else {
					tk = o.tickets[len(o.tickets)-1]
				}
			}
			v2 := o.v2token
			v1u := "alice"
			if o.v1UseridEmpty {
				v1u = ""
			}
			o.i++
			o.mu.Unlock()
			// Реальный формат settings (фикстура capability_external.json):
			// корневые ticket/userId И helloAuthParams С ОБЕИМИ ключами.
			fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{`+
				`"signalingMode":"external","server":"https://signaling.example.org/standalone-signaling/",`+
				`"userId":"alice","ticket":%q,`+
				`"helloAuthParams":{"1.0":{"userid":%q,"ticket":%q},"2.0":{"token":%q}},`+
				`"stunservers":[],"turnservers":[]}}}`, tk, v1u, tk, v2)
		case strings.Contains(r.URL.Path, "/call/"):
			o.mu.Lock()
			o.joinCallPaths = append(o.joinCallPaths, r.URL.Path)
			cb := o.onCall
			o.mu.Unlock()
			if cb != nil {
				cb()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(o.srv.Close)
	return o
}

// setV2Token — потокобезопасная установка helloAuthParams["2.0"].token.
func (o *fakeOCS) setV2Token(tok string) {
	o.mu.Lock()
	o.v2token = tok
	o.mu.Unlock()
}

// setOnCall — потокобезопасная установка колбэка POST /call/{token}.
func (o *fakeOCS) setOnCall(fn func()) {
	o.mu.Lock()
	o.onCall = fn
	o.mu.Unlock()
}

// setV1UseridEmpty — потокобезопасно: settings шлют helloAuthParams["1.0"].userid="".
func (o *fakeOCS) setV1UseridEmpty() {
	o.mu.Lock()
	o.v1UseridEmpty = true
	o.mu.Unlock()
}

// newTestClient собирает Client против моков.
func newTestClient(f *fakeHPB, o *fakeOCS, mutate func(*Config)) *Client {
	base, _ := url.Parse(o.srv.URL)
	cfg := Config{
		Auth:          transport.Auth{BaseURL: base, Login: "alice", Password: "pw"},
		Doer:          o.srv.Client(),
		Server:        f.url(),
		Ticket:        "ticket-1",
		Userid:        "alice",
		RoomSessionId: "ocs-sess-1",
		ConnectBudget: 3 * time.Second,
		BackoffBase:   20 * time.Millisecond,
		BackoffMax:    100 * time.Millisecond,
		PingPeriod:    time.Hour, // ping отключён (Task 5 включает)
		Stderr:        io.Discard,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg)
}

// collectEvents читает ch до таймаута, возвращая события и флаг loop-выхода.
func collectEvents(t *testing.T, ch <-chan signaling.Event, d time.Duration) ([]signaling.Event, bool) {
	t.Helper()
	var got []signaling.Event
	timer := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got, true
			}
			got = append(got, ev)
		case <-timer:
			return got, false
		}
	}
}

func TestPollLoop_EventsFlow(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false) // без hello-v2 → клиент обязан выбрать hello v1
		if h := fc.awaitHello(); h.Hello.Auth.Params.Ticket != "ticket-1" {
			t.Errorf("hello ticket = %q, want ticket-1 (settings без ticket — стартовые cfg-параметры)",
				h.Hello.Auth.Params.Ticket)
		}
		fc.replyHello("own-hpb-sid")
		if tok := fc.awaitRoom(); tok != "tok-test" {
			t.Errorf("room = %q", tok)
		}
		fc.replyRoomAck("tok-test")
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[` +
			`{"sessionid":"own-hpb-sid","userid":"alice"},{"sessionid":"peer-1","userid":"bob"}]}}`)
		fc.sendJSON(`{"type":"event","event":{"target":"participants","type":"update","update":{"roomid":"tok-test",` +
			`"users":[{"sessionId":"own-hpb-sid","userId":"alice","inCall":3},{"sessionId":"peer-1","userId":"bob","inCall":3}]}}}`)
		fc.sendJSON(`{"type":"message","message":{"sender":{"type":"session","sessionid":"peer-1","userid":"bob"},` +
			`"recipient":{"type":"session","sessionid":"own-hpb-sid"},` +
			`"data":{"type":"offer","roomType":"video","payload":{"type":"offer","sdp":"v=0"}}}}`)
		time.Sleep(2 * time.Second) // держим соединение
	})
	o := newFakeOCS(t) // settings без ticket → fallback на cfg ticket-1
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 2*time.Second)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("PollLoop err = %v, want nil (ctx-выход)", err)
	}

	var sawOwn, sawUsers, sawOffer bool
	for _, ev := range evs {
		switch ev.Kind {
		case signaling.EvOwnSession:
			sawOwn = ev.From == "own-hpb-sid"
		case signaling.EvUsersUpdated:
			sawUsers = len(ev.Users) == 2 && ev.Users[1].InCall == 3
		case signaling.EvOffer:
			sawOffer = ev.From == "peer-1" && ev.SDP == "v=0"
		}
	}
	if !sawOwn || !sawUsers || !sawOffer {
		t.Fatalf("события неполные: own=%v users=%v offer=%v (все=%d)", sawOwn, sawUsers, sawOffer, len(evs))
	}
}

func TestPollLoop_ConnectBudgetExhausted_EvErrorExit1(t *testing.T) {
	// Сервер, отклоняющий upgrade → WS-dial падает; бюджет мал.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth: transport.Auth{BaseURL: base, Login: "a", Password: "p"}, Doer: o.srv.Client(),
		Server: "ws" + strings.TrimPrefix(srv.URL, "http"), Ticket: "t", Userid: "a",
		ConnectBudget: 200 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok", ch) }()

	select {
	case ev := <-ch:
		if ev.Kind != signaling.EvError {
			t.Fatalf("первое событие = %v, want EvError", ev.Kind)
		}
		var ee exit.ExitError // VALUE-target, не указатель (инвариант CLAUDE.md)
		if !errors.As(ev.Err, &ee) || ee.Code != exit.ExitGeneric {
			t.Fatalf("EvError.Err = %v, want ExitError{1}", ev.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("бюджет не исчерпался — молчаливый ретрай даст exit 0 «я один» (симптом дельты §1)")
	}
	if err := <-done; err == nil {
		t.Fatal("PollLoop вернул nil — должен вернуть ошибку подключения")
	}
}

func TestPollLoop_NoSuchRoom_EvErrorExit2(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.send(map[string]any{"id": idRoom, "type": "error",
			"error": map[string]string{"code": "no_such_room", "message": "нет такой комнаты"}})
		time.Sleep(time.Second)
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok-test", ch) }()

	select {
	case ev := <-ch:
		var ee exit.ExitError // VALUE-target (инвариант CLAUDE.md)
		if ev.Kind != signaling.EvError || !errors.As(ev.Err, &ee) || ee.Code != exit.ExitNotFound {
			t.Fatalf("ev = %+v, want EvError{exit 2}", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no_such_room не фаталится exit 2")
	}
	<-done
}

func TestPollLoop_ReconnectAfterDrop_NewTicket(t *testing.T) {
	// Мок явно выбирает v1 (welcome без hello-v2) — ticket-assert ниже в терминах v1.
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello(fmt.Sprintf("own-hpb-sid-%d", n+1))
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		if n == 0 {
			fc.drop() // обрыв первого соединения
			return
		}
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"peer-1"}]}}`)
		time.Sleep(2 * time.Second)
	})
	// Первое подключение — стартовый ticket-1 (settings пусты → cfg-fallback),
	// переподключение — СВЕЖИЙ ticket-2 из settings-refetch.
	o := newFakeOCS(t, "", "ticket-2")
	var buf syncBuffer
	c := newTestClient(f, o, func(cfg *Config) { cfg.Stderr = &buf })

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 2*time.Second)
	cancel()
	<-done

	if f.connectCount() < 2 {
		t.Fatalf("connects = %d, want >= 2 (переподключение после обрыва)", f.connectCount())
	}
	if f.lastAuth().Ticket != "ticket-2" {
		t.Errorf("hello после переподключения несёт %q, want свежий ticket-2 (refetch из settings)", f.lastAuth().Ticket)
	}
	var sawOwn2 bool
	for _, ev := range evs {
		if ev.Kind == signaling.EvOwnSession && ev.From == "own-hpb-sid-2" {
			sawOwn2 = true // sessionId МЕНЯЕТСЯ между сессиями — агент обязан обновить own
		}
	}
	if !sawOwn2 {
		t.Error("после переподключения не доставлен EvOwnSession с новым sessionid")
	}
	if !strings.Contains(buf.String(), "reconnect") {
		t.Errorf("stderr без диагностики reconnect: %q", buf.String())
	}
}

// syncBuffer — потокобезопасный bytes.Buffer для Stderr-assert'ов.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestJoinCall_ConnectsWSRoomBeforeOCS — canonical flow (дельта §2, ревью
// плана #4): JoinCall обязан установить WS-комнату (hello → room-ack) ДО
// делегирования POST /call/{token} — иначе participants-update с inCall-флагами
// уйдёт комнате без нашей сессии, а join-event флагов не несёт (фильтр
// WITH_AUDIO даст 0 пиров → exit 0 «я один»).
func TestJoinCall_ConnectsWSRoomBeforeOCS(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		if tok := fc.awaitRoom(); tok != "tok-test" {
			t.Errorf("room = %q", tok)
		}
		fc.replyRoomAck("tok-test")
		record("ws-room")
		time.Sleep(time.Second) // держим соединение до конца теста
	})
	o := newFakeOCS(t)
	o.setOnCall(func() { record("ocs-call") })
	c := newTestClient(f, o, nil)

	if err := c.JoinCall(context.Background(), "tok-test", 3); err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	o.mu.Lock()
	paths := append([]string(nil), o.joinCallPaths...)
	o.mu.Unlock()
	if len(paths) != 1 || !strings.Contains(paths[0], "/call/tok-test") {
		t.Fatalf("JoinCall не дошёл до OCS: %v", paths)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "ws-room" || order[1] != "ocs-call" {
		t.Fatalf("порядок = %v, want [ws-room ocs-call] (canonical flow дельты §2)", order)
	}
}

// TestJoinCall_HelloV2_WhenWelcomeFeature — версия hello выбирается по
// welcome-features (спайк Task 1): feature hello-v2 + helloAuthParams["2.0"]
// из свежих settings → кадр версии "2.0" с params {token}, БЕЗ v1-полей —
// v1-ticket в v2-hello утекать не должен. Заодно — auth.url обязан содержать
// ocs/v2.php/apps/spreed/ (валидируется HPB, protocol.go backendPath).
func TestJoinCall_HelloV2_WhenWelcomeFeature(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(true) // feature hello-v2 → клиент обязан выбрать "2.0"
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		time.Sleep(time.Second) // держим соединение до конца теста
	})
	o := newFakeOCS(t, "ticket-1")
	o.setV2Token("authsig-v2")
	c := newTestClient(f, o, nil)

	if err := c.JoinCall(context.Background(), "tok-test", 3); err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	h := f.lastHello()
	if h == nil {
		t.Fatal("hello-кадр не получен моком")
	}
	if h.Version != helloV2 {
		t.Errorf("hello version = %q, want %q (welcome-feature hello-v2)", h.Version, helloV2)
	}
	if h.Auth.Params.Token != "authsig-v2" {
		t.Errorf("hello token = %q, want authsig-v2 (helloAuthParams[2.0] из свежих settings)", h.Auth.Params.Token)
	}
	if h.Auth.Params.Ticket != "" || h.Auth.Params.Userid != "" {
		t.Errorf("v2-hello несёт v1-поля: %+v (v1-ticket утекать не должен)", h.Auth.Params)
	}
	if !strings.Contains(h.Auth.URL, "ocs/v2.php/apps/spreed/") {
		t.Errorf("hello auth.url = %q, want содержит ocs/v2.php/apps/spreed/ (валидируется HPB)", h.Auth.URL)
	}
}

// TestPollLoop_HungServer_BudgetFiresEvError — сервер принял TCP, но не
// отвечает (аналог SYN-blackhole/молчащего файрвола): per-attempt deadline
// обязан оборвать зависший dial в бюджет, а не ждать OS-таймаута TCP
// (ревью плана #5) — иначе EvError{exit 1} придёт после ICE-таймера (exit 0
// «я один»).
// ---- Task 5: Send (любой Type) + keepalive-ping ----

// TestSend_WrapsAnyType — критично: unmute обязан проходить (whitelist ломает
// звук на живом HPB — root cause spike-gate 2026-07-20; unit-мок бы не заметил).
func TestSend_WrapsAnyType(t *testing.T) {
	var got []clientFrame
	var mu sync.Mutex
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false) // Task 4: сессия открывается welcome-кадром (v1-путь)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		for {
			fr, err := fc.readFrame()
			if err != nil {
				return // соединение закрыто тестом
			}
			mu.Lock()
			got = append(got, fr)
			mu.Unlock()
		}
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	ch := make(chan signaling.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	// Ждём установления (EvOwnSession) — Send до подключения обязан ошибаться.
	if !waitForEvent(t, ch, signaling.EvOwnSession, 2*time.Second) {
		t.Fatal("соединение не установилось")
	}
	payload, _ := json.Marshal(map[string]string{"name": "audio"})
	if err := c.Send(ctx, "tok-test", signaling.Message{Type: "unmute", To: "peer-1", Payload: payload}); err != nil {
		t.Fatalf("Send(unmute): %v", err)
	}
	if err := c.Send(ctx, "tok-test", signaling.Message{Type: "candidate", To: "peer-1",
		Payload: json.RawMessage(`{"candidate":{"candidate":"cand-1","sdpMLineIndex":0,"sdpMid":"0"}}`)}); err != nil {
		t.Fatalf("Send(candidate): %v", err)
	}

	// Даём мок-серверу время прочитать оба кадра (Read в горутине сессии).
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("сервер получил %d кадров, want >= 2", len(got))
	}
	u := got[0].Message
	if u == nil || u.Data.Type != "unmute" || u.Recipient.SessionId != "peer-1" ||
		string(u.Data.Payload) != `{"name":"audio"}` {
		t.Errorf("unmute-кадр = %+v", u)
	}
	cd := got[1].Message
	if cd == nil || cd.Data.Type != "candidate" || cd.Recipient.Type != "session" {
		t.Errorf("candidate-кадр = %+v", cd)
	}
}

// waitForEvent ждёт событие заданного Kind (true) до таймаута (false).
func waitForEvent(t *testing.T, ch <-chan signaling.Event, kind signaling.EventKind, d time.Duration) bool {
	t.Helper()
	timer := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return true
			}
		case <-timer:
			return false
		}
	}
}

// TestSend_NoConnection_Error — Send между (пере)подключениями — явная ошибка.
func TestSend_NoConnection_Error(t *testing.T) {
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth: transport.Auth{BaseURL: base, Login: "alice", Password: "pw"},
		Doer: o.srv.Client(), Server: "ws://127.0.0.1:1",
		Ticket: "t", Userid: "a", ConnectBudget: 50 * time.Millisecond,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})
	if err := c.Send(context.Background(), "tok", signaling.Message{Type: "offer"}); err == nil {
		t.Fatal("Send без соединения = nil, want ошибка")
	}
}

// TestPing_KeepsConnectionAlive — ping не убивает живое соединение; события
// приходят и спустя несколько ping-периодов (мок отвечает pong автоматически —
// coder/websocket; сервер читает кадры, чтобы control-фреймы обрабатывались).
func TestPing_KeepsConnectionAlive(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false) // Task 4: сессия открывается welcome-кадром (v1-путь)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		// Фоновое чтение: coder/websocket обрабатывает control-фреймы (наши
		// ping → auto-pong) только при активном Read — без него клиент
		// справедливо разорвёт «молчащую» связь.
		go func() {
			for {
				if _, err := fc.readFrame(); err != nil {
					return
				}
			}
		}()
		time.Sleep(500 * time.Millisecond) // > 4 ping-тиков при PingPeriod=100ms
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"peer-1"}]}}`)
		// Держим соединение ДО КОНЦА окна collectEvents (1.2с): выход сессии
		// закрывает ws (defer в newFakeHPB) и дал бы переподключение, не
		// связанное с ping — ложный connects=2 (адаптация к Task 4-моку).
		time.Sleep(1 * time.Second)
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, func(cfg *Config) { cfg.PingPeriod = 100 * time.Millisecond })

	ch := make(chan signaling.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	evs, _ := collectEvents(t, ch, 1200*time.Millisecond)
	if f.connectCount() != 1 {
		t.Fatalf("connects = %d, want 1 (ping не должен рвать живое соединение)", f.connectCount())
	}
	sawUsers := false
	for _, ev := range evs {
		if ev.Kind == signaling.EvUsersUpdated && len(ev.Users) == 1 {
			sawUsers = true
		}
	}
	if !sawUsers {
		t.Fatal("событие после ping-периодов не доставлено — соединение умерло")
	}
}

func TestPollLoop_HungServer_BudgetFiresEvError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() { // принимаем соединения и молчим: ни HTTP-ответа, ни upgrade
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c
		}
	}()
	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth:   transport.Auth{BaseURL: base, Login: "a", Password: "p"},
		Doer:   o.srv.Client(),
		Server: "ws://" + ln.Addr().String(),
		Ticket: "t", Userid: "a",
		ConnectBudget: 200 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})

	ch := make(chan signaling.Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok", ch) }()

	select {
	case ev := <-ch:
		var ee exit.ExitError // VALUE-target (инвариант CLAUDE.md)
		if ev.Kind != signaling.EvError || !errors.As(ev.Err, &ee) || ee.Code != exit.ExitGeneric {
			t.Fatalf("ev = %+v, want EvError{exit 1} (бюджет оборвал зависший dial)", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("зависший dial не оборван бюджетом — EvError после ICE-таймера дал бы exit 0 «я один»")
	}
	if err := <-done; err == nil {
		t.Fatal("PollLoop вернул nil — должен вернуть ошибку подключения")
	}
}

// ---- ревью HPB 2026-09-24: guard конкурентных подключений, v1-userid,
// redirect-политика WS-dial, диагностика reply-error ----

// TestJoinCall_Concurrent_SingleDial — guard TOCTOU (ревью #2): конкурентные
// JoinCall не должны поднимать параллельные WS-подключения — второй setConn
// перезаписывал бы первый (утечка коннекта + ghost-сессия на сервере), а
// pending с EvOwnSession мёртвой сессии доехал бы до агента. Владелец диалит,
// прочие ждут его результата. Welcome задержан, чтобы все горутины застали
// «подключение в полёте» (без guard каждая диалила бы сама).
func TestJoinCall_Concurrent_SingleDial(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		time.Sleep(400 * time.Millisecond) // держим connect-фазу открытой: welcome задержан
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		time.Sleep(time.Second)
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	const workers = 3
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.JoinCall(context.Background(), "tok-test", 3)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("JoinCall[%d]: %v", i, err)
		}
	}
	if n := f.connectCount(); n != 1 {
		t.Fatalf("connects = %d, want 1 (guard: конкурентные подключения сериализуются, ревью #2)", n)
	}
}

// TestPollLoop_WaitsInflightJoinCallDial — вторая сторона guard (ревью #2):
// PollLoop, стартовавший ПОКА JoinCall ещё диалит, переиспользует его
// подключение, а не поднимает своё: connects==1, EvOwnSession доставлен ровно
// один раз.
func TestPollLoop_WaitsInflightJoinCallDial(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		time.Sleep(400 * time.Millisecond) // connect-фаза JoinCall открыта
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		time.Sleep(3 * time.Second) // переживает окно теста — без лишнего reconnect
	})
	o := newFakeOCS(t)
	c := newTestClient(f, o, nil)

	jc := make(chan error, 1)
	go func() { jc <- c.JoinCall(context.Background(), "tok-test", 3) }()
	time.Sleep(100 * time.Millisecond) // PollLoop стартует при «диале в полёте»

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	if err := <-jc; err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	evs, _ := collectEvents(t, ch, 1500*time.Millisecond)
	cancel()
	<-done

	if n := f.connectCount(); n != 1 {
		t.Fatalf("connects = %d, want 1 (PollLoop обязан ждать in-flight диал JoinCall, ревью #2)", n)
	}
	own := 0
	for _, ev := range evs {
		if ev.Kind == signaling.EvOwnSession {
			own++
		}
	}
	if own != 1 {
		t.Fatalf("EvOwnSession доставлен %d раз, want 1 (по одному на WS-сессию)", own)
	}
}

// TestConnect_V1BlockEmptyUserid_FallsBackToRoot — зеркало capability-фикса
// (ревью #4): settings с непустым v1-ticket, но ПУСТЫМ v1-userid при корневом
// userId → hello v1 обязан нести корневой userid (иначе invalid_ticket-цикл).
func TestConnect_V1BlockEmptyUserid_FallsBackToRoot(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		time.Sleep(time.Second)
	})
	o := newFakeOCS(t, "ticket-v1")
	o.setV1UseridEmpty()
	c := newTestClient(f, o, nil)

	if err := c.JoinCall(context.Background(), "tok-test", 3); err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	if got := f.lastAuth().Userid; got != "alice" {
		t.Fatalf("hello userid = %q, want alice — пустой v1-userid НЕ затирает корневой userId (ревью #4)", got)
	}
}

// TestConnect_CrossHostRedirect_Blocked — инвариант транспорта
// (transport.SameHostRedirectPolicy: cross-host и https→http блокируются)
// распространён на WS-dial (ревью #5): handshake-редирект на чужой хост
// обязан блокироваться, а не молча следовать.
func TestConnect_CrossHostRedirect_Blocked(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // до upgrade здесь дойти не должно
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/spreed", http.StatusFound) // чужой хост (другой порт)
	}))
	defer src.Close()

	o := newFakeOCS(t)
	base, _ := url.Parse(o.srv.URL)
	c := New(Config{
		Auth:   transport.Auth{BaseURL: base, Login: "a", Password: "p"},
		Doer:   o.srv.Client(),
		Server: src.URL,
		Ticket: "t", Userid: "a",
		ConnectBudget: time.Second, BackoffBase: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		PingPeriod: time.Hour, Stderr: io.Discard,
	})
	err := c.JoinCall(context.Background(), "tok", 3)
	if err == nil {
		t.Fatal("JoinCall = nil, want ошибка (cross-host redirect обязан блокироваться)")
	}
	if !strings.Contains(err.Error(), "cross-host redirect blocked") {
		t.Fatalf("ошибка = %v, want «cross-host redirect blocked» (redirect-политика WS-dial, ревью #5)", err)
	}
}

// TestReadLoop_ErrorFrameWithID_Logged — error-кадр с непустым id (reply-форма)
// в steady-state раньше терялся молча (applyFrame → default → nil); теперь —
// диагностика в Stderr (ревью #6). Звонок НЕ роняется: события после кадра
// продолжают доставляться.
func TestReadLoop_ErrorFrameWithID_Logged(t *testing.T) {
	f := newFakeHPB(t, func(fc *fakeConn, n int) {
		fc.sendWelcome(false)
		fc.awaitHello()
		fc.replyHello("own-hpb-sid")
		fc.awaitRoom()
		fc.replyRoomAck("tok-test")
		fc.sendJSON(`{"id":"x1","type":"error","error":{"code":"unexpected_reply","message":"boz"}}`)
		fc.sendJSON(`{"type":"event","event":{"target":"room","type":"join","join":[{"sessionid":"peer-1"}]}}`)
		time.Sleep(2 * time.Second)
	})
	o := newFakeOCS(t)
	var buf syncBuffer
	c := newTestClient(f, o, func(cfg *Config) { cfg.Stderr = &buf })

	ch := make(chan signaling.Event, 16)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	if !waitForEvent(t, ch, signaling.EvUsersUpdated, 2*time.Second) {
		t.Fatal("событие после error-кадра не доставлено — звонок упал на reply-error")
	}
	if !strings.Contains(buf.String(), "unexpected_reply") {
		t.Errorf("reply-error потерян молча: stderr = %q, want код unexpected_reply (ревью #6)", buf.String())
	}
	cancel()
	<-done
}
