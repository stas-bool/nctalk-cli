package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// profileNow — фиксированное «сейчас» для расчёта местного времени.
var profileNow = time.Date(2026, 10, 22, 8, 41, 0, 0, time.UTC)

// TestProfileText_Golden — порядок строк, пропуск пустых (Заголовок,
// Местоимения, О себе), выравнивание tabwriter (считает руны — кириллица
// выравнивается), пояс со смещением и местным временем, контакты.
func TestProfileText_Golden(t *testing.T) {
	p := &client.Profile{
		UserId:         "ivan.p",
		DisplayName:    "Иван Петров",
		Role:           "Генеральный директор",
		Organisation:   "Example LLC",
		Address:        "Калининград",
		Timezone:       "Europe/Kaliningrad",
		TimezoneOffset: 7200,
		Contacts: []client.ProfileContact{
			{Type: "email", Value: "ivan.p@example.org"},
			{Type: "phone", Value: "+70000000000"},
		},
	}
	var buf bytes.Buffer
	if err := ProfileText(&buf, p, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	want := "" +
		"Имя:           Иван Петров\n" +
		"ID:            ivan.p\n" +
		"Должность:     Генеральный директор\n" +
		"Организация:   Example LLC\n" +
		"Город:         Калининград\n" +
		"Часовой пояс:  Europe/Kaliningrad (UTC+2), сейчас 2026-10-22 10:41\n" +
		"email:         ivan.p@example.org\n" +
		"phone:         +70000000000\n"
	if got := buf.String(); got != want {
		t.Errorf("ProfileText:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestProfileText_AllRowsAndBiography — все строки в порядке спеки,
// биография последним блоком как есть (\t и \n сохраняются, не ломают
// выравнивание блока), без завершающего \n — дописывается один.
func TestProfileText_AllRowsAndBiography(t *testing.T) {
	p := &client.Profile{
		UserId:         "ivan.p",
		DisplayName:    "Иван Петров",
		Role:           "Генеральный директор",
		Headline:       "COO",
		Organisation:   "Example LLC",
		Address:        "Калининград",
		Pronouns:       "он/его",
		Biography:      "Строка 1\n\tСтрока 2",
		Timezone:       "Europe/Kaliningrad",
		TimezoneOffset: 7200,
		Contacts:       []client.ProfileContact{{Type: "website", Value: "https://example.org"}},
	}
	var buf bytes.Buffer
	if err := ProfileText(&buf, p, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	want := "" +
		"Имя:           Иван Петров\n" +
		"ID:            ivan.p\n" +
		"Должность:     Генеральный директор\n" +
		"Заголовок:     COO\n" +
		"Организация:   Example LLC\n" +
		"Город:         Калининград\n" +
		"Местоимения:   он/его\n" +
		"Часовой пояс:  Europe/Kaliningrad (UTC+2), сейчас 2026-10-22 10:41\n" +
		"website:       https://example.org\n" +
		"О себе:\n" +
		"Строка 1\n\tСтрока 2\n"
	if got := buf.String(); got != want {
		t.Errorf("ProfileText:\n got:\n%q\nwant:\n%q", got, want)
	}
}

// TestProfileText_BiographyTrailingNewline — биография с \n в конце: второй
// \n не дописывается; биография из одних пробельных — блок не печатается.
func TestProfileText_BiographyTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	if err := ProfileText(&buf, &client.Profile{UserId: "u", Biography: "Текст\n"}, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	if got, want := buf.String(), "ID:  u\nО себе:\nТекст\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	buf.Reset()
	if err := ProfileText(&buf, &client.Profile{UserId: "u", Biography: " \n\t "}, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	if got, want := buf.String(), "ID:  u\n"; got != want {
		t.Errorf("пробельная биография: got %q, want %q", got, want)
	}
}

// TestProfileText_CollapseWhitespace — \n/\t/повторы в однострочном поле и в
// контакте схлопываются в один пробел; ставшее пустым значение или метка
// контакта — строки нет (защита рендера; клиент такие контакты и так
// отбрасывает).
func TestProfileText_CollapseWhitespace(t *testing.T) {
	p := &client.Profile{
		UserId:   "u",
		Address:  "Калининград,\n\tул.  Мира",
		Pronouns: " \n\t ",
		Contacts: []client.ProfileContact{
			{Type: "email", Value: "a@b\n.c"},
			{Type: "", Value: "https://x.example.org"},
			{Type: " \t", Value: "https://y.example.org"},
			{Type: "phone", Value: " \n "},
		},
	}
	var buf bytes.Buffer
	if err := ProfileText(&buf, p, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	want := "" +
		"ID:     u\n" +
		"Город:  Калининград, ул. Мира\n" +
		"email:  a@b .c\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestProfileText_NoTimezone — пустой timezone → строки пояса нет (даже при
// ненулевом смещении).
func TestProfileText_NoTimezone(t *testing.T) {
	var buf bytes.Buffer
	if err := ProfileText(&buf, &client.Profile{UserId: "u", TimezoneOffset: 7200}, profileNow); err != nil {
		t.Fatalf("ProfileText: %v", err)
	}
	if strings.Contains(buf.String(), "Часовой пояс") {
		t.Errorf("строки пояса быть не должно: %q", buf.String())
	}
}

// TestProfileTimezone — смещение и местное время по timezoneOffset (без
// tzdata): 0, целые часы ±, получасовые ±; переход через полночь.
func TestProfileTimezone(t *testing.T) {
	cases := []struct {
		offset int
		want   string
	}{
		{0, "Etc/X (UTC), сейчас 2026-10-22 08:41"},
		{7200, "Etc/X (UTC+2), сейчас 2026-10-22 10:41"},
		{-10800, "Etc/X (UTC-3), сейчас 2026-10-22 05:41"},
		{19800, "Etc/X (UTC+5:30), сейчас 2026-10-22 14:11"},
		{-12600, "Etc/X (UTC-3:30), сейчас 2026-10-22 05:11"},
		{-36000, "Etc/X (UTC-10), сейчас 2026-10-21 22:41"},
	}
	for _, tc := range cases {
		got := profileTimezone(&client.Profile{Timezone: "Etc/X", TimezoneOffset: tc.offset}, profileNow)
		if got != tc.want {
			t.Errorf("offset %d: got %q, want %q", tc.offset, got, tc.want)
		}
	}
}

// TestProfileJSON — каноническая модель: camelCase-имена, пустые поля "",
// contacts — [] (не null) даже при nil в модели.
func TestProfileJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := ProfileJSON(&buf, &client.Profile{UserId: "ivan.p", Timezone: "UTC"}); err != nil {
		t.Fatalf("ProfileJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"userId", "displayName", "role", "headline", "organisation",
		"address", "pronouns", "biography", "timezone", "timezoneOffset", "contacts"} {
		if _, ok := got[k]; !ok {
			t.Errorf("нет ключа %q: %s", k, buf.String())
		}
	}
	if got["displayName"] != "" || got["biography"] != "" {
		t.Errorf("пустые поля: want \"\", got displayName=%v biography=%v", got["displayName"], got["biography"])
	}
	if c, ok := got["contacts"].([]any); !ok || len(c) != 0 {
		t.Errorf("contacts: got %v, want [] (не null)", got["contacts"])
	}
	if !strings.Contains(buf.String(), `"contacts": []`) {
		t.Errorf("contacts в тексте: want `\"contacts\": []`, got %s", buf.String())
	}
}
