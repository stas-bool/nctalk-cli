package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestMsgParams_UnmarshalJSON_Array — формат v4/room для messageType=comment:
// массив (чаще пустой []). После Unmarshal MsgParams должен быть пустым
// (параметров нет), ошибка не возвращается. Элементы массива нам не нужны — для
// comment сообщение уже человекочитаемое (плейсхолдеры подставлены сервером).
func TestMsgParams_UnmarshalJSON_Array(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"пустой массив (типичный comment в v4/room)", `[]`},
		{"массив с элементами (редкий случай) — элементы отбрасываются", `[{"type":"user","id":"x","name":"Y"}]`},
		{"массив с пробелами", ` [ ] `},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p MsgParams
			if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
				t.Fatalf("Unmarshal(%s): неожиданная ошибка: %v", tc.raw, err)
			}
			if len(p) != 0 {
				t.Errorf("Unmarshal(%s): got %d элементов, want 0 (массив → пустая карта)", tc.raw, len(p))
			}
		})
	}
}

// TestMsgParams_UnmarshalJSON_Object — канонический объектный формат
// (chat-API всегда; v4/room для system). Ключ = имя плейсхолдера, значение —
// MsgParam. Проверяем что подстановка плейсхолдеров будет работать: ключи и
// поля Type/Id/Name/Link сохраняются.
func TestMsgParams_UnmarshalJSON_Object(t *testing.T) {
	raw := `{
	  "actor": {"type":"user","id":"alice","name":"Alice","link":"https://nc.example.com/u/alice"},
	  "file":  {"type":"file","id":"5001","name":"report.pdf","path":"/files/report.pdf"},
	  "mention-user2": {"type":"user","id":"user2","name":"Борис"}
	}`
	var p MsgParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, want := len(p), 3; got != want {
		t.Fatalf("len(p) = %d, want %d", got, want)
	}
	if mp := p["actor"]; mp.Type != "user" || mp.Id != "alice" || mp.Name != "Alice" {
		t.Errorf("p[actor]: got %+v, want {Type:user Id:alice Name:Alice}", mp)
	}
	if mp := p["file"]; mp.Type != "file" || mp.Name != "report.pdf" {
		t.Errorf("p[file]: got %+v, want {Type:file Name:report.pdf}", mp)
	}
	if mp := p["mention-user2"]; mp.Name != "Борис" {
		t.Errorf("p[mention-user2].Name: got %q, want %q", mp.Name, "Борис")
	}
}

// TestMsgParams_UnmarshalJSON_Null — JSON null → nil-карта без ошибки. Это
// страховка: json-кодировщик Nextcloud иногда отдаёт null вместо пустого
// объекта/массива. (Пустой ввод через json.Unmarshal не доходит до
// UnmarshalJSON — пакет json отвергает его на верхнем уровне с «unexpected
// end of JSON input», что корректно.)
func TestMsgParams_UnmarshalJSON_Null(t *testing.T) {
	var p MsgParams
	if err := json.Unmarshal([]byte("null"), &p); err != nil {
		t.Fatalf("Unmarshal(null): неожиданная ошибка: %v", err)
	}
	if p != nil {
		t.Errorf("Unmarshal(null): got %#v, want nil", p)
	}
}

// TestMsgParams_UnmarshalJSON_Invalid — синтаксически битый JSON → ошибка
// декодирования (не замалчивается). Защита от тихого проглатывания реальных
// поломок ответа сервера.
func TestMsgParams_UnmarshalJSON_Invalid(t *testing.T) {
	raw := `{not json`
	var p MsgParams
	err := json.Unmarshal([]byte(raw), &p)
	if err == nil {
		t.Fatal("err = nil, want JSON-decode error")
	}
	// Стандартный json.SyntaxError или подобное — без паники.
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		// Допускаем также *json.UnmarshalTypeError — главное, что ошибка есть.
		var ute *json.UnmarshalTypeError
		if !errors.As(err, &ute) {
			t.Logf("тип ошибки не SyntaxError/UnmarshalTypeError: %T (%v) — считаем допустимым", err, err)
		}
	}
}

// TestMsgParams_UnmarshalJSON_EmptyObject — пустой объект {} → пустая (не nil)
// карта. Это нормальный сценарий «messageType=comment, параметров нет» для
// chat-API, где формат всегда объектный.
func TestMsgParams_UnmarshalJSON_EmptyObject(t *testing.T) {
	raw := `{}`
	var p MsgParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	if got, want := len(p), 0; got != want {
		t.Errorf("len(p) = %d, want %d", got, want)
	}
}

// TestMessage_UnmarshalJSON_BothFormats — end-to-end проверка на уровне всего
// Message: объект с lastMessage/comment + массивом messageParameters должен
// распаковаться без ошибки и с пустой картой параметров; объектный вариант — с
// заполненной. Воспроизводит реальный сценарий, который пропустили старые тесты
// (комната с comment-сообщением из v4/room).
func TestMessage_UnmarshalJSON_BothFormats(t *testing.T) {
	t.Run("comment с массивом (v4/room)", func(t *testing.T) {
		raw := `{
		  "id": 42,
		  "actorType": "users",
		  "actorId": "alice",
		  "actorDisplayName": "Alice",
		  "messageType": "comment",
		  "message": "привет, скинул report.pdf",
		  "messageParameters": [],
		  "reactions": {},
		  "timestamp": 1718000000,
		  "token": "tok-x"
		}`
		var m Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if m.Message != "привет, скинул report.pdf" {
			t.Errorf("Message: got %q, want %q", m.Message, "привет, скинул report.pdf")
		}
		if len(m.MessageParameters) != 0 {
			t.Errorf("MessageParameters: got %d элементов, want 0 (массив → пусто)", len(m.MessageParameters))
		}
	})
	t.Run("system с объектом (v4/room и chat-API)", func(t *testing.T) {
		raw := `{
		  "id": 43,
		  "actorType": "users",
		  "actorId": "alice",
		  "actorDisplayName": "Alice",
		  "messageType": "system",
		  "systemMessage": "conversation_created",
		  "message": "{actor} создал(а) беседу",
		  "messageParameters": {"actor": {"type":"user","id":"alice","name":"Alice"}},
		  "reactions": {},
		  "timestamp": 1718000100,
		  "token": "tok-x"
		}`
		var m Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if mp := m.MessageParameters["actor"]; mp.Type != "user" || mp.Name != "Alice" {
			t.Errorf("MessageParameters[actor]: got %+v, want {Type:user Name:Alice}", mp)
		}
		if !strings.Contains(m.Message, "{actor}") {
			t.Errorf("плейсхолдер {actor} должен сохраниться для последующей подстановки: %q", m.Message)
		}
	})
}
