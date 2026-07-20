//go:build integration

// Файл интеграционного теста signaling на боевом сервере Nextcloud Talk
// (Task 2.3, спека 2026-07-19 §7). Компилируется ТОЛЬКО с тегом
// `-tags=integration` — заголовок //go:build integration выше исключает его из
// обычного `go test ./...`.
//
// Canonical flow (после фикса багов #1/#3/#5, подтверждён spike-gate 2026-07-20):
//
//	1. web-login → PHP-session в shared cookiejar (баг #5: без session pull → 404).
//	2. JoinRoom → participant session + ownSessionId (баг #3: без joinRoom pull → 404).
//	3. SetSessionId (исходящий POST signaling требует own sessionId).
//	4. JoinCall(token, flags=3) — войти в звонок.
//	5. PollLoop (GET /api/v3/signaling/{token} long-poll — баг #1: v3 не v4).
//	6. LeaveCall (cleanup).
//
// Сценарий мутационный (JoinRoom/JoinCall меняют состояние сервера), защищён
// двухслойно: env NCTALK_INTEGRATION_ROOM (token) + env NCTALK_INTEGRATION_CALL=1
// (opt-in). Без любого — t.Skip. Креды — NEXTCLOUD_URL/LOGIN/PASS через config.Load.
//
// НЕ делает: WebRTC/audio (нет pion-слоя — это signaling spike), не закрывает PC.
package signaling

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stas/nctalk/internal/call/weblogin"
	"github.com/stas/nctalk/internal/config"
	"github.com/stas/nctalk/internal/exit"
	"github.com/stas/nctalk/internal/transport"
)

const signalingPollTimeout = 5 * time.Second  // потолок ожидания signaling-событий
const signalingCallTimeout = 30 * time.Second // пер-операционный потолок JoinRoom/JoinCall/LeaveCall

// TestSignalingPollLoop_CanonicalFlow — e2e проверка исправленного signaling
// flow на Docker: web-login → joinRoom → SetSessionId → joinCall → pull (v3) →
// ≥1 Event (usersInRoom содержит наш session). Доказывает, что баги #1/#3/#5
// фиксированы (раньше pull → 404, 0 events).
func TestSignalingPollLoop_CanonicalFlow(t *testing.T) {
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропуск signaling integration test")
	}
	if got := os.Getenv("NCTALK_INTEGRATION_CALL"); got != "1" {
		t.Skip("NCTALK_INTEGRATION_CALL != 1 — пропуск signaling integration test (мутация)")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("пропуск signaling integration test: %v", err)
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		t.Fatalf("парсинг BaseURL %q: %v", cfg.BaseURL, err)
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}

	// SHARED cookiejar: loginClient (no-redirect для web-login) + httpClient
	// (same-host redirect для signaling/call). Session-cookie от weblogin живёт
	// в jar, signaling его подхватывает (баг #5).
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	loginClient := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	httpClient := &http.Client{
		Transport:     http.DefaultTransport,
		Timeout:       cfg.Timeout,
		Jar:           jar,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}

	// 1. web-login → PHP-session.
	loginCtx, loginCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
	defer loginCancel()
	if err := weblogin.Login(loginCtx, loginClient, auth); err != nil {
		t.Fatalf("weblogin.Login: %v (баг #5: без session signaling pull → 404)", err)
	}

	c := New(auth, httpClient)

	// 2. JoinRoom → participant session + ownSessionId (баг #3).
	roomCtx, roomCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
	defer roomCancel()
	sessionId, err := c.JoinRoom(roomCtx, token)
	if err != nil {
		t.Fatalf("JoinRoom(%s): %v — без participant session pull даст 404 (баг #3)", token, err)
	}
	if sessionId == "" {
		t.Fatal("JoinRoom вернул пустой sessionId — сервер не отдал data.sessionId")
	}
	t.Logf("JoinRoom OK: sessionId len=%d", len(sessionId))
	c.SetSessionId(sessionId)

	// 3. JoinCall: flags=3 (IN_CALL|WITH_AUDIO = sendrecv). WebRTC-слоя нет — флаг
	//    это метаданные для других участников; signaling работает и без реального
	//    аудио. Вызываем ПОСЛЕ joinRoom (баг #4: session создана joinRoom'ом).
	joinCtx, joinCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
	defer joinCancel()
	if err := c.JoinCall(joinCtx, token, 3); err != nil {
		t.Fatalf("JoinCall(%s, flags=3): %v", token, err)
	}

	// Гарантированный leave при любом исходе (отдельный ctx — pollCtx отменён).
	defer func() {
		leaveCtx, leaveCancel := context.WithTimeout(context.Background(), signalingCallTimeout)
		defer leaveCancel()
		if err := c.LeaveCall(leaveCtx, token); err != nil {
			t.Errorf("LeaveCall(%s): %v — участник уйдёт по ping-timeout ~60–90с", token, err)
		}
	}()

	// 4. PollLoop (v3 pull — баг #1) в goroutine. Буфер 8 — на пачку сообщений.
	events := make(chan Event, 8)
	pollCtx, pollCancel := context.WithTimeout(context.Background(), signalingPollTimeout)
	defer pollCancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.PollLoop(pollCtx, token, events) }()

	var observed []Event
	var pollErr error
loop:
	for {
		select {
		case ev := <-events:
			observed = append(observed, ev)
		case err := <-errCh:
			pollErr = err
		case <-pollCtx.Done():
			break loop
		}
	}
	if pollErr != nil {
		t.Errorf("PollLoop завершился раньше ctx: %v — фатальная ошибка signaling'а (401/403/404)", pollErr)
	}

	// Hard assert: ≥1 Event. После joinRoom+joinCall сервер включает нас в
	// usersInRoom snapshot на ближайшем pull'е. 0 событий = pull не отдал данные
	// (404 без session/joinRoom, или long-poll > 5с).
	if len(observed) == 0 {
		t.Fatalf("PollLoop за %s не вернул ни одного Event'а — сервер не отдал usersInRoom snapshot. "+
			"Возможные причины: (1) web-login/joinRoom не создали session → pull 404; "+
			"(2) long-poll дольше %s — увеличить signalingPollTimeout; (3) signaling отключён (hpb_mode?).",
			signalingPollTimeout, signalingPollTimeout)
	}

	// Логируем события для сверки формата с фикстурами Task 2.1.
	var sawOurSession bool
	for i, ev := range observed {
		t.Logf("Event[%d]: Kind=%s", i, kindName(ev.Kind))
		switch ev.Kind {
		case EvUsersUpdated:
			t.Logf("  usersInRoom: %d participant(s)", len(ev.Users))
			for j, u := range ev.Users {
				t.Logf("  user[%d]: sessionId=%s actorType=%s actorId=%s inCall=%d",
					j, u.SessionId, u.ActorType, u.ActorId, u.InCall)
				if u.SessionId == sessionId {
					sawOurSession = true
				}
			}
		case EvOffer, EvAnswer:
			t.Logf("  %s from=%s SDPlen=%d", kindName(ev.Kind), ev.From, len(ev.SDP))
		case EvCandidate:
			t.Logf("  candidate from=%s: %q", ev.From, ev.Candidate.Candidate)
		case EvError:
			var ee exit.ExitError
			if errors.As(ev.Err, &ee) {
				t.Errorf("  EvError: code=%d err=%v", ee.Code, ee.Err)
			} else {
				t.Errorf("  EvError: %v", ev.Err)
			}
		}
	}
	// Наш session должен быть в usersInRoom (joinRoom создал participant).
	if !sawOurSession {
		t.Errorf("own sessionId не найден в usersInRoom — joinRoom не создал participant-session?")
	}
}

// kindName — человекочитаемое имя EventKind для логов.
func kindName(k EventKind) string {
	switch k {
	case EvUsersUpdated:
		return "EvUsersUpdated"
	case EvOffer:
		return "EvOffer"
	case EvAnswer:
		return "EvAnswer"
	case EvCandidate:
		return "EvCandidate"
	case EvError:
		return "EvError"
	default:
		return "EvUnknown"
	}
}
