package client

// files_helpers_test.go — таблицы чистых хелперов отправки файла (спека-дельта
// 2026-10-09 §1, §2.4, §3.5, §3.6, §4.2): кандидаты имени, сегменты пути,
// referenceId, talkMetaData, папка вложений, срок загрузки.

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestNameCandidate — правило веб-клиента (§1, модель) + решение плана для
// дотфайлов: расширение с позиции 0 расширением не считается.
func TestNameCandidate(t *testing.T) {
	cases := []struct {
		name string
		i    int
		want string
	}{
		{"report.pdf", 0, "report.pdf"},
		{"report.pdf", 1, "report (2).pdf"},
		{"report.pdf", 2, "report (3).pdf"},
		{"x (3).pdf", 1, "x (4).pdf"},
		{"x (0).pdf", 1, "x (1).pdf"},
		{"a.tar.gz", 1, "a.tar (2).gz"},
		{"Makefile", 1, "Makefile (2)"},
		{"Makefile (7)", 2, "Makefile (9)"},
		{"REPORT.PDF", 1, "REPORT (2).PDF"},
		{"отчёт.pdf", 1, "отчёт (2).pdf"},
		{"файл.тхт", 1, "файл.тхт (2)"}, // кириллица в «расширении» — не [0-9a-z]
		{".bashrc", 1, ".bashrc (2)"},
		{".bashrc (2)", 1, ".bashrc (3)"},
		// суффикс — только « (N)» с пробелом; не влезающий в int — часть base
		{"x(2).pdf", 1, "x(2) (2).pdf"},
		{"n (99999999999999999999).txt", 1, "n (99999999999999999999) (2).txt"},
	}
	for _, tc := range cases {
		if got := nameCandidate(tc.name, tc.i); got != tc.want {
			t.Errorf("nameCandidate(%q, %d): got %q, want %q", tc.name, tc.i, got, tc.want)
		}
	}
}

// TestIsPlainSegment — один сегмент пути: пустое, «.», «..» и имена с «/»
// отвергаются (path.Join в транспорте схлопнул бы «.»/«..»).
func TestIsPlainSegment(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"report.pdf", true},
		{"отчёт #1?%.pdf", true},
		{"...", true},
		{".bashrc", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"/", false},
	}
	for _, tc := range cases {
		if got := isPlainSegment(tc.s); got != tc.want {
			t.Errorf("isPlainSegment(%q): got %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestNormalizeReferenceId — как у сервера: первые 64 байта, затем trim по
// набору PHP trim().
func TestNormalizeReferenceId(t *testing.T) {
	long := strings.Repeat("ab", 40) // 80 байт
	cases := []struct {
		in, want string
	}{
		{"abc", "abc"},
		{"  abc \t\n", "abc"},
		{"\x00\x0Babc\r", "abc"},
		{long, long[:64]},
		{"  " + long, ("  " + long)[2:64]}, // сначала срез 64 байт, потом trim
		{strings.Repeat(" ", 70), ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeReferenceId(tc.in); got != tc.want {
			t.Errorf("normalizeReferenceId(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGenerateReferenceId — 64 hex-символа (32 байта crypto/rand), два вызова
// различаются.
func TestGenerateReferenceId(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{64}$`)
	a, err := generateReferenceId()
	if err != nil {
		t.Fatalf("generateReferenceId: %v", err)
	}
	b, err := generateReferenceId()
	if err != nil {
		t.Fatalf("generateReferenceId: %v", err)
	}
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Errorf("формат: got %q / %q, want 64 hex", a, b)
	}
	if a == b {
		t.Errorf("два вызова дали одинаковый id %q", a)
	}
	if normalizeReferenceId(a) != a {
		t.Errorf("сгенерированный id меняется нормализацией: %q", a)
	}
}

// TestEncodeTalkMetaData — JSON-строка только с заданными полями; пустой
// набор → "{}" (§3.5).
func TestEncodeTalkMetaData(t *testing.T) {
	cases := []struct {
		in   talkMetaData
		want string
	}{
		{talkMetaData{}, `{}`},
		{talkMetaData{Caption: "Отчёт"}, `{"caption":"Отчёт"}`},
		{talkMetaData{ReplyTo: 7}, `{"replyTo":7}`},
		{talkMetaData{ThreadId: 1234, Silent: true}, `{"threadId":1234,"silent":true}`},
		{talkMetaData{Caption: "a\"b", ThreadTitle: "Тема"}, `{"caption":"a\"b","threadTitle":"Тема"}`},
	}
	for _, tc := range cases {
		got, err := encodeTalkMetaData(tc.in)
		if err != nil {
			t.Fatalf("encodeTalkMetaData(%+v): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("encodeTalkMetaData(%+v): got %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeAttachmentFolder — пусто → /Talk; абсолютный путь без
// завершающего «/» и двойных слэшей; корень — «/».
func TestNormalizeAttachmentFolder(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/Talk"},
		{"/Talk", "/Talk"},
		{"Talk", "/Talk"},
		{"/Docs/Chat/", "/Docs/Chat"},
		{"//a//b", "/a/b"},
		{"/", "/"},
	}
	for _, tc := range cases {
		if got := normalizeAttachmentFolder(tc.in); got != tc.want {
			t.Errorf("normalizeAttachmentFolder(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUploadTimeout — base + 1 с на каждые начатые 64 КиБ (§4.2); base <= 0 —
// без срока; переполнение — потолок.
func TestUploadTimeout(t *testing.T) {
	const s = time.Second
	cases := []struct {
		base time.Duration
		size int64
		want time.Duration
	}{
		{0, 1 << 20, 0},
		{-1, 1 << 20, 0},
		{30 * s, 0, 30 * s},
		{30 * s, 1, 31 * s},
		{30 * s, 64 << 10, 31 * s},
		{30 * s, 64<<10 + 1, 32 * s},
		{30 * s, 52428800, 830 * s}, // 50 МиБ = 800 кусков
		{30 * s, math.MaxInt64, time.Duration(math.MaxInt64)},
	}
	for _, tc := range cases {
		if got := uploadTimeout(tc.base, tc.size); got != tc.want {
			t.Errorf("uploadTimeout(%v, %d): got %v, want %v", tc.base, tc.size, got, tc.want)
		}
	}
}
