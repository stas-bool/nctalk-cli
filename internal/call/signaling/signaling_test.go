// Package signaling — тесты OCS-polling signaling-клиента.
//
// Структура:
//   - парсер (parseEnvelopes): fixture-driven тесты на каждую фикстуру Task 2.1
//     (usersInRoom/offer/answer/candidate);
//   - PollLoop: retry/backoff (5xx → N retry → успех), 401/404 → EvError без retry,
//     ctx.Done → return nil;
//   - Send: исходящий POST — форма тела (form-encoded, messages=JSON-строка),
//     заголовки (Authorization/OCS-APIRequest/Accept/Content-Type);
//   - JoinCall/LeaveCall: мин-контракт — правильный path/method/body.
//
// Пакет внутренний (package signaling, а не signaling_test): тестируем и
// внутренние функции (parseEnvelopes, classifyPollErr, nextBackoff), и public API.
package signaling

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/exit"
	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// ---- helpers ----

// fixturePath возвращает абсолютный путь к фикстуре в testdata/signaling/.
// Фикстуры лежат в корне репо (рядом с go.mod), из пакета internal/call/signaling
// (cwd при go test) это ../../../testdata/signaling/<name>.json — signaling
// на один уровень глубже, чем internal/client (там достаточно ../../).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	// filepath.Rel здесь не нужен — go test запускается с cwd=пакет, поэтому
	// относительный путь работает и в CI, и локально.
	p := filepath.Join("..", "..", "..", "testdata", "signaling", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s недоступен: %v", name, err)
	}
	return p
}

// loadFixture загружает JSON-фикстуру и возвращает распарсенный OCS-конверт:
// из ocs.data извлекается массив signalingEnvelope, который и тестируем.
func loadFixtureEnvelopes(t *testing.T, name string) []signalingEnvelope {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	// Используем transport.OCSEnvelope — он совпадает с тем, что разворачивает
	// transport.DoOCS в PollLoop. Фикстуры содержат _comment/_source — они
	// игнорируются (нет DisallowUnknownFields, см. требования Task 2.2).
	var env transport.OCSEnvelope[json.RawMessage]
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope %s: %v", name, err)
	}
	var envelopes []signalingEnvelope
	if err := json.Unmarshal(env.OCS.Data, &envelopes); err != nil {
		t.Fatalf("unmarshal ocs.data array %s: %v", name, err)
	}
	return envelopes
}

// mockDoer — queue-based мок: Doer.Do возвращает элементы из responses по
// очереди, запросы складывает в requests (для пост-проверки method/path/body).
// При исчерпании очереди — возвращает 200 с пустым OCS-конвертом, чтобы
// PollLoop вышел на ctx.Done, а не упал с недетерминированной ошибкой.
type mockDoer struct {
	mu        sync.Mutex
	responses []mockResp
	requests  []*http.Request
	idx       int
}

type mockResp struct {
	status      int
	body        string
	err         error
	reqInspector func(*http.Request) // опц. проверка конкретного запроса
}

func (m *mockDoer) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Копируем тело, чтобы оно оставалось доступным для пост-проверки после
	// закрытия (http.Response.Body закрывается вызывающим; в тесте хотим
	// прочитать его позже — поэтому сразу drains в строку).
	var bodyCopy string
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		bodyCopy = string(b)
		req.Body = io.NopCloser(strings.NewReader(bodyCopy))
	}
	// Сохраняем клон запроса с requestBody для пост-проверки (без Body,
	// чтобы не зависеть от его закрытия).
	reqClone := req.Clone(req.Context())
	reqClone.Body = nil
	// Прикрепляем captured-тело через кастомное поле (через Header — нет, это
	// служебные данные; используем отдельный map в тестах).
	if reqClone.Header == nil {
		reqClone.Header = http.Header{}
	}
	reqClone.Header.Set("X-Test-Captured-Body", bodyCopy)
	m.requests = append(m.requests, reqClone)

	if m.idx >= len(m.responses) {
		// По умолчанию: 200 + пустой OCS-конверт.
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(ocsOK(nil))),
			Header:     make(http.Header),
		}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	if resp.err != nil {
		return nil, resp.err
	}
	return &http.Response{
		StatusCode: resp.status,
		Body:       io.NopCloser(strings.NewReader(resp.body)),
		Header:     make(http.Header),
	}, nil
}

func (m *mockDoer) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// ocsOK строит OCS-конверт с указанным data ( marshals to JSON).
func ocsOK(data any) string {
	env := transport.OCSEnvelope[any]{}
	env.OCS.Meta.Status = "ok"
	env.OCS.Meta.StatusCode = http.StatusOK
	env.OCS.Meta.Message = "OK"
	env.OCS.Data = data
	b, _ := json.Marshal(env)
	return string(b)
}

// ocsErrBody строит OCS-конверт с meta.statusCode = code (используется для
// симуляции 4xx/5xx OCS-ошибок).
func ocsErrBody(code int, msg string) string {
	env := transport.OCSEnvelope[any]{}
	env.OCS.Meta.Status = "failure"
	env.OCS.Meta.StatusCode = code
	env.OCS.Meta.Message = msg
	b, _ := json.Marshal(env)
	return string(b)
}

// mustParseURL — тестовый хелпер для Auth.BaseURL.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// newTestClient — Client с очень короткими backoff-границами (для retry-тестов).
func newTestClient(doer transport.Doer) *Client {
	c := New(Auth{BaseURL: mustParseURL("https://nc.example.org"), Login: "alice", Password: "secret"}, doer)
	c.backoffBase = 1 * time.Millisecond
	c.backoffMax = 5 * time.Millisecond
	return c
}

// ---- парсер: fixture-driven тесты ----

// TestParse_UsersInRoom — комплексная фикстура: 1 offer + 1 candidate + финальный
// usersInRoom на 2 участника. Должно получиться 3 события: EvOffer, EvCandidate,
// EvUsersUpdated(2). Проверяем все поля, включая From/SDP у offer, IP у candidate,
// ActorId/SessionId/InCall у users.
func TestParse_UsersInRoom(t *testing.T) {
	envelopes := loadFixtureEnvelopes(t, "usersInRoom.json")
	events := parseEnvelopes(envelopes)

	if got, want := len(events), 3; got != want {
		t.Fatalf("len(events) = %d, want %d (offer + candidate + users)", got, want)
	}

	// [0] offer
	if events[0].Kind != EvOffer {
		t.Errorf("events[0].Kind: got %v, want EvOffer", events[0].Kind)
	}
	if events[0].From != "SESSION_2" {
		t.Errorf("events[0].From: got %q, want SESSION_2", events[0].From)
	}
	if !strings.Contains(events[0].SDP, "a=setup:actpass") {
		t.Errorf("events[0].SDP не содержит offer-маркер a=setup:actpass: %q", events[0].SDP)
	}
	if !strings.Contains(events[0].SDP, "m=audio") {
		t.Errorf("events[0].SDP не содержит m=audio line: %q", events[0].SDP)
	}

	// [1] candidate (host)
	if events[1].Kind != EvCandidate {
		t.Errorf("events[1].Kind: got %v, want EvCandidate", events[1].Kind)
	}
	if events[1].From != "SESSION_2" {
		t.Errorf("events[1].From: got %q, want SESSION_2", events[1].From)
	}
	if got, want := events[1].Candidate.Candidate, "candidate:842163049 1 udp"; !strings.HasPrefix(got, want) {
		t.Errorf("events[1].Candidate.Candidate: got %q, want prefix %q", got, want)
	}
	if events[1].Candidate.SDPMLineIndex == nil || *events[1].Candidate.SDPMLineIndex != 0 {
		t.Errorf("events[1].Candidate.SDPMLineIndex: got %v, want 0", events[1].Candidate.SDPMLineIndex)
	}
	if events[1].Candidate.SDPMid == nil || *events[1].Candidate.SDPMid != "0" {
		t.Errorf("events[1].Candidate.SDPMid: got %v, want \"0\"", events[1].Candidate.SDPMid)
	}

	// [2] usersInRoom (2 участника)
	if events[2].Kind != EvUsersUpdated {
		t.Errorf("events[2].Kind: got %v, want EvUsersUpdated", events[2].Kind)
	}
	if got, want := len(events[2].Users), 2; got != want {
		t.Fatalf("len(events[2].Users) = %d, want %d", got, want)
	}
	alice := events[2].Users[0]
	if alice.SessionId != "SESSION_1" || alice.ActorId != "alice" || alice.ActorType != "users" || alice.InCall != 3 {
		t.Errorf("Users[0] = %+v, want {SESSION_1 alice users 3}", alice)
	}
	bob := events[2].Users[1]
	if bob.SessionId != "SESSION_2" || bob.ActorId != "bob" || bob.InCall != 3 {
		t.Errorf("Users[1] = %+v, want {SESSION_2 bob [users] 3}", bob)
	}
}

// TestParse_Offer — одна входящая message{offer} + финальный пустой usersInRoom.
// Ключевая проверка: двойной unmarshal (data → string → inner object).
func TestParse_Offer(t *testing.T) {
	envelopes := loadFixtureEnvelopes(t, "offer.json")
	events := parseEnvelopes(envelopes)

	if got, want := len(events), 2; got != want {
		t.Fatalf("len(events) = %d, want %d (offer + empty users)", got, want)
	}
	if events[0].Kind != EvOffer {
		t.Errorf("events[0].Kind: got %v, want EvOffer", events[0].Kind)
	}
	if events[0].From != "SESSION_2" {
		t.Errorf("events[0].From: got %q, want SESSION_2", events[0].From)
	}
	if !strings.Contains(events[0].SDP, "a=sendrecv") {
		t.Errorf("events[0].SDP не содержит a=sendrecv: %q", events[0].SDP)
	}
	// Финальный usersInRoom с пустым data — всё равно доставляется (потребитель
	// может его игнорировать, но сигнал «снапшот получен» нужен peer-слою).
	if events[1].Kind != EvUsersUpdated {
		t.Errorf("events[1].Kind: got %v, want EvUsersUpdated", events[1].Kind)
	}
	if len(events[1].Users) != 0 {
		t.Errorf("events[1].Users: got %d, want 0 (пустой снапшот)", len(events[1].Users))
	}
}

// TestParse_Answer — одна входящая message{answer}. SDP должен быть setup:active
// (маркер responder-стороны).
func TestParse_Answer(t *testing.T) {
	envelopes := loadFixtureEnvelopes(t, "answer.json")
	events := parseEnvelopes(envelopes)

	// 2 события: answer + пустой usersInRoom.
	if got, want := len(events), 2; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Kind != EvAnswer {
		t.Fatalf("events[0].Kind: got %v, want EvAnswer", events[0].Kind)
	}
	if events[0].From != "SESSION_1" {
		t.Errorf("events[0].From: got %q, want SESSION_1 (answer приходит от responder'а)", events[0].From)
	}
	if !strings.Contains(events[0].SDP, "a=setup:active") {
		t.Errorf("events[0].SDP не содержит a=setup:active (answer = responder): %q", events[0].SDP)
	}
}

// TestParse_Candidate — 3 кандидата: host + srflx + end-of-candidates.
// End-of-candidates marker (payload.candidate === "") тоже доставляется как
// EvCandidate с пустой строкой — peer-слой трактует как конец trickle.
func TestParse_Candidate(t *testing.T) {
	envelopes := loadFixtureEnvelopes(t, "candidate.json")
	events := parseEnvelopes(envelopes)

	// 3 candidate + 1 пустой usersInRoom = 4 события.
	if got, want := len(events), 4; got != want {
		t.Fatalf("len(events) = %d, want %d (3 candidates + 1 users)", got, want)
	}

	// [0] host candidate
	if events[0].Kind != EvCandidate {
		t.Fatalf("events[0].Kind: got %v, want EvCandidate", events[0].Kind)
	}
	if !strings.Contains(events[0].Candidate.Candidate, "typ host") {
		t.Errorf("events[0].Candidate: want 'typ host', got %q", events[0].Candidate.Candidate)
	}

	// [1] srflx candidate
	if !strings.Contains(events[1].Candidate.Candidate, "typ srflx") {
		t.Errorf("events[1].Candidate: want 'typ srflx', got %q", events[1].Candidate.Candidate)
	}

	// [2] end-of-candidates marker (пустая строка)
	if events[2].Candidate.Candidate != "" {
		t.Errorf("events[2].Candidate.Candidate: got %q, want \"\" (end-of-candidates)", events[2].Candidate.Candidate)
	}
	if events[2].Kind != EvCandidate {
		t.Errorf("events[2].Kind: got %v, want EvCandidate (end-of-candidates доставляется как EvCandidate)", events[2].Kind)
	}

	// [3] usersInRoom
	if events[3].Kind != EvUsersUpdated {
		t.Errorf("events[3].Kind: got %v, want EvUsersUpdated", events[3].Kind)
	}
}

// TestWrapCandidatePayload — ИСХОДЯЩИЙ candidate-payload должен быть ВЛОЖЕННЫМ
// объектом {candidate:{candidate,sdpMLineIndex,sdpMid}} (симметрично входящему
// icePayload), иначе удалённый Spreed-клиент в talk-main.js вызывает
// pc.addIceCandidate(a.payload.candidate) со СТРОКОЙ → TypeError ×N → peer-pipeline
// не достраивается → нет audio-sink → нет звука (баг #6, spike-gate 2026-07-20).
// Вход — плоский pion-формат ICECandidateInit от c.ToJSON(): {candidate,sdpMLineIndex,sdpMid}.
//
// WrapCandidatePayload живёт в signaling (wire-format там), не зависит от webrtc —
// принимает уже смаршаленный init как json.RawMessage.
func TestWrapCandidatePayload(t *testing.T) {
	// Плоский pion ICECandidateInit (строка candidate на верхнем уровне).
	initJSON := json.RawMessage(`{"candidate":"candidate:842163049 1 udp 1677729535 203.0.113.10 49152 typ host","sdpMLineIndex":0,"sdpMid":"0"}`)
	out, err := WrapCandidatePayload(initJSON)
	if err != nil {
		t.Fatalf("WrapCandidatePayload: %v", err)
	}

	// 1) Результат должен парситься как icePayload (тот же тип, что входящий
	//    candidate) — доказывает симметрию входящего/исходящего wire-format'а.
	var p icePayload
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("результат не парсится как icePayload (вложенный формат): %v\nraw=%s", err, out)
	}
	if p.Candidate.Candidate != "candidate:842163049 1 udp 1677729535 203.0.113.10 49152 typ host" {
		t.Errorf("nested Candidate: got %q", p.Candidate.Candidate)
	}
	if p.Candidate.SDPMLineIndex == nil || *p.Candidate.SDPMLineIndex != 0 {
		t.Errorf("nested SDPMLineIndex: got %v, want 0", p.Candidate.SDPMLineIndex)
	}
	if p.Candidate.SDPMid == nil || *p.Candidate.SDPMid != "0" {
		t.Errorf("nested SDPMid: got %v, want %q", p.Candidate.SDPMid, "0")
	}

	// 2) На верхнем уровне — ТОЛЬКО ключ "candidate" (объект-обёртка). Плоские
	//    sdpMLineIndex/sdpMid снаружи = баг #6 (browser addIceCandidate падает).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("unmarshal top-level map: %v", err)
	}
	if _, ok := top["candidate"]; !ok {
		t.Fatal("нет ключа \"candidate\" на верхнем уровне (формат не вложен)")
	}
	if _, ok := top["sdpMLineIndex"]; ok {
		t.Error("плоский \"sdpMLineIndex\" на верхнем уровне — формат плоский, не вложенный (баг #6)")
	}
	if _, ok := top["sdpMid"]; ok {
		t.Error("плоский \"sdpMid\" на верхнем уровне — формат плоский, не вложенный (баг #6)")
	}
}

// TestParse_EmptyData — пустой ocs.data (или только usersInRoom с пустым списком)
// не должен падать.
func TestParse_EmptyData(t *testing.T) {
	events := parseEnvelopes(nil)
	if len(events) != 0 {
		t.Errorf("parseEnvelopes(nil): got %d events, want 0", len(events))
	}
}

// TestParse_MalformedData_SilentSkip — повреждённая запись data не роняет
// весь parse: некритичная parse-ошибка скипается без шума (поломанная запись
// не должна убивать signaling-loop).
func TestParse_MalformedData_SilentSkip(t *testing.T) {
	cases := []struct {
		name     string
		env      signalingEnvelope
		wantKind EventKind // ожидаемый Kind первой записи ИЛИ EvError если 0
		wantLen  int
	}{
		{
			name: "usersInRoom with broken array",
			env: signalingEnvelope{
				Type: "usersInRoom",
				Data: json.RawMessage(`"not an array"`),
			},
			wantLen: 0,
		},
		{
			name: "message data is not a JSON string",
			env: signalingEnvelope{
				Type: "message",
				Data: json.RawMessage(`{"object":"instead of string"}`),
			},
			wantLen: 0,
		},
		{
			name: "message with valid string but broken inner JSON",
			env: signalingEnvelope{
				Type: "message",
				Data: json.RawMessage(`"not valid json"`),
			},
			wantLen: 0,
		},
		{
			name: "unknown type skipped",
			env: signalingEnvelope{
				Type: "control",
				Data: json.RawMessage(`"anything"`),
			},
			wantLen: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := parseEnvelopes([]signalingEnvelope{tc.env})
			if got := len(events); got != tc.wantLen {
				t.Errorf("len(events) = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

// ---- PollLoop: retry/backoff ----

// TestPollLoop_5xxRetriesThenSucceeds — спека §7: 5xx → exp-backoff retry,
// loop НЕ выходит. Мок возвращает 500 (OCS statusCode=500) N=3 раза, потом
// 200 с пустым data. PollLoop должен сделать N+1 запросов и выйти по ctx.Done
// без ошибок.
func TestPollLoop_5xxRetriesThenSucceeds(t *testing.T) {
	const N = 3
	doer := &mockDoer{
		responses: []mockResp{
			{status: 500, body: ocsErrBody(500, "internal server error")},
			{status: 500, body: ocsErrBody(500, "internal server error")},
			{status: 500, body: ocsErrBody(500, "internal server error")},
			// 4-й ответ: 200 с пустым data — после этого mockDoer будет
			// возвращать 200 indefinitely до ctx.Cancel.
			{status: 200, body: ocsOK(nil)},
		},
	}
	c := newTestClient(doer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Event, 16)

	// Запускаем PollLoop; через короткий timeout отменяем — к тому моменту
	// мок уже отдаст все 4 ответа и накопит запросы.
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	// Ждём пока мок отдаст все ответы (N+1 запросов).Poll loop крутится быстро,
	// т.к. backoffBase=1ms.
	deadline := time.After(2 * time.Second)
	for doer.requestCount() < N+1 {
		select {
		case <-deadline:
			t.Fatalf("таймаут: сделано %d запросов, want >= %d", doer.requestCount(), N+1)
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Ни одно EvError не должно было прийти в канал (5xx — не fatal).
	for _, ev := range drainChan(ch) {
		if ev.Kind == EvError {
			t.Fatalf("получили EvError на 5xx — а должны были retry'ить: %v", ev.Err)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("PollLoop вернул ошибку после ctx.Done: %v, want nil", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("PollLoop не завершился после ctx.Cancel")
	}
}

// TestPollLoop_NetworkErrorRetries — сетевая ошибка (не OCS) тоже должна
// ретраиться с backoff, а не ронять loop. Спека §7: «сетевой сбой / timeout».
func TestPollLoop_NetworkErrorRetries(t *testing.T) {
	const N = 2
	doer := &mockDoer{
		responses: []mockResp{
			{err: errors.New("simulated connection refused")},
			{err: errors.New("simulated timeout")},
			// 3-й ответ — успех.
			{status: 200, body: ocsOK([]any{})},
		},
	}
	c := newTestClient(doer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Event, 16)

	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	deadline := time.After(2 * time.Second)
	for doer.requestCount() < N+1 {
		select {
		case <-deadline:
			t.Fatalf("таймаут: %d запросов, want >= %d", doer.requestCount(), N+1)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestPollLoop_401_EvError_Exit1 — спека §7: 401/403 → exit 1 без retry.
// PollLoop должен сделать ровно 1 запрос и вернуть ошибку; в канале —
// EvError с *exit.ExitError{Code:1}.
func TestPollLoop_401_EvError_Exit1(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{
			{status: 401, body: ocsErrBody(401, "Unauthorized")},
		},
	}
	c := newTestClient(doer)
	ch := make(chan Event, 4)
	done := make(chan error, 1)

	go func() { done <- c.PollLoop(context.Background(), "tok-test", ch) }()

	select {
	case ev := <-ch:
		if ev.Kind != EvError {
			t.Fatalf("Event.Kind: got %v, want EvError", ev.Kind)
		}
		// exit.ExitError — ЗНАЧИМЫЙ тип (value), не указатель: exit.Exit возвращает
		// значение, поэтому errors.As ищет именно ExitError в цепочке, не *ExitError.
		var ee exit.ExitError
		if !errors.As(ev.Err, &ee) {
			t.Fatalf("Event.Err: got %T (%v), want exit.ExitError в цепочке", ev.Err, ev.Err)
		}
		if ee.Code != exit.ExitGeneric {
			t.Errorf("ExitError.Code: got %d, want %d (ExitGeneric, 401 → exit 1)", ee.Code, exit.ExitGeneric)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("не дождались EvError в канале")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("PollLoop вернул nil, want error (401 должен вернуть err)")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("PollLoop не завершился после 401")
	}
	if got, want := doer.requestCount(), 1; got != want {
		t.Errorf("сделано запросов: %d, want %d (401 — без retry)", got, want)
	}
}

// TestPollLoop_403_EvError_Exit1 — 403 (forbidden) тоже → exit 1 без retry.
func TestPollLoop_403_EvError_Exit1(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{
			{status: 403, body: ocsErrBody(403, "Forbidden")},
		},
	}
	c := newTestClient(doer)
	ch := make(chan Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok-test", ch) }()

	select {
	case ev := <-ch:
		var ee exit.ExitError
		if !errors.As(ev.Err, &ee) || ee.Code != exit.ExitGeneric {
			t.Errorf("Event: got %+v, want EvError with ExitError.Code=ExitGeneric", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("не дождались EvError в канале")
	}
	<-done
	if got := doer.requestCount(); got != 1 {
		t.Errorf("запросов: %d, want 1", got)
	}
}

// TestPollLoop_404_EvError_Exit2 — спека §7: 404 → exit 2 без retry.
func TestPollLoop_404_EvError_Exit2(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{
			{status: 404, body: ocsErrBody(404, "Room not found")},
		},
	}
	c := newTestClient(doer)
	ch := make(chan Event, 4)
	done := make(chan error, 1)
	go func() { done <- c.PollLoop(context.Background(), "tok-test", ch) }()

	select {
	case ev := <-ch:
		if ev.Kind != EvError {
			t.Fatalf("Event.Kind: got %v, want EvError", ev.Kind)
		}
		var ee exit.ExitError
		if !errors.As(ev.Err, &ee) {
			t.Fatalf("Event.Err: got %T, want exit.ExitError в цепочке", ev.Err)
		}
		if ee.Code != exit.ExitNotFound {
			t.Errorf("ExitError.Code: got %d, want %d (ExitNotFound, 404 → exit 2)", ee.Code, exit.ExitNotFound)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("не дождались EvError в канале")
	}
	<-done
	if got := doer.requestCount(); got != 1 {
		t.Errorf("запросов: %d, want 1 (404 — без retry)", got)
	}
}

// TestPollLoop_CtxDone_ReturnsNil — отмена ctx до первого ответа → return nil,
// без записи в канал (штатный leave).
func TestPollLoop_CtxDone_ReturnsNil(t *testing.T) {
	// Мок без ответов вообще (mockDoer с пустой очередью сразу вернёт 200).
	// Чтобы первый запрос завис — используем канал-барьер в mockInspector.
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	doer := &blockingDoer{started: started, block: block}
	c := newTestClient(doer)
	ch := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()
	<-started // дожидаемся, что PollLoop уже в середине первого запроса
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("PollLoop после ctx.Done: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PollLoop не завершился после ctx.Cancel")
	}
	// Канал должен остаться пустым (EvError не отправлялся).
	select {
	case ev := <-ch:
		t.Errorf("канал не пуст: получили %v, want пусто", ev)
	default:
	}
	close(block) // отпустить blockingDoer, чтобы goroutine не утекла
}

// blockingDoer блокирует Do до закрытия block, сигнализируя в started о начале.
// Используется в TestPollLoop_CtxDone_ReturnsNil, чтобы точно отловить момент,
// когда PollLoop ушёл в первый запрос.
type blockingDoer struct {
	started chan<- struct{}
	block   <-chan struct{}
}

func (b *blockingDoer) Do(req *http.Request) (*http.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.block:
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(ocsOK(nil))),
			Header:     make(http.Header),
		}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// TestPollLoop_ParsesAndEmitsEvents — успешный 200 с реальной payload'ой
// (предварительно свёрнутой из фикстуры offer.json). Должен разобрать и
// отправить в канал EvOffer + EvUsersUpdated.
func TestPollLoop_ParsesAndEmitsEvents(t *testing.T) {
	// Загружаем фикстуру offer.json целиком — это полный OCS-конверт, который
	// mockDoer отдаст как тело 200-ответа.
	offerFixture := loadRawFixture(t, "offer.json")

	doer := &mockDoer{
		responses: []mockResp{
			{status: 200, body: offerFixture},
		},
	}
	c := newTestClient(doer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan Event, 16)

	done := make(chan error, 1)
	go func() { done <- c.PollLoop(ctx, "tok-test", ch) }()

	// Ждём оба события (offer + usersInRoom).
	var got []Event
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("таймаут: получили %d событий, want >= 2", len(got))
		}
	}
	cancel()
	<-done

	if got[0].Kind != EvOffer {
		t.Errorf("got[0].Kind: got %v, want EvOffer", got[0].Kind)
	}
	if got[0].From != "SESSION_2" {
		t.Errorf("got[0].From: got %q, want SESSION_2", got[0].From)
	}
	if got[1].Kind != EvUsersUpdated {
		t.Errorf("got[1].Kind: got %v, want EvUsersUpdated", got[1].Kind)
	}
}

// ---- Send: форма POST-запроса ----

// TestSend_WireFormat — детальная проверка формы исходящего запроса:
//   - method/path: POST /ocs/v2.php/apps/spreed/api/v3/signaling/{token};
//   - Content-Type: application/x-www-form-urlencoded (НЕ application/json);
//   - тело: form-encoded с ключом messages, значение которого — JSON-строка
//     массива [{ev:"message", fn:"<inner JSON string>", sessionId:"<own>"}];
//   - inner payload: {type, to, roomType:"video", payload} — БЕЗ поля from
//     (его подставит сервер).
//   - Authorization: Basic <base64(login:pass)> присутствует.
func TestSend_WireFormat(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{{status: 200, body: ocsOK(nil)}},
	}
	c := newTestClient(doer)
	c.SetSessionId("SESSION_1")

	sdp := `{"type":"offer","sdp":"v=0\r\n..."}`
	err := c.Send(context.Background(), "tok-test", Message{
		Type:    "offer",
		To:      "SESSION_2",
		Payload: json.RawMessage(sdp),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := doer.requestCount(); got != 1 {
		t.Fatalf("запросов: %d, want 1", got)
	}
	req := doer.requests[0]
	if got, want := req.Method, http.MethodPost; got != want {
		t.Errorf("Method: got %q, want %q", got, want)
	}
	if got, want := req.URL.Path, "/ocs/v2.php/apps/spreed/api/v3/signaling/tok-test"; got != want {
		t.Errorf("URL.Path: got %q, want %q", got, want)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type: got %q, want application/x-www-form-urlencoded", ct)
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("Authorization: got %q, want Basic ...", auth)
	} else {
		// Проверяем, что в Basic-auth лежит alice:secret.
		// Decoding base64 — лишний шаг для теста, но даёт гарантию что креды
		// собираются именно из auth.Login/auth.Password.
		enc := strings.TrimPrefix(auth, "Basic ")
		dec, derr := base64Decode(enc)
		if derr != nil {
			t.Errorf("base64 decode Authorization: %v", derr)
		} else if dec != "alice:secret" {
			t.Errorf("Authorization decoded: got %q, want alice:secret", dec)
		}
	}
	if got := req.Header.Get("OCS-APIRequest"); got != "true" {
		t.Errorf("OCS-APIRequest: got %q, want \"true\"", got)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept: got %q, want \"application/json\"", got)
	}

	// Тело — form-encoded. Достаём messages и парсим дважды (это строка JSON).
	bodyStr := req.Header.Get("X-Test-Captured-Body")
	vals, err := url.ParseQuery(bodyStr)
	if err != nil {
		t.Fatalf("ParseQuery body: %v", err)
	}
	messagesRaw := vals.Get("messages")
	if messagesRaw == "" {
		t.Fatalf("messages empty in body: %q", bodyStr)
	}
	// messages — JSON-массив записей.
	var records []map[string]any
	if err := json.Unmarshal([]byte(messagesRaw), &records); err != nil {
		t.Fatalf("unmarshal messages array: %v\nraw: %s", err, messagesRaw)
	}
	if len(records) != 1 {
		t.Fatalf("len(records): got %d, want 1", len(records))
	}
	r := records[0]
	if got, want := r["ev"], "message"; got != want {
		t.Errorf("record[ev]: got %v, want %q", got, want)
	}
	if got, want := r["sessionId"], "SESSION_1"; got != want {
		t.Errorf("record[sessionId]: got %v, want %q", got, want)
	}
	// fn — строка-JSON внутреннего payload'а.
	fnStr, ok := r["fn"].(string)
	if !ok {
		t.Fatalf("record[fn]: got %T, want string (%v)", r["fn"], r["fn"])
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(fnStr), &inner); err != nil {
		t.Fatalf("unmarshal inner payload (fn): %v\nraw: %s", err, fnStr)
	}
	if got, want := inner["type"], "offer"; got != want {
		t.Errorf("inner[type]: got %v, want %q", got, want)
	}
	if got, want := inner["to"], "SESSION_2"; got != want {
		t.Errorf("inner[to]: got %v, want %q", got, want)
	}
	if got, want := inner["roomType"], "video"; got != want {
		t.Errorf("inner[roomType]: got %v, want %q (audio-only тоже video!)", got, want)
	}
	if _, hasFrom := inner["from"]; hasFrom {
		t.Errorf("inner[from] присутствует, но его НЕ должно быть в исходящем POST (сервер подставит сам)")
	}
	// payload внутри — исходный SDP-JSON.
	pl, ok := inner["payload"].(map[string]any)
	if !ok {
		t.Fatalf("inner[payload]: got %T, want map (%v)", inner["payload"], inner["payload"])
	}
	if got, want := pl["type"], "offer"; got != want {
		t.Errorf("payload[type]: got %v, want %q", got, want)
	}
	if got, want := pl["sdp"].(string), "v=0\r\n..."; got != want {
		t.Errorf("payload[sdp]: got %q, want %q", got, want)
	}
}

// TestSend_OCSError_Propagated — серверная OCS-ошибка (>=400) возвращается как
// *transport.OCSError с корректным Code (нужно для exit-маппинга в вызывающем).
func TestSend_OCSError_Propagated(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{
			{status: 404, body: ocsErrBody(404, "Room not found")},
		},
	}
	c := newTestClient(doer)
	c.SetSessionId("SESSION_1")
	err := c.Send(context.Background(), "tok-test", Message{Type: "offer"})
	if err == nil {
		t.Fatal("Send: got nil, want error")
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): got %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
}

// TestSend_Server500_OCSBody_ClassifiedAsBackoff — проверка, что 5xx от Send
// тоже возвращает OCSError с Code >= 500 (вызывающий может отличить transient
// от fatal). Используется потребителем для решения, делать ли retry.
func TestSend_Server500_OCSBody(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{
			{status: 500, body: ocsErrBody(500, "internal")},
		},
	}
	c := newTestClient(doer)
	c.SetSessionId("SESSION_1")
	err := c.Send(context.Background(), "tok-test", Message{Type: "offer"})
	if err == nil {
		t.Fatal("Send: got nil, want error")
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) || oe.Code != 500 {
		t.Errorf("OCSError.Code: got %v (%v), want 500", oe, err)
	}
}

// ---- JoinCall / LeaveCall ----

// TestJoinCall_PostsFlags — POST /call/{token} с JSON body {"flags": N}.
// flags=3 (IN_CALL|WITH_AUDIO = sendrecv) — спека §6.
func TestJoinCall_PostsFlags(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{{status: 200, body: ocsOK(nil)}},
	}
	c := newTestClient(doer)
	if err := c.JoinCall(context.Background(), "tok-test", 3); err != nil {
		t.Fatalf("JoinCall: %v", err)
	}
	if got := doer.requestCount(); got != 1 {
		t.Fatalf("запросов: %d, want 1", got)
	}
	req := doer.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("Method: got %q, want POST", req.Method)
	}
	if got, want := req.URL.Path, "/ocs/v2.php/apps/spreed/api/v4/call/tok-test"; got != want {
		t.Errorf("URL.Path: got %q, want %q", got, want)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json (DoOCS mutate=true)", ct)
	}
	bodyStr := req.Header.Get("X-Test-Captured-Body")
	var got struct {
		Flags int `json:"flags"`
	}
	if err := json.Unmarshal([]byte(bodyStr), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Flags != 3 {
		t.Errorf("body.flags: got %d, want 3", got.Flags)
	}
}

// TestLeaveCall_Deletes — DELETE /call/{token} без тела.
func TestLeaveCall_Deletes(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{{status: 200, body: ocsOK(nil)}},
	}
	c := newTestClient(doer)
	if err := c.LeaveCall(context.Background(), "tok-test"); err != nil {
		t.Fatalf("LeaveCall: %v", err)
	}
	if got := doer.requestCount(); got != 1 {
		t.Fatalf("запросов: %d, want 1", got)
	}
	req := doer.requests[0]
	if req.Method != http.MethodDelete {
		t.Errorf("Method: got %q, want DELETE", req.Method)
	}
	if got, want := req.URL.Path, "/ocs/v2.php/apps/spreed/api/v4/call/tok-test"; got != want {
		t.Errorf("URL.Path: got %q, want %q", got, want)
	}
	bodyStr := req.Header.Get("X-Test-Captured-Body")
	if bodyStr != "" {
		t.Errorf("Body у LeaveCall должен быть пустым, got %q", bodyStr)
	}
}

// ---- helpers для тестов ----

// drainChan вычитывает все события из буферизованного канала без блокировки.
func drainChan(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// loadRawFixture возвращает сырой JSON фикстуры как строку (для подачи в mockDoer
// как тело HTTP-ответа).
func loadRawFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// base64Decode — обёртка для теста Authorization (вынесено, чтобы профиль-
// ные импорты теста оставались компактными).
func base64Decode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- nextBackoff: экспоненциальный рост + потолок (спека §7) ----

// TestNextBackoff_ExponentialGrowthThenCap — 0 → base, base → 2*base, …, до max.
// После достижения потолка стабилизируется на max (не растёт дальше).
func TestNextBackoff_ExponentialGrowthThenCap(t *testing.T) {
	const base = 1 * time.Second
	const max = 30 * time.Second
	want := []time.Duration{
		1 * time.Second,  // 0 → base
		2 * time.Second,  // base → 2*base
		4 * time.Second,  // → 4
		8 * time.Second,  // → 8
		16 * time.Second, // → 16
		30 * time.Second, // 32 cap'ится до max
		30 * time.Second, // остаётся на max
		30 * time.Second, // и далее
	}
	var prev time.Duration
	for i, w := range want {
		got := nextBackoff(prev, base, max)
		if got != w {
			t.Errorf("[%d] nextBackoff(%v) = %v, want %v", i, prev, got, w)
		}
		prev = got
	}
}

// TestNextBackoff_ClientDefaults — production-конструктор ставит base=1s/max=30s
// (спека §7). Тест защищает от случайной правки дефолтов в New.
func TestNextBackoff_ClientDefaults(t *testing.T) {
	c := New(Auth{}, nil)
	if c.backoffBase != 1*time.Second {
		t.Errorf("backoffBase default: got %v, want 1s", c.backoffBase)
	}
	if c.backoffMax != 30*time.Second {
		t.Errorf("backoffMax default: got %v, want 30s", c.backoffMax)
	}
}

// ---- изоляция: signaling НЕ должен импортировать cli/render ----

// TestSignalingDoesNotImportCLIorRender — статическая защита от случайной
// циклической зависимости (спека §3). Запускаем через go list -deps.
func TestSignalingDoesNotImportCLIorRender(t *testing.T) {
	// Резолвим путь к пакету через go list из репо-рута. cwd теста = пакет
	// (internal/call/signaling), поэтому поднимаемся на 3 уровня.
	cmd := exec.Command("go", "list", "-deps", "./internal/call/signaling")
	cmd.Dir = "../../.."
	// На этой машине (macOS + Go 1.21.4) go list падает с dyld без CGO_ENABLED=0
	// (см. CLAUDE.md). Прокидываем env, чтобы тест был устойчив.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "github.com/stas-bool/nctalk-cli/internal/cli") ||
			strings.Contains(line, "github.com/stas-bool/nctalk-cli/internal/render") {
			t.Errorf("signaling не должен зависеть от cli/render: найдено %q", line)
		}
	}
}

// ---- JoinRoom (баг #3: participant session для signaling pull) ----

// TestJoinRoom_ReturnsSessionId — POST /api/v4/room/{token}/participants/active
// возвращает собственный sessionId (нужен для SetSessionId / ownSessionId-фильтра
// в agent). Без joinRoom signaling pull → 404 (CallController требует session).
func TestJoinRoom_ReturnsSessionId(t *testing.T) {
	const wantSID = "x9dla7zKOAB4haAbb11f7BcMu1sh84T1"
	doer := &mockDoer{
		responses: []mockResp{{
			status: 200,
			body:   ocsOK(map[string]any{"token": "tok-test", "sessionId": wantSID, "participantType": 1}),
		}},
	}
	c := newTestClient(doer)
	sid, err := c.JoinRoom(context.Background(), "tok-test")
	if err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
	if sid != wantSID {
		t.Errorf("sessionId: got %q, want %q", sid, wantSID)
	}
	if got := doer.requestCount(); got != 1 {
		t.Fatalf("запросов: %d, want 1", got)
	}
	req := doer.requests[0]
	if got, want := req.Method, http.MethodPost; got != want {
		t.Errorf("Method: got %q, want %q", got, want)
	}
	if got, want := req.URL.Path, "/ocs/v2.php/apps/spreed/api/v4/room/tok-test/participants/active"; got != want {
		t.Errorf("URL.Path: got %q, want %q", got, want)
	}
}

// TestJoinRoom_OCS404 — серверная ошибка (комната не найдена) возвращается как
// *transport.OCSError{Code:404} → cli-маппинг даст exit 2.
func TestJoinRoom_OCS404(t *testing.T) {
	doer := &mockDoer{
		responses: []mockResp{{status: 200, body: ocsErrBody(http.StatusNotFound, "Room not found")}},
	}
	c := newTestClient(doer)
	sid, err := c.JoinRoom(context.Background(), "tok-test")
	if err == nil {
		t.Fatalf("JoinRoom: got sid=%q err=nil, want error", sid)
	}
	var oe *transport.OCSError
	if !errors.As(err, &oe) {
		t.Fatalf("errors.As(*OCSError): got %T (%v)", err, err)
	}
	if oe.Code != http.StatusNotFound {
		t.Errorf("OCSError.Code: got %d, want 404", oe.Code)
	}
}
