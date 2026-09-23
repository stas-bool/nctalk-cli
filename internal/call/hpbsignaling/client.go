// client.go — WS-транспорт signaling для HPB (дельта 2026-09-23 §2/§3).
// Реализует agent.sigClient ЦЕЛИКОМ: PollLoop — WebSocket, LeaveCall —
// делегирование OCS-клиенту call/signaling (Call API v4 работает при любом
// signalingMode), JoinCall — ensureConnected (WS hello/room с бюджетом) +
// делегирование OCS; Send добавит Task 5.
//
// Модель подключений (дельта §2/§3):
//   - canonical flow: JoinRoom (cmd) → WS hello/room → JoinCall (OCS) →
//     события. WS-комнату устанавливает JoinCall ДО делегирования (ревью
//     плана #4): participants-update с inCall-флагами, порождённый JoinCall,
//     обязан расходиться по комнате, в которой наша сессия УЖЕ есть —
//     join-event флагов не несёт, «опоздавший» их не увидит (фильтр
//     WITH_AUDIO даст 0 пиров → exit 0 «я один», симптом §1);
//   - первичное подключение (settings → welcome → hello → room-ack) —
//     ОГРАНИЧЕННЫЙ бюджет (ConnectBudget, дефолт 15с ≈ половина
//     NCTALK_ICE_TIMEOUT), причём per-attempt context.WithDeadline (ревью
//     плана #5): зависший dial или молчащий сервер обрываются в бюджет, а не
//     ждут OS-таймаута TCP; исчерпание → ошибка JoinCall (агент выйдет до
//     старта ICE-таймера) или EvError{exit 1} в канал ДО ICE-таймера — иначе
//     молчаливый ретрай даст exit 0 «я один в звонке» (исходный симптом
//     дельты §1);
//   - переподключение ПОСЛЕ установления — бесконечный backoff 1с→30с БЕЗ
//     дедлайна бюджета, свежие auth-params на каждое (settings-refetch),
//     peers стоят, звонок живёт;
//   - reconnect = ПОЛНЫЙ re-hello + re-room-join (без resumeid): сервер сам
//     присылает полный join-список, reconcile агента самовосстанавливается.
//
// Connect-последовательность — канон спайка Task 1 (integration_test.go):
// settings (свежие auth-params + v2-token) → dial /spreed → welcome (features)
// → hello версии по welcome (hello-v2 + token → "2.0", иначе "1.0") →
// hello-response → room-join → room-ack.
package hpbesignaling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	// pathCallFmt дублирует signaling.pathCallFmt (там unexported): JoinCall/
	// LeaveCall делегируются вложенному signaling.Client, а этот путь нужен
	// только его док-контексту — оставлен для симметрии/будущих правок.
	pathCallFmt = "/ocs/v2.php/apps/spreed/api/v4/call/%s"
	// pathSignalingSettings — источник auth-params при каждом (пере)подключении
	// (дельта §2: отдельного ticket-эндпоинта нет, ticket живёт в settings-ответе).
	pathSignalingSettings = "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"

	defaultConnectBudget = 15 * time.Second // ≈ NCTALK_ICE_TIMEOUT(30с)/2 (дельта §3)
	defaultBackoffBase   = 1 * time.Second
	defaultBackoffMax    = 30 * time.Second
	defaultPingPeriod    = 30 * time.Second // сервер пингует ~54с; наш ping — NAT + half-open (Task 5)
	writeTimeout         = 5 * time.Second  // на один исходящий кадр (Task 5)
)

// Config — параметры WS-сессии. Ticket/Userid — стартовые auth-params из
// settings-запроса запуска (capability.Settings, ОДИН запрос на старт — дельта
// §3); каждое подключение пере-запрашивает settings САМО (свежий ticket И
// v2-token для выбора версии hello), стартовые остаются fallback'ом на случай
// недоступности settings.
type Config struct {
	Auth          transport.Auth // Basic-auth для OCS (settings/JoinCall/LeaveCall)
	Doer          transport.Doer // OCS-транспорт (обычно *http.Client с Timeout)
	Server        string         // settings.server (https/wss — нормализуется)
	Ticket        string         // стартовый ticket (fallback v1-hello)
	Userid        string         // userid для стартовых hello-params
	RoomSessionId string         // OCS-sessionId из JoinRoom (room-join; НЕ own-фильтр!)
	ConnectBudget time.Duration  // 0 → defaultConnectBudget
	BackoffBase   time.Duration  // 0 → 1с (тесты сжимают)
	BackoffMax    time.Duration  // 0 → 30с
	PingPeriod    time.Duration  // 0 → 30с
	Stderr        io.Writer      // диагностика (reconnect / settings-fallback); nil → io.Discard
}

// Client — HPB-signaling клиент. Потокобезопасен: Send/Ping сериализуются
// мьютексом на conn; PollLoop — единственный владелец read-стороны.
type Client struct {
	cfg Config
	ocs *signaling.Client // делегирование JoinCall/LeaveCall (Call API — OCS)

	mu      sync.Mutex
	conn    *websocket.Conn   // nil между (пере)подключениями
	state   *roomState        // снапшот текущей WS-сессии (nil между подключениями)
	pending []signaling.Event // события connect-фазы при JoinCall-подключении (раздаёт PollLoop)
}

// New — конструктор. doer используется и для OCS-делегирования, и для
// settings-refetch; WS-диал идёт через собственный http.Client БЕЗ Timeout
// (Timeout на http.Client убивает длительное WS-соединение).
func New(cfg Config) *Client {
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = defaultBackoffBase
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.PingPeriod == 0 {
		cfg.PingPeriod = defaultPingPeriod
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	return &Client{cfg: cfg, ocs: signaling.New(cfg.Auth, cfg.Doer)}
}

// JoinCall — Call API (POST /api/v4/call/{token}) ПОСЛЕ установления
// WS-комнаты: canonical flow дельты §2 (JoinRoom → settings → WS hello/room →
// JoinCall; ревью плана #4 — иначе participants-update с inCall-флагами
// уйдёт комнате без нашей сессии). Ошибка подключения — ошибка JoinCall:
// агент выйдет по ней до старта ICE-таймера.
func (c *Client) JoinCall(ctx context.Context, token string, flags int) error {
	if err := c.ensureConnected(ctx, token); err != nil {
		return err
	}
	return c.ocs.JoinCall(ctx, token, flags)
}

// LeaveCall — делегирование Call API (DELETE /api/v4/call/{token}).
func (c *Client) LeaveCall(ctx context.Context, token string) error {
	return c.ocs.LeaveCall(ctx, token)
}

// connectBudget — бюджет первичного подключения (0 → дефолт).
func (c *Client) connectBudget() time.Duration {
	if c.cfg.ConnectBudget == 0 {
		return defaultConnectBudget
	}
	return c.cfg.ConnectBudget
}

// ensureConnected — первичное подключение с бюджетом (идемпотентно: при живом
// соединении no-op). События connect-фазы буферизуются в pending: у JoinCall
// нет канала событий, их раздаст PollLoop. Per-attempt deadline — зависший
// dial/молчащий welcome не переживают бюджет (ревью плана #5).
func (c *Client) ensureConnected(ctx context.Context, token string) error {
	c.mu.Lock()
	connected := c.conn != nil
	c.mu.Unlock()
	if connected {
		return nil
	}
	budget := c.connectBudget()
	actx, cancel := context.WithDeadline(ctx, time.Now().Add(budget))
	defer cancel()
	conn, st, pending, err := c.connect(actx, token)
	if err != nil {
		return fmt.Errorf("hpbesignaling: подключение к signaling-серверу не удалось за %s (бюджет первичного подключения): %w", budget, err)
	}
	c.mu.Lock()
	c.state = st
	c.pending = append(c.pending, pending...)
	c.mu.Unlock()
	c.setConn(conn)
	return nil
}

// PollLoop — главный цикл транспорта. Семантика выхода — как у OCS-polling
// (signaling.Client.PollLoop): ctx.Done → nil; фатал (бюджет исчерпан /
// no_such_room) → EvError в ch ДО return и возврат ошибки. Если JoinCall уже
// установил соединение (canonical flow) — переиспользует его и раздаёт
// буферизованные события connect-фазы (включая EvOwnSession). Если PollLoop
// запущен без JoinCall — сам подключается с бюджетом (тесты/защита).
func (c *Client) PollLoop(ctx context.Context, token string, ch chan<- signaling.Event) error {
	budget := c.connectBudget()
	deadline := time.Now().Add(budget)
	established := false
	var backoff time.Duration

	for {
		if err := ctx.Err(); err != nil {
			return nil // штатный выход (вызывающий отменит ctx; LeaveCall — отдельно)
		}
		c.mu.Lock()
		conn, st := c.conn, c.state
		c.mu.Unlock()
		if conn == nil {
			// per-attempt deadline — ТОЛЬКО пока соединение не установлено
			// (reconnect-попытки после обрыва живут без дедлайна бюджета):
			// зависший dial/молчащий welcome/hello обрываются в бюджет (ревью
			// плана #5). Каждая попытка пере-запрашивает settings (invalid_ticket
			// лечится сам).
			callCtx := ctx
			var cancel context.CancelFunc
			if !established {
				callCtx, cancel = context.WithDeadline(ctx, deadline)
			}
			var pending []signaling.Event
			var cerr error
			conn, st, pending, cerr = c.connect(callCtx, token)
			if cancel != nil {
				cancel()
			}
			if cerr != nil {
				var fe *errFrameError
				switch {
				case errors.As(cerr, &fe) && frameErrAction(fe.Code) == frameErrFatal2:
					return c.fatal(ch, ctx, exit.ExitNotFound, cerr)
				case !established && !time.Now().Before(deadline):
					return c.fatal(ch, ctx, exit.ExitGeneric,
						fmt.Errorf("подключение к signaling-серверу не удалось за %s (бюджет первичного подключения): %w", budget, cerr))
				}
				backoff = nextBackoff(backoff, c.cfg.BackoffBase, c.cfg.BackoffMax)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(backoff):
					continue
				}
			}
			c.mu.Lock()
			c.state = st
			c.mu.Unlock()
			c.setConn(conn)
			for _, ev := range pending {
				if !sendEvent(ctx, ch, ev) {
					return nil
				}
			}
		} else {
			// Соединение установлено JoinCall: забираем буфер connect-фазы.
			c.mu.Lock()
			pending := c.pending
			c.pending = nil
			c.mu.Unlock()
			for _, ev := range pending {
				if !sendEvent(ctx, ch, ev) {
					return nil
				}
			}
		}
		established = true
		backoff = 0

		_ = c.readLoop(ctx, conn, st, ch)
		_ = conn.Close(websocket.StatusNormalClosure, "loop exit")
		c.setConn(nil)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(c.cfg.Stderr, "signaling: reconnect") // диагностика обрыва (дельта §4)
	}
}

// fatal — EvError в канал (уважая ctx) + возврат ошибки.
func (c *Client) fatal(ch chan<- signaling.Event, ctx context.Context, code int, err error) error {
	sendEvent(ctx, ch, signaling.Event{Kind: signaling.EvError, Err: exit.Exit(code, err)})
	return err
}

func sendEvent(ctx context.Context, ch chan<- signaling.Event, ev signaling.Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// connect — одно подключение: свежие settings-auth-params → dial (/spreed) →
// welcome → hello (версия по welcome-features) → hello-response → room-join →
// room-ack. События, пришедшие ДО ack (join-список), буферизуются и
// возвращаются (включая EvOwnSession из hello-response).
func (c *Client) connect(ctx context.Context, token string) (*websocket.Conn, *roomState, []signaling.Event, error) {
	// Auth-params: ВСЕГДА свежие settings (дают ticket И v2-token для выбора
	// версии hello — capability.Settings v2-токен не exposes, дельта §3);
	// при неудаче и наличии стартового cfg.Ticket — fallback на cfg-параметры
	// (v1 only).
	userid, ticket, v2Token := c.cfg.Userid, c.cfg.Ticket, ""
	fresh, ferr := c.fetchAuthParams(ctx)
	if ferr == nil && (fresh.ticket != "" || fresh.v2Token != "") {
		userid, ticket, v2Token = fresh.userid, fresh.ticket, fresh.v2Token
	} else if c.cfg.Ticket != "" {
		if ferr != nil {
			fmt.Fprintf(c.cfg.Stderr, "signaling: settings не получены (%v) — стартовые auth-params из конфига\n", ferr)
		} else {
			fmt.Fprintf(c.cfg.Stderr, "signaling: settings без ticket/token — стартовые auth-params из конфига\n")
		}
	} else {
		if ferr == nil {
			ferr = errors.New("пустые auth-параметры")
		}
		return nil, nil, nil, fmt.Errorf("hpbesignaling: signaling-settings: %w", ferr)
	}

	conn, _, err := websocket.Dial(ctx, normalizeWSURL(c.cfg.Server), &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: http.DefaultTransport}, // без Timeout — длительное WS
	})
	if err != nil {
		return nil, nil, nil, transport.SanitizeErr(fmt.Errorf("hpbesignaling: ws dial: %w", err))
	}

	st := newRoomState()
	var pending []signaling.Event
	// welcome — первый кадр после connect (спайк Task 1): по features
	// выбирается версия hello. Не-welcome первый кадр уходит в общий цикл
	// waitReply (robustness).
	hello := newHelloFrame(userid, ticket, c.backendURL())
	first, err := readFrame(ctx, conn)
	if err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, transport.SanitizeErr(fmt.Errorf("hpbesignaling: welcome: %w", err))
	}
	if first.Type == "welcome" && first.Welcome != nil {
		if wantHelloV2(first.Welcome.Features, v2Token) {
			hello = newHelloFrameV2(v2Token, c.backendURL())
		}
		// welcome — шум для roomState (Task 3): applyFrame вернёт пусто,
		// pending остаётся чистым.
		pending = append(pending, st.applyFrame(first)...)
		first = nil
	}
	// hello → ждём hello-response (welcome поглощён applyFrame'ом).
	if err := writeFrame(ctx, conn, hello); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, fmt.Errorf("hpbesignaling: hello: %w", err)
	}
	if err := waitReply(ctx, conn, st, idHello, first, checkHelloResponse, &pending); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, err
	}
	// room-join → ждём ack (OCS-sessionId нужен серверу для проверки прав).
	if err := writeFrame(ctx, conn, newRoomFrame(token, c.cfg.RoomSessionId, idRoom)); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, fmt.Errorf("hpbesignaling: room: %w", err)
	}
	if err := waitReply(ctx, conn, st, idRoom, nil, nil, &pending); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return nil, nil, nil, err
	}
	return conn, st, pending, nil
}

// checkHelloResponse — hello-response обязан нести sessionid (наш sessionId
// в HPB-пространстве); format-drift без guard'а дал бы молчаливый пустой own.
func checkHelloResponse(f *serverFrame) error {
	if f.Hello == nil || f.Hello.SessionId == "" {
		return fmt.Errorf("hello-response без sessionid (формат дрейфанул — см. спайк Task 1)")
	}
	return nil
}

// waitReply читает кадры до ответа с id==want; first — заранее прочитанный
// кадр (nil → читать из WS). Попутные кадры идут через roomState (буфер
// событий) — join-список приходит сразу после ack.
func waitReply(ctx context.Context, conn *websocket.Conn, st *roomState, want string, first *serverFrame, check func(*serverFrame) error, pending *[]signaling.Event) error {
	f := first
	for {
		if f == nil {
			var err error
			if f, err = readFrame(ctx, conn); err != nil {
				return fmt.Errorf("hpbesignaling: чтение ответа %s: %w", want, err)
			}
		}
		if f.Type == "error" && f.Error != nil {
			return &errFrameError{Code: f.Error.Code, Message: f.Error.Message}
		}
		*pending = append(*pending, st.applyFrame(f)...)
		if f.ID == want {
			if check != nil {
				if err := check(f); err != nil {
					return err
				}
			}
			return nil
		}
		f = nil
	}
}

// readLoop — чтение до обрыва/ctx; каждый кадр через roomState → события в ch.
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn, st *roomState, ch chan<- signaling.Event) error {
	for {
		f, err := readFrame(ctx, conn)
		if err != nil {
			return err // обрыв → PollLoop переподключится
		}
		if f.Type == "error" && f.Error != nil && f.ID == "" {
			// асинхронная ошибка: token_expired/invalid_ticket чинятся reconnect'ом
			// (свежие settings в connect), прочие — лог и продолжаем (не роняем звонок).
			fmt.Fprintf(c.cfg.Stderr, "signaling: error %s: %s\n", f.Error.Code, f.Error.Message)
			if frameErrAction(f.Error.Code) == frameErrRefetchTicket {
				return &errFrameError{Code: f.Error.Code, Message: f.Error.Message}
			}
			continue
		}
		for _, ev := range st.applyFrame(f) {
			if !sendEvent(ctx, ch, ev) {
				return nil
			}
		}
	}
}

// authBundle — параметры hello-аутентификации из signaling-settings: v1
// {userid, ticket} (приоритет helloAuthParams["1.0"] над корневыми полями —
// спайк Task 1) и v2 token (helloAuthParams["2.0"]).
type authBundle struct {
	userid  string
	ticket  string
	v2Token string
}

// fetchAuthParams — свежие auth-params из signaling-settings (Basic-auth;
// weblogin не нужен — дельта §2). Ticket не логируется.
func (c *Client) fetchAuthParams(ctx context.Context) (authBundle, error) {
	var data struct {
		Ticket string `json:"ticket"`
		UserId string `json:"userId"`
		HelloAuthParams struct {
			V1 struct {
				Userid string `json:"userid"`
				Ticket string `json:"ticket"`
			} `json:"1.0"`
			V2 struct {
				Token string `json:"token"`
			} `json:"2.0"`
		} `json:"helloAuthParams"`
	}
	if _, err := transport.DoOCS(ctx, c.cfg.Doer, c.cfg.Auth, http.MethodGet,
		pathSignalingSettings, nil, nil, false, &data); err != nil {
		return authBundle{}, err
	}
	b := authBundle{userid: data.UserId, ticket: data.Ticket, v2Token: data.HelloAuthParams.V2.Token}
	if v := data.HelloAuthParams.V1; v.Ticket != "" {
		b.userid, b.ticket = v.Userid, v.Ticket // приоритет helloAuthParams["1.0"]
	}
	return b, nil
}

// backendURL — полный URL OCS signaling-backend для hello.auth.url (BaseURL +
// backendPath). HPB его ВАЛИДИРУЕТ: без ocs/v2.php/apps/spreed/ —
// invalid_format (см. protocol.go, backendPath).
func (c *Client) backendURL() string {
	u := *c.cfg.Auth.BaseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + backendPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// setConn — потокобезопасная замена активного соединения (Send/ping видят
// его). nil заодно обнуляет state — снапшот живёт ровно одну WS-сессию.
func (c *Client) setConn(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	if conn == nil {
		c.state = nil
	}
	c.mu.Unlock()
}

// ---- кодирование кадров ----

// writeFrame — один исходящий JSON-кадр (единственный Writer в моменте —
// контракт coder/websocket).
func writeFrame(ctx context.Context, conn *websocket.Conn, f clientFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	w, err := conn.Writer(ctx, websocket.MessageText)
	if err != nil {
		return transport.SanitizeErr(err)
	}
	if _, err := w.Write(b); err != nil {
		_ = w.Close()
		return transport.SanitizeErr(err)
	}
	return transport.SanitizeErr(w.Close())
}

// readFrame — один входящий кадр + debug-дамп без секретов (дельта §4).
func readFrame(ctx context.Context, conn *websocket.Conn) (*serverFrame, error) {
	_, r, err := conn.Reader(ctx)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	hpbDebug("WS<< %s", redactTicket(string(b)))
	var f serverFrame
	if err := json.Unmarshal(b, &f); err != nil {
		// Поломанный кадр не роняет loop (как parseEnvelopes в call/signaling).
		hpbDebug("decode fail: %v", err)
		return &serverFrame{}, nil
	}
	return &f, nil
}

// redactTicket маскирует секреты в debug-дампе кадра: значения "ticket"
// (v1-hello params) и "token" (v2-hello params) — оба секрета живут в
// auth-params (дельта §4).
func redactTicket(s string) string {
	for _, key := range []string{"ticket", "token"} {
		s = redactJSONKey(s, key)
	}
	return s
}

// redactJSONKey маскирует значение строкового JSON-поля с именем key.
func redactJSONKey(s, key string) string {
	needle := `"` + key + `":"`
	i := strings.Index(s, needle)
	if i < 0 {
		return s
	}
	rest := s[i+len(needle):]
	if j := strings.Index(rest, `"`); j >= 0 {
		rest = rest[j:]
	}
	return s[:i] + `"` + key + `":"<REDACTED>"` + rest
}

// hpbDebug — отладка протокола (NCTALK_DEBUG=1), как signaling.signalingDebug.
func hpbDebug(format string, a ...any) {
	if os.Getenv("NCTALK_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "DEBUG hpbesignaling: "+format+"\n", a...)
}
