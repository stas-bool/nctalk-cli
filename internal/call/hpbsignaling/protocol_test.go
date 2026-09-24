package hpbesignaling

// protocol_test.go — чистый маппинг кадров HPB в signaling.Event (Task 3,
// дельта §2/§3). Фикстуры — живые кадры спайка Task 1 (testdata/hpb/).
// Ожидания сверены с эталонными sessionid фикстур (REDACTED-…), а не с
// доспайковыми шаблонными литералами брифа.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/signaling"
)

// fixturePath — путь к testdata/hpb/ (cwd теста = internal/call/hpbsignaling).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "testdata", "hpb", name)
}

func loadFrame(t *testing.T, name string) *serverFrame {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("фикстура %s: %v", name, err)
	}
	var f serverFrame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("декод фикстуры %s: %v", name, err)
	}
	return &f
}

func TestApplyFrame_Hello_EmitsOwnSession(t *testing.T) {
	st := newRoomState()
	evs := st.applyFrame(loadFrame(t, "hello.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvOwnSession || evs[0].From == "" {
		t.Fatalf("evs = %+v, want один EvOwnSession с sessionid", evs)
	}
}

func TestApplyFrame_JoinLeave_Participants(t *testing.T) {
	st := newRoomState()
	st.applyFrame(loadFrame(t, "hello.json"))

	// join: снапшот с 2 участниками (self опознаётся только по ownSid —
	// фильтрует агент, адаптер доставляет всех).
	evs := st.applyFrame(loadFrame(t, "event_join.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvUsersUpdated || len(evs[0].Users) != 2 {
		t.Fatalf("join: evs = %+v", evs)
	}
	// participants update: те же сессии, но с inCall=3 (мерж, не дубли).
	evs = st.applyFrame(loadFrame(t, "participants_update.json"))
	if len(evs) != 1 || len(evs[0].Users) != 2 {
		t.Fatalf("participants: evs = %+v", evs)
	}
	for _, u := range evs[0].Users {
		if u.InCall != 3 {
			t.Errorf("user %s InCall = %d, want 3 (мерж флагов из update)", u.SessionId, u.InCall)
		}
	}
	// leave: снапшот из 1 (sessionid — эталонный из фикстуры, не шаблонный).
	evs = st.applyFrame(loadFrame(t, "event_leave.json"))
	if len(evs) != 1 || len(evs[0].Users) != 1 || evs[0].Users[0].SessionId != "REDACTED-hpb-sessionid-own-64chars" {
		t.Fatalf("leave: evs = %+v", evs)
	}
}

func TestApplyFrame_Messages(t *testing.T) {
	st := newRoomState()
	evs := st.applyFrame(loadFrame(t, "message_offer.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvOffer || evs[0].From == "" || evs[0].SDP == "" {
		t.Fatalf("offer: evs = %+v", evs)
	}
	evs = st.applyFrame(loadFrame(t, "message_candidate.json"))
	if len(evs) != 1 || evs[0].Kind != signaling.EvCandidate {
		t.Fatalf("candidate: evs = %+v", evs)
	}
	if evs[0].Candidate.Candidate == "" || evs[0].Candidate.SDPMLineIndex == nil {
		t.Fatalf("candidate-поля: %+v", evs[0].Candidate)
	}
}

// TestApplyFrame_CandidateEndOfCandidates — пустой candidate (end-of-candidates
// marker, форма как у OCS: payload.candidate.candidate === "") ДОЛЖЕН
// доставляться как EvCandidate — паритет с OCS-путём (decodeCandidateEvent,
// TestParse_Candidate [2]): peer-слой трактует пустой Candidate.Candidate как
// конец trickle-последовательности. Скип остаётся только для payload, не
// разобравшегося unmarshal'ом.
func TestApplyFrame_CandidateEndOfCandidates(t *testing.T) {
	st := newRoomState()
	raw := `{"type":"message","message":{"sender":{"type":"session","sessionid":"peer-sid"},"data":{"type":"candidate","roomType":"video","payload":{"candidate":{"candidate":""}}}}}`
	var f serverFrame
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("декод: %v", err)
	}
	evs := st.applyFrame(&f)
	if len(evs) != 1 {
		t.Fatalf("len(evs) = %d, want 1 (end-of-candidates — событие)", len(evs))
	}
	if evs[0].Kind != signaling.EvCandidate {
		t.Fatalf("Kind = %v, want EvCandidate", evs[0].Kind)
	}
	if evs[0].From != "peer-sid" {
		t.Fatalf("From = %q, want peer-sid (sender.sessionid)", evs[0].From)
	}
	if evs[0].Candidate.Candidate != "" {
		t.Fatalf("Candidate.Candidate = %q, want пусто (end-of-candidates)", evs[0].Candidate.Candidate)
	}
	// sdpMLineIndex/sdpMid в маркере отсутствуют — доставлены как nil.
	if evs[0].Candidate.SDPMLineIndex != nil || evs[0].Candidate.SDPMid != nil {
		t.Fatalf("SDPMLineIndex/SDPMid = %v/%v, want nil/nil (отсутствуют — как задекодировалось)",
			evs[0].Candidate.SDPMLineIndex, evs[0].Candidate.SDPMid)
	}

	// Битый payload (unmarshal-ошибка) — по-прежнему 0 событий (без шума).
	var bad serverFrame
	if err := json.Unmarshal([]byte(`{"type":"message","message":{"sender":{"sessionid":"p"},"data":{"type":"candidate","payload":"not-an-object"}}}`), &bad); err != nil {
		t.Fatalf("декод: %v", err)
	}
	if evs := st.applyFrame(&bad); len(evs) != 0 {
		t.Fatalf("битый candidate-payload → %+v, want 0 событий", evs)
	}
}

// TestApplyFrame_Welcome_DecodeAndNoise — welcome-кадр по ЖИВОЙ фикстуре
// Task 1: без id-поля, version — ВЕРСИЯ СЕРВЕРА (не протокола), features —
// реальный список (hello-v2 есть). Событий не несёт, но декодируется в
// serverFrame.Welcome — по features выбирается версия hello (Task 4).
func TestApplyFrame_Welcome_DecodeAndNoise(t *testing.T) {
	f := loadFrame(t, "welcome.json")
	if f.ID != "" {
		t.Errorf("welcome.id = %q, want пусто (живой кадр id не несёт)", f.ID)
	}
	if f.Welcome == nil {
		t.Fatal("welcome не задекодирован (serverFrame.Welcome)")
	}
	if f.Welcome.Version != "2.1.1" {
		t.Errorf("welcome.version = %q, want 2.1.1 (версия сервера)", f.Welcome.Version)
	}
	if !wantHelloV2(f.Welcome.Features, "authsig-example") {
		t.Errorf("features %+v без hello-v2 — не с чем выбирать версию hello", f.Welcome.Features)
	}
	if evs := newRoomState().applyFrame(f); len(evs) != 0 {
		t.Errorf("welcome → %+v, want 0 событий (шум)", evs)
	}
}

func TestApplyFrame_NoiseSkipped(t *testing.T) {
	st := newRoomState()
	for _, raw := range []string{
		`{"type":"welcome","welcome":{"version":"1.0"}}`,
		`{"id":"r","type":"room","room":{"roomid":"tok-test"}}`,
		`{"type":"event","event":{"target":"roomlist","type":"update"}}`,
		`{"type":"event","event":{"target":"room","type":"join","join":[]}}`,
		`{"type":"event","event":{"target":"participants","type":"update","update":{"roomid":"x","users":[]}}}`,
		`{"type":"message","message":{"sender":{"sessionid":"p"},"data":{"type":"control","payload":{}}}}`,
		`{"type":"dialout","dialout":{}}`,
		`{"type":"message","message":{"sender":{"sessionid":"p"},"data":{"type":"offer","payload":"not-an-object"}}}`,
	} {
		var f serverFrame
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("декод %s: %v", raw, err)
		}
		if evs := st.applyFrame(&f); len(evs) != 0 {
			t.Errorf("%s → %+v, want 0 событий (шум/пустое скипается)", raw, evs)
		}
	}
}

func TestInCallFlags_Tolerant(t *testing.T) {
	var u eventUser
	if err := json.Unmarshal([]byte(`{"sessionId":"s","inCall":3}`), &u); err != nil || int(u.InCall) != 3 {
		t.Fatalf("int inCall: %+v err=%v", u, err)
	}
	if err := json.Unmarshal([]byte(`{"sessionId":"s","inCall":true}`), &u); err != nil || int(u.InCall) != 1 {
		t.Fatalf("bool inCall: %+v err=%v (старые деплои шлют bool)", u, err)
	}
}

// TestNewHelloFrame_BothVersions — канон hello, подтверждённый живым HPB
// (спайк Task 1): auth.type:"ticket" сервером отвергнут (invalid_format);
// реальная форма — auth:{url: OCS signaling-backend, params: форма версии}.
func TestNewHelloFrame_BothVersions(t *testing.T) {
	const backend = "https://nc.example.org" + backendPath
	b, err := json.Marshal(newHelloFrame("alice", "ticket-example", backend))
	if err != nil {
		t.Fatal(err)
	}
	wantV1 := `{"id":"h","type":"hello","hello":{"version":"1.0","auth":{"url":"https://nc.example.org/ocs/v2.php/apps/spreed/api/v3/signaling/backend","params":{"userid":"alice","ticket":"ticket-example"}}}}`
	if string(b) != wantV1 {
		t.Fatalf("v1 = %s, want %s", b, wantV1)
	}
	b, err = json.Marshal(newHelloFrameV2("authsig-example", backend))
	if err != nil {
		t.Fatal(err)
	}
	wantV2 := `{"id":"h","type":"hello","hello":{"version":"2.0","auth":{"url":"https://nc.example.org/ocs/v2.php/apps/spreed/api/v3/signaling/backend","params":{"token":"authsig-example"}}}}`
	if string(b) != wantV2 {
		t.Fatalf("v2 = %s, want %s", b, wantV2)
	}
}

// TestWantHelloV2 — выбор версии hello (канон JS-клиента spreed, путь принят
// живым HPB Task 1): v2 только при feature hello-v2 И доступном token.
func TestWantHelloV2(t *testing.T) {
	live := loadFrame(t, "welcome.json").Welcome.Features // живой список фикстуры
	cases := []struct {
		name     string
		features []string
		token    string
		want     bool
	}{
		{"живой welcome + token", live, "authsig-example", true},
		{"живой welcome без token", live, "", false},
		{"feature нет", []string{"mcu", "incall-all"}, "authsig-example", false},
		{"features пусты", nil, "authsig-example", false},
	}
	for _, c := range cases {
		if got := wantHelloV2(c.features, c.token); got != c.want {
			t.Errorf("%s: wantHelloV2 = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNewMessageFrame_AnyType(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"name": "audio"})
	f := newMessageFrame(signaling.Message{Type: "unmute", To: "peer-1", Payload: payload})
	b, _ := json.Marshal(f)
	want := `{"type":"message","message":{"recipient":{"type":"session","sessionid":"peer-1"},` +
		`"data":{"type":"unmute","to":"peer-1","roomType":"video","payload":{"name":"audio"}}}}`
	if string(b) != want {
		t.Fatalf("кадр = %s, want %s", b, want)
	}
}

func TestFrameErrAction(t *testing.T) {
	if frameErrAction("no_such_room") != frameErrFatal2 {
		t.Error("no_such_room → exit 2")
	}
	for _, c := range []string{"invalid_ticket", "token_expired"} {
		if frameErrAction(c) != frameErrRefetchTicket {
			t.Errorf("%s → refetch ticket", c)
		}
	}
	if frameErrAction("processing_failed") != frameErrRetry {
		t.Error("прочее → retry")
	}
}

func TestNextBackoffAndWSURL(t *testing.T) {
	if got := nextBackoff(0, time.Second, 30*time.Second); got != time.Second {
		t.Errorf("nextBackoff(0) = %v", got)
	}
	if got := nextBackoff(16*time.Second, time.Second, 30*time.Second); got != 30*time.Second {
		t.Errorf("nextBackoff cap = %v", got)
	}
	// /spreed — путь standalone-сервера (корень отдаёт 404; живой HPB, спайк
	// Task 1). РОВНО ОДИН /spreed: суффиксированный URL не дублируется,
	// хвостовой / срезается.
	for in, want := range map[string]string{
		"https://h/sig/":  "wss://h/sig/spreed",
		"http://h/sig/":   "ws://h/sig/spreed",
		"wss://h/sig/":    "wss://h/sig/spreed",
		"https://h":       "wss://h/spreed",
		"wss://h/spreed":  "wss://h/spreed",
		"wss://h/spreed/": "wss://h/spreed",
	} {
		if got := normalizeWSURL(in); got != want {
			t.Errorf("normalizeWSURL(%q) = %q, want %q", in, got, want)
		}
	}
}
