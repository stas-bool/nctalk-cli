package render

import (
	"testing"

	"github.com/stas/nctalk/internal/client"
)

// TestSubstituteParams — table-driven проверка подстановки messageParameters
// (спека §6 «Подстановка»). Нераспознанные плейсхолдеры остаются как есть.
func TestSubstituteParams(t *testing.T) {
	// Фикстура параметров: ключ = имя плейсхолдера без скобок.
	params := map[string]client.MsgParam{
		"file":          {Type: "file", Id: "42", Name: "report.pdf"},
		"actor":         {Type: "user", Id: "alice", Name: "Алиса"},
		"mention-user2": {Type: "user", Id: "user2", Name: "Борис"},
		// прочий тип (не file/user) → ветка «остальные»: name подставляется,
		// если непусто.
		"call":  {Type: "call", Id: "c1", Name: "Звонок"},
		"empty": {Type: "call", Id: "c2", Name: ""},
	}

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "file → name файлового параметра",
			msg:  "Поделился файлом {file}",
			want: "Поделился файлом report.pdf",
		},
		{
			name: "actor → name пользовательского параметра",
			msg:  "{actor} зашёл в разговор",
			want: "Алиса зашёл в разговор",
		},
		{
			name: "mention-user2 → name mention-параметра (ключ точно mention-user2)",
			msg:  "Привет, {mention-user2}!",
			want: "Привет, Борис!",
		},
		{
			name: "unknown → плейсхолдер остаётся как есть",
			msg:  "Неизвестный {unknown} тут",
			want: "Неизвестный {unknown} тут",
		},
		{
			name: "несколько плейсхолдеров в одном сообщении",
			msg:  "{actor} упомянул {mention-user2} и скил {file}",
			want: "Алиса упомянул Борис и скил report.pdf",
		},
		{
			name: "прочий тип с name → подставляется (ветка «остальные»)",
			msg:  "Состоялся {call}",
			want: "Состоялся Звонок",
		},
		{
			name: "прочий тип с пустым name → плейсхолдер остаётся",
			msg:  "Пустой {empty} тут",
			want: "Пустой {empty} тут",
		},
		{
			name: "текст без плейсхолдеров не меняется",
			msg:  "просто текст",
			want: "просто текст",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubstituteParams(tt.msg, params)
			if got != tt.want {
				t.Errorf("SubstituteParams(%q):\n got = %q\nwant = %q", tt.msg, got, tt.want)
			}
		})
	}
}

// TestSubstituteParams_NilParams — nil-карта параметров: текст без плейсхолдеров
// возвращается как есть. Это страховка от паники на nil-lookup.
func TestSubstituteParams_NilParams(t *testing.T) {
	got := SubstituteParams("hi {there}", nil)
	if want := "hi {there}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
