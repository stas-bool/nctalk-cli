//go:build integration

// Спайк на живом HPB (дельта §5.2 — ПЕРВЫЙ шаг реализации, до основной
// логики; правило базовой спеки §12: не compile-only). Доказывает
// канонический flow external-режима и фиксирует фактические поля кадров
// (подтверждено на живом HPB 2026-09-23):
//
//	settings (Basic-auth, weblogin НЕ нужен) → ticket/helloAuthParams →
//	WS wss://<server>/spreed → welcome(features) →
//	hello{version, auth:{url, params}} (2.0 → {token} при feature
//	hello-v2, иначе 1.0 → {userid, ticket}; type:"ticket" НЕ поддержан) →
//	hello-response(sessionid) →
//	room {roomid, sessionid: OCS из JoinRoom} → room-ack + event join.
//
// Все кадры дампов в t.Logf при NCTALK_DEBUG=1 (ticket маскируется — дельта §4).
// Захваченные обезличенные кадры ложатся в testdata/hpb/*.json.
//
// Мутационный (JoinRoom меняет состояние): env NCTALK_INTEGRATION_ROOM +
// NCTALK_INTEGRATION_HPB=1 (opt-in). Креды — NEXTCLOUD_URL/LOGIN/PASS.
// Сервер с HPB (боевой, TEST-комната); Docker internal-сервер этот тест
// пропустит (signalingMode != external).
package hpbesignaling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stas-bool/nctalk-cli/internal/config"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	spikeTimeout    = 15 * time.Second // общий потолок спайка
	spikeEventsWait = 10 * time.Second // окно чтения событий после room-ack
)

type spikeSettings struct {
	SignalingMode string `json:"signalingMode"`
	Server        string `json:"server"`
	Ticket        string `json:"ticket"`
	UserId        string `json:"userId"`
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

func TestHPBSpike_TicketHelloRoom(t *testing.T) {
	token := os.Getenv("NCTALK_INTEGRATION_ROOM")
	if token == "" {
		t.Skip("NCTALK_INTEGRATION_ROOM не задан — пропуск HPB-спайка")
	}
	if os.Getenv("NCTALK_INTEGRATION_HPB") != "1" {
		t.Skip("NCTALK_INTEGRATION_HPB != 1 — пропуск HPB-спайка (мутация)")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("пропуск HPB-спайка: %v", err)
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		t.Fatalf("парсинг BaseURL %q: %v", cfg.BaseURL, err)
	}
	auth := transport.Auth{BaseURL: baseURL, Login: cfg.Login, Password: cfg.Password}
	// Basic-auth достаточно; PHP-session (weblogin) нужна только OCS-pull,
	// который в external не используется (дельта §2). Timeout>0 — запросы
	// разовые; WS-диал пойдёт через отдельный клиент без Timeout.
	httpClient := &http.Client{
		Timeout:       cfg.Timeout,
		CheckRedirect: transport.SameHostRedirectPolicy,
	}
	ctx, cancel := context.WithTimeout(context.Background(), spikeTimeout)
	defer cancel()

	// 1. signaling-settings → режим + ticket (отдельного ticket-эндпоинта нет).
	var st spikeSettings
	p := "/ocs/v2.php/apps/spreed/api/v3/signaling/settings"
	if _, err := transport.DoOCS(ctx, httpClient, auth, http.MethodGet, p, nil, nil, false, &st); err != nil {
		t.Fatalf("signaling-settings: %v — без settings транспорт не выбрать (дельта §3)", err)
	}
	t.Logf("settings: signalingMode=%q server=%q userId=%q ticketPresent=%v",
		st.SignalingMode, st.Server, st.UserId, st.Ticket != "")
	if st.SignalingMode != "external" {
		t.Fatalf("signalingMode=%q — сервер без HPB, спайк не применим (это Docker/internal?)", st.SignalingMode)
	}
	userid, ticket := st.UserId, st.Ticket
	if v := st.HelloAuthParams.V1; v.Ticket != "" {
		userid, ticket = v.Userid, v.Ticket // приоритет helloAuthParams["1.0"]
	}
	if ticket == "" || userid == "" || st.Server == "" {
		t.Fatalf("settings без ticket/userid/server — уточнить формат по дампа выше")
	}

	// 2. JoinRoom — OCS-sessionId нужен room-join'у (проверка прав в NC).
	var roomData struct {
		SessionId string `json:"sessionId"`
	}
	jp := fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", token)
	if _, err := transport.DoOCS(ctx, httpClient, auth, http.MethodPost, jp, nil, nil, true, &roomData); err != nil {
		t.Fatalf("JoinRoom(%s): %v", token, err)
	}
	t.Logf("JoinRoom OK: OCS sessionId len=%d", len(roomData.SessionId))

	// 3. WS + hello(auth: ticket). WS-путь standalone-сервера — /spreed
	// (корень отдаёт 404; проверено на живом HPB спайком Task 1) — так же
	// строит URL JS-клиент spreed: scheme-swap + TrimSuffix("/") + "/spreed".
	wsURL := strings.Replace(strings.Replace(st.Server, "https://", "wss://", 1), "http://", "ws://", 1)
	if !strings.HasSuffix(wsURL, "/spreed") {
		wsURL = strings.TrimSuffix(wsURL, "/") + "/spreed"
	}
	dialer := &http.Client{Transport: http.DefaultTransport} // БЕЗ Timeout — длительное соединение
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: dialer})
	if err != nil {
		t.Fatalf("WS dial %s: %v", wsURL, transport.SanitizeErr(err))
	}
	defer conn.Close(websocket.StatusNormalClosure, "spike done")

	// 3a. welcome читаем ДО hello: feature-список определяет версию hello
	// (как JS-клиент spreed: hello-v2 + helloAuthParams["2.0"] → "2.0").
	var helloV2 bool
	welcomeDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(welcomeDeadline) {
		raw, ok := readSpikeFrame(t, ctx, conn, welcomeDeadline)
		if !ok {
			break
		}
		dumpSpikeFrame(t, raw, ticket)
		var w struct {
			Type    string `json:"type"`
			Welcome *struct {
				Version  string   `json:"version"`
				Features []string `json:"features"`
			} `json:"welcome"`
		}
		if err := json.Unmarshal(raw, &w); err == nil && w.Type == "welcome" && w.Welcome != nil {
			for _, f := range w.Welcome.Features {
				if f == "hello-v2" {
					helloV2 = true
				}
			}
			break
		}
	}

	// 3b. hello. Форма auth — как JS-клиент spreed (проверено на живом HPB
	// спайком Task 1; type:"ticket" отвергается кодом invalid_format — в
	// актуальном nextcloud-spreed-signaling поле type это ClientType со
	// значением по умолчанию "client"): {url: OCS backend-эндпоинт NC,
	// params: helloAuthParams[version]} — 2.0 → {token}, иначе 1.0 →
	// {userid, ticket} (приоритет helloAuthParams["1.0"] над корневым ticket).
	authParams := map[string]any{"userid": userid, "ticket": ticket}
	helloVersion := "1.0"
	if helloV2 && st.HelloAuthParams.V2.Token != "" {
		helloVersion, authParams = "2.0", map[string]any{"token": st.HelloAuthParams.V2.Token}
	}
	backendURL := cfg.BaseURL + "/ocs/v2.php/apps/spreed/api/v3/signaling/backend"
	writeSpike(t, ctx, conn, map[string]any{
		"id": "h", "type": "hello",
		"hello": map[string]any{
			"version": helloVersion,
			"auth":    map[string]any{"url": backendURL, "params": authParams},
		},
	})

	// 4. room-join отправлен сразу за hello; читаем поток до hello-response,
	// room-ack и своего entry в event join.
	var ownSid string
	deadline := time.Now().Add(spikeEventsWait)
	gotRoomAck, gotJoin := false, false
	writeSpike(t, ctx, conn, map[string]any{
		"id": "r", "type": "room",
		"room": map[string]any{"roomid": token, "sessionid": roomData.SessionId},
	})
	for time.Now().Before(deadline) && !(gotRoomAck && gotJoin) {
		raw, ok := readSpikeFrame(t, ctx, conn, deadline)
		if !ok {
			break
		}
		dumpSpikeFrame(t, raw, ticket)

		var f struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Hello *struct {
				SessionId string `json:"sessionid"`
			} `json:"hello"`
			Room *struct {
				RoomId string `json:"roomid"`
			} `json:"room"`
			Event *struct {
				Target string `json:"target"`
				Type   string `json:"type"`
				Join   []struct {
					SessionId string `json:"sessionid"`
				} `json:"join"`
			} `json:"event"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			continue // неканонический кадр — уже задамплен выше
		}
		switch {
		case f.Type == "hello" && f.Hello != nil:
			ownSid = f.Hello.SessionId
		case f.Type == "room" && f.Room != nil && f.Room.RoomId == token:
			gotRoomAck = true
		case f.Type == "event" && f.Event != nil && f.Event.Target == "room" && f.Event.Type == "join":
			for _, j := range f.Event.Join {
				if j.SessionId == ownSid {
					gotJoin = true // себя видели в join-списке
				}
			}
		}
	}

	// ГЕЙТ спайка (дельта §5.2): welcome+hello получены, room-ack + свой entry.
	if ownSid == "" {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: hello-response без sessionid — приветствие не получено")
	}
	if !gotRoomAck {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: room-ack не получен (проверить sessionid из JoinRoom)")
	}
	if !gotJoin {
		t.Fatal("ГЕЙТ ПРОВАЛЕН: event join со своим sessionid не получен")
	}
	t.Logf("ГЕЙТ ПРОЙДЕН: ownSid=%s…, room-ack OK, self-join OK", ownSid[:min(8, len(ownSid))])
}

// writeSpike отправляет JSON-кадр (тестовый helper).
func writeSpike(t *testing.T, ctx context.Context, conn *websocket.Conn, frame any) {
	t.Helper()
	w, err := conn.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatalf("ws writer: %v", err)
	}
	if err := json.NewEncoder(w).Encode(frame); err != nil {
		t.Fatalf("ws encode: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("ws flush: %v", err)
	}
}

// readSpikeFrame читает один WS-кадр до deadline (ok=false — конец окна).
func readSpikeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, deadline time.Time) (json.RawMessage, bool) {
	t.Helper()
	if time.Now().After(deadline) {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, time.Until(deadline))
	defer cancel()
	_, r, err := conn.Reader(cctx)
	if err != nil {
		return nil, false
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		t.Fatalf("decode кадра: %v", err)
	}
	return raw, true
}

// dumpSpikeFrame печатает кадр в t.Logf при NCTALK_DEBUG=1, маскируя ticket.
func dumpSpikeFrame(t *testing.T, raw json.RawMessage, ticket string) {
	t.Helper()
	if os.Getenv("NCTALK_DEBUG") == "" {
		return
	}
	s := string(raw)
	if ticket != "" {
		s = strings.ReplaceAll(s, ticket, "<TICKET-REDACTED>")
	}
	t.Logf("WS<< %s", s)
}
