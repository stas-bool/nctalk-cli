package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// containsAll проверяет, что out содержит все подстроки want.
func containsAll(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("вывод не содержит %q:\n%s", s, out)
		}
	}
}

// ----- Rooms -----

func TestRoomsTable(t *testing.T) {
	rooms := []client.Room{
		{
			Type:           client.RoomType(1),
			Token:          "tokABC",
			DisplayName:    "Комната №1",
			UnreadMessages: 3,
			ActorType:      "users",
			ActorId:        "alice",
			LastMessage: &client.Message{
				Id:               42,
				ActorDisplayName: "Алиса",
				Message:          "Привет, мир",
				Timestamp:        1700000000,
			},
		},
	}
	var buf bytes.Buffer
	if err := RoomsTable(&buf, rooms); err != nil {
		t.Fatalf("RoomsTable: %v", err)
	}
	containsAll(t, buf.String(),
		"ТИП", "ЧАТ", "TOKEN", "НЕПРОЧИТАНО", "АКТЁР", "ПОСЛЕДНЕЕ",
		"1", "Комната №1", "tokABC", "3", "alice", "Алиса", "Привет, мир")
}

func TestRoomsTableNoLastMessage(t *testing.T) {
	// Комната без LastMessage — превью должно быть "—".
	rooms := []client.Room{
		{Type: 2, Token: "t", DisplayName: "Пустая", UnreadMessages: 0, ActorId: "bob"},
	}
	var buf bytes.Buffer
	if err := RoomsTable(&buf, rooms); err != nil {
		t.Fatalf("RoomsTable: %v", err)
	}
	containsAll(t, buf.String(), "Пустая", "bob", "—")
}

func TestRoomsJSON(t *testing.T) {
	rooms := []client.Room{
		{Type: 1, Token: "tok1", DisplayName: "Чат", UnreadMessages: 5,
			ActorType: "users", ActorId: "alice"},
	}
	var buf bytes.Buffer
	if err := RoomsJSON(&buf, rooms); err != nil {
		t.Fatalf("RoomsJSON: %v", err)
	}
	// Обратный парсинг.
	var got []client.Room
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Token != "tok1" || got[0].ActorId != "alice" ||
		got[0].DisplayName != "Чат" || got[0].UnreadMessages != 5 || got[0].Type != 1 {
		t.Fatalf("неожиданный результат Unmarshal: %+v", got)
	}
	// actorId обязан присутствовать в JSON как поле.
	if !strings.Contains(buf.String(), `"actorId"`) {
		t.Errorf("JSON не содержит поле actorId:\n%s", buf.String())
	}
}

// ----- Messages -----

func TestMessagesTable(t *testing.T) {
	msgs := []client.Message{
		{
			Id:               10,
			ActorType:        "users",
			ActorId:          "alice",
			ActorDisplayName: "Алиса",
			Message:          "Текст сообщения",
			Timestamp:        1700000000,
			Reactions:        map[string]int{"👍": 2, "✅": 1},
		},
	}
	var buf bytes.Buffer
	if err := MessagesTable(&buf, msgs); err != nil {
		t.Fatalf("MessagesTable: %v", err)
	}
	out := buf.String()
	containsAll(t, out, "[10]", "Алиса", "Текст сообщения", "👍×2", "✅×1")
	// Порядок эмодзи детерминированный (сортировка): ✅ قبل 👍 по байтам.
	// Проверим только вхождение обоих суффиксов.
}

func TestMessagesTableNoReactions(t *testing.T) {
	msgs := []client.Message{
		{Id: 1, ActorDisplayName: "Bob", Message: "hi", Timestamp: 1700000000},
	}
	var buf bytes.Buffer
	if err := MessagesTable(&buf, msgs); err != nil {
		t.Fatalf("MessagesTable: %v", err)
	}
	if strings.Contains(buf.String(), "×") {
		t.Errorf("сообщение без реакций не должно содержать ×:\n%s", buf.String())
	}
}

func TestMessagesJSON(t *testing.T) {
	msgs := []client.Message{
		{Id: 7, ActorId: "alice", ActorDisplayName: "Алиса", Message: "привет",
			Timestamp: 1700000000, Reactions: map[string]int{"👍": 2}},
	}
	var buf bytes.Buffer
	if err := MessagesJSON(&buf, msgs); err != nil {
		t.Fatalf("MessagesJSON: %v", err)
	}
	var got []client.Message
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Id != 7 || got[0].ActorId != "alice" ||
		got[0].Message != "привет" || got[0].Reactions["👍"] != 2 {
		t.Fatalf("неожиданный результат Unmarshal: %+v", got)
	}
	if !strings.Contains(buf.String(), `"actorId"`) {
		t.Errorf("JSON не содержит поле actorId:\n%s", buf.String())
	}
}

// ----- ConversationResults -----

func TestConversationResultsTable(t *testing.T) {
	rs := []client.ConversationResult{
		{Title: "Найденный чат", Token: "convTok"},
	}
	var buf bytes.Buffer
	if err := ConversationResultsTable(&buf, rs); err != nil {
		t.Fatalf("ConversationResultsTable: %v", err)
	}
	containsAll(t, buf.String(), "Найденный чат", "convTok", "TOKEN")
}

func TestConversationResultsJSON(t *testing.T) {
	rs := []client.ConversationResult{
		{Title: "Чат", Token: "tk"},
	}
	var buf bytes.Buffer
	if err := ConversationResultsJSON(&buf, rs); err != nil {
		t.Fatalf("ConversationResultsJSON: %v", err)
	}
	var got []client.ConversationResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Title != "Чат" || got[0].Token != "tk" {
		t.Fatalf("неожиданный результат Unmarshal: %+v", got)
	}
}

// ----- MessageResults -----

func TestMessageResultsTable(t *testing.T) {
	rs := []client.MessageResult{
		{Title: "Алиса", Subline: "найденный текст", ResourceUrl: "https://x/call/tok#message_99"},
	}
	rs[0].Attributes.Conversation = "tok"
	rs[0].Attributes.MessageId = 99
	rs[0].Attributes.ActorType = "users"
	rs[0].Attributes.ActorId = "alice"
	rs[0].Attributes.Timestamp = 1700000000
	var buf bytes.Buffer
	if err := MessageResultsTable(&buf, rs); err != nil {
		t.Fatalf("MessageResultsTable: %v", err)
	}
	containsAll(t, buf.String(), "Алиса", "найденный текст", "tok#99")
}

func TestMessageResultsJSON(t *testing.T) {
	rs := []client.MessageResult{
		{Title: "Алиса", Subline: "текст", ResourceUrl: "u"},
	}
	rs[0].Attributes.Conversation = "tok"
	rs[0].Attributes.MessageId = 99
	rs[0].Attributes.ActorId = "alice"
	rs[0].Attributes.Timestamp = 1700000000
	var buf bytes.Buffer
	if err := MessageResultsJSON(&buf, rs); err != nil {
		t.Fatalf("MessageResultsJSON: %v", err)
	}
	var got []client.MessageResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || got[0].Title != "Алиса" ||
		got[0].Attributes.Conversation != "tok" ||
		got[0].Attributes.MessageId != 99 ||
		got[0].Attributes.ActorId != "alice" {
		t.Fatalf("неожиданный результат Unmarshal: %+v", got)
	}
}

// ----- Reactions -----

func TestReactionsTextEmpty(t *testing.T) {
	// Пустая map → "реакций нет" (спека §6).
	var buf bytes.Buffer
	if err := ReactionsText(&buf, map[string][]client.ReactionActor{}); err != nil {
		t.Fatalf("ReactionsText: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != "реакций нет" {
		t.Fatalf("для пустой map ожидалась строка %q, получено %q", "реакций нет", got)
	}
}

func TestReactionsTextNonEmpty(t *testing.T) {
	rs := map[string][]client.ReactionActor{
		"👍": {
			{ActorType: "users", ActorId: "alice", ActorDisplayName: "Алиса"},
			{ActorType: "users", ActorId: "bob", ActorDisplayName: "Боб"},
		},
		"🎉": {
			{ActorType: "users", ActorId: "carol", ActorDisplayName: "Кэрол"},
		},
	}
	var buf bytes.Buffer
	if err := ReactionsText(&buf, rs); err != nil {
		t.Fatalf("ReactionsText: %v", err)
	}
	out := buf.String()
	containsAll(t, out, "👍", "🎉", "Алиса", "Боб", "Кэрол", "→", "[", "]")
}

func TestReactionsJSON(t *testing.T) {
	rs := map[string][]client.ReactionActor{
		"👍": {{ActorType: "users", ActorId: "alice", ActorDisplayName: "Алиса"}},
	}
	var buf bytes.Buffer
	if err := ReactionsJSON(&buf, rs); err != nil {
		t.Fatalf("ReactionsJSON: %v", err)
	}
	var got map[string][]client.ReactionActor
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 || len(got["👍"]) != 1 || got["👍"][0].ActorId != "alice" {
		t.Fatalf("неожиданный результат Unmarshal: %+v", got)
	}
	if !strings.Contains(buf.String(), `"actorId"`) {
		t.Errorf("JSON не содержит поле actorId:\n%s", buf.String())
	}
}

// ----- NewMessageID -----

func TestNewMessageID(t *testing.T) {
	var buf bytes.Buffer
	if err := NewMessageID(&buf, 42); err != nil {
		t.Fatalf("NewMessageID: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != "42" {
		t.Fatalf("ожидалось %q, получено %q", "42", got)
	}
}

func TestNewMessageIDJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := NewMessageIDJSON(&buf, 42); err != nil {
		t.Fatalf("NewMessageIDJSON: %v", err)
	}
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if got.Id != 42 {
		t.Fatalf("ожидался id=42, получено %d", got.Id)
	}
	if !strings.Contains(buf.String(), `"id"`) {
		t.Errorf("JSON не содержит ключ id:\n%s", buf.String())
	}
}

// ----- Candidates -----

func TestCandidates(t *testing.T) {
	rooms := []client.Room{
		{Type: 1, Token: "tokA", DisplayName: "Первый"},
		{Type: 2, Token: "tokB", DisplayName: "Второй"},
	}
	var buf bytes.Buffer
	if err := Candidates(&buf, rooms); err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	containsAll(t, buf.String(), "1", "Первый", "tokA", "2", "Второй", "tokB")
}
