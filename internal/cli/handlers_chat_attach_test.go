package cli

// handlers_chat_attach_test.go — `chat send --attach` (спека-дельта
// 2026-10-09 §2, §6, §7): формы флага, guard-ы до сети, проверки файла,
// подпись (необязательная, правило терминала), прокидка опций, вывод и
// exit-коды. Шпион — chatSpyClient (handlers_chat_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/client"
)

// attachFile создаёт временный файл name с содержимым content, возвращает путь.
func attachFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestChatSendAttach_Forms — `--attach <путь>` и `--attach=<путь>`: SendFile
// получает token, базовое имя, размер и содержимое; SendMessage не зовётся.
func TestChatSendAttach_Forms(t *testing.T) {
	p := attachFile(t, "report.pdf", "PDF-содержимое")
	for _, args := range [][]string{
		{"TOK", "--attach", p},
		{"TOK", "--attach=" + p},
	} {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 901, Path: "/Talk/report.pdf"}}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		ee := chatSendHandler(context.Background(), deps, args, false)
		if ee.Code != ExitOK {
			t.Fatalf("%v: code %d, want 0 (err=%v)", args, ee.Code, ee.Err)
		}
		if spy.fileCalls != 1 || spy.sendCalls != 0 {
			t.Fatalf("%v: SendFile=%d SendMessage=%d, want 1/0", args, spy.fileCalls, spy.sendCalls)
		}
		if spy.gotFileToken != "TOK" || spy.gotFile.Name != "report.pdf" ||
			spy.gotFile.Size != int64(len("PDF-содержимое")) || spy.gotFileContent != "PDF-содержимое" {
			t.Errorf("%v: token=%q name=%q size=%d content=%q", args, spy.gotFileToken, spy.gotFile.Name, spy.gotFile.Size, spy.gotFileContent)
		}
		if out := deps.Stdout.(*bytes.Buffer).String(); out != "901\n" {
			t.Errorf("%v: stdout %q, want \"901\\n\"", args, out)
		}
	}
}

// TestChatSendAttach_FlagErrors — повтор и пустой --attach, конфликты и
// невалидные значения прочих флагов → exit 1 ДО сети (ноль вызовов).
func TestChatSendAttach_FlagErrors(t *testing.T) {
	p := attachFile(t, "a.txt", "x")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"TOK", "--attach"}, "--attach требует значение"},
		{[]string{"TOK", "--attach", p, "--attach", p}, "только один раз"},
		{[]string{"TOK", "--attach=" + p, "--attach=" + p}, "только один раз"},
		{[]string{"TOK", "--attach="}, "непустой путь"},
		{[]string{"TOK", "--attach", ""}, "непустой путь"},
		{[]string{"TOK", "--attach", p, "--reply-to", "1", "--thread", "2"}, "взаимоисключаемы"},
		{[]string{"TOK", "--attach", p, "--thread", "2", "--thread-title", "Т"}, "взаимоисключаемы"},
		{[]string{"TOK", "--attach", p, "--thread-title", ""}, "не может быть пустым"},
		{[]string{"TOK", "--attach", p, "--reply-to", "0"}, "положительное число"},
		{[]string{"TOK", "--attach", p, "--thread", "abc"}, "целое число"},
		{[]string{"TOK", "--attach", p, "--nope"}, "неизвестный флаг"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		ee := chatSendHandler(context.Background(), deps, tc.args, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("%v: code=%d err=%v, want 1 и «%s»", tc.args, ee.Code, ee.Err, tc.want)
		}
		if spy.fileCalls+spy.sendCalls+spy.findCalls != 0 {
			t.Errorf("%v: вызовы клиента SendFile=%d SendMessage=%d FindRooms=%d, want 0", tc.args, spy.fileCalls, spy.sendCalls, spy.findCalls)
		}
	}
}

// TestChatSendAttach_FileErrors — файл не найден / каталог / нет прав чтения
// → exit 1 до сети: ни FindRooms (с --name), ни SendFile (§2.3).
func TestChatSendAttach_FileErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"нет файла", filepath.Join(dir, "missing.pdf"), "no such file"},
		{"каталог", dir, "не обычный файл"},
	}
	if os.Geteuid() != 0 { // root читает файл и без прав
		locked := attachFile(t, "locked.txt", "x")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		cases = append(cases, struct{ name, path, want string }{"нет прав чтения", locked, "permission denied"})
	}
	for _, tc := range cases {
		spy := &chatSpyClient{findRooms: []client.Room{{Token: "TOK", DisplayName: "Команда"}}}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		ee := chatSendHandler(context.Background(), deps, []string{"--name", "Команда", "--attach", tc.path}, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("%s: code=%d err=%v, want 1 и «%s»", tc.name, ee.Code, ee.Err, tc.want)
		}
		if spy.findCalls != 0 || spy.fileCalls != 0 {
			t.Errorf("%s: FindRooms=%d SendFile=%d, want 0/0 (файл проверяется до сети)", tc.name, spy.findCalls, spy.fileCalls)
		}
	}
}

// TestChatSendAttach_Caption — подпись: stdin или --file, ровно один
// завершающий перевод строки срезается, пустая подпись допустима (§2.2).
func TestChatSendAttach_Caption(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	capFile := attachFile(t, "caption.txt", "из файла\r\n")
	cases := []struct {
		name  string
		stdin string
		extra []string
		want  string
	}{
		{"stdin", "Отчёт за сентябрь\n", nil, "Отчёт за сентябрь"},
		{"многострочная", "a\nb\n\n", nil, "a\nb\n"},
		{"пустой stdin", "", nil, ""},
		{"--file", "игнорируется", []string{"--file", capFile}, "из файла"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, strings.NewReader(tc.stdin))
		args := append([]string{"TOK", "--attach", p}, tc.extra...)
		if ee := chatSendHandler(context.Background(), deps, args, false); ee.Code != ExitOK {
			t.Fatalf("%s: code %d (err=%v)", tc.name, ee.Code, ee.Err)
		}
		if spy.gotFileOpts.Caption != tc.want {
			t.Errorf("%s: Caption %q, want %q", tc.name, spy.gotFileOpts.Caption, tc.want)
		}
	}
}

// TestIsCharDevice — правило терминала (§2.2) на уровне предиката: символьное
// устройство (/dev/null) — true; пайп, обычный файл, не-*os.File и закрытый
// файл — false.
func TestIsCharDevice(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	regular, err := os.Open(attachFile(t, "r.txt", "x"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer regular.Close()
	closed, err := os.Open(attachFile(t, "c.txt", "x"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed.Close()

	cases := []struct {
		name string
		r    io.Reader
		want bool
	}{
		{"/dev/null", devnull, true},
		{"пайп", pr, false},
		{"обычный файл", regular, false},
		{"strings.Reader", strings.NewReader("x"), false},
		{"закрытый файл", closed, false},
	}
	for _, tc := range cases {
		if got := isCharDevice(tc.r); got != tc.want {
			t.Errorf("isCharDevice(%s): got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestChatSendAttach_TerminalStdin — stdin — символьное устройство: при
// --attach подпись не читается (файл уходит без подписи, exit 0), без
// --attach тело читается, как прежде. /dev/null, открытый только на запись, —
// детектор попытки чтения: read(2) на нём — EBADF, так что «прочитал вместо
// правила терминала» и «пропустил чтение тела» видны без терминала.
func TestChatSendAttach_TerminalStdin(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	wronly, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s O_WRONLY: %v", os.DevNull, err)
	}
	defer wronly.Close()
	p := attachFile(t, "report.pdf", "x")

	for _, tc := range []struct {
		name  string
		stdin *os.File
	}{{"/dev/null", devnull}, {"/dev/null только на запись", wronly}} {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, tc.stdin)
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != ExitOK {
			t.Fatalf("--attach, %s: code %d (err=%v) — stdin-устройство читалось", tc.name, ee.Code, ee.Err)
		}
		if spy.fileCalls != 1 || spy.gotFileOpts.Caption != "" {
			t.Errorf("--attach, %s: SendFile=%d Caption=%q, want 1 и пусто", tc.name, spy.fileCalls, spy.gotFileOpts.Caption)
		}
	}

	for _, tc := range []struct {
		name  string
		stdin *os.File
		want  string
	}{
		{"/dev/null", devnull, "тело сообщения пусто"},
		{"/dev/null только на запись", wronly, "не удалось прочитать тело"},
	} {
		spy := &chatSpyClient{}
		deps := newChatSendDeps(spy, tc.stdin)
		ee := chatSendHandler(context.Background(), deps, []string{"TOK"}, false)
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), tc.want) {
			t.Errorf("без --attach, %s: code=%d err=%v, want 1 «%s»", tc.name, ee.Code, ee.Err, tc.want)
		}
		if spy.sendCalls != 0 {
			t.Errorf("без --attach, %s: SendMessage=%d, want 0", tc.name, spy.sendCalls)
		}
	}
}

// TestChatSendAttach_NilStdinFallback — Deps.Stdin == nil → fallback на
// os.Stdin; правило терминала — на ИТОГОВОМ reader-е (§2.2): os.Stdin —
// /dev/null или терминал → подпись не читается и handler не ждёт ввода;
// os.Stdin — пайп → подпись читается. Подтест «только на запись» ловит
// проверку правила до fallback и без управляющего терминала (чтение = EBADF).
func TestChatSendAttach_NilStdinFallback(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	run := func(t *testing.T, stdin *os.File) string {
		t.Helper()
		orig := os.Stdin
		os.Stdin = stdin
		defer func() { os.Stdin = orig }()
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		done := make(chan ExitError, 1)
		go func() {
			done <- chatSendHandler(context.Background(), newChatDeps(spy), []string{"TOK", "--attach", p}, false)
		}()
		select {
		case ee := <-done:
			if ee.Code != ExitOK {
				t.Fatalf("code %d (err=%v)", ee.Code, ee.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("handler ждёт ввода: правило терминала проверено не на итоговом reader-е")
		}
		return spy.gotFileOpts.Caption
	}

	t.Run("/dev/null", func(t *testing.T) {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("open %s: %v", os.DevNull, err)
		}
		defer devnull.Close()
		if got := run(t, devnull); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("/dev/null только на запись", func(t *testing.T) {
		wronly, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("open %s O_WRONLY: %v", os.DevNull, err)
		}
		defer wronly.Close()
		if got := run(t, wronly); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("терминал", func(t *testing.T) {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			t.Skipf("нет управляющего терминала: %v", err)
		}
		defer tty.Close()
		if got := run(t, tty); got != "" {
			t.Errorf("Caption %q, want пусто", got)
		}
	})
	t.Run("пайп", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer pr.Close()
		if _, err := pw.WriteString("из пайпа\n"); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
		pw.Close()
		if got := run(t, pr); got != "из пайпа" {
			t.Errorf("Caption %q, want «из пайпа»", got)
		}
	})
}

// TestChatSendAttach_OptsPassThrough — --reply-to/--thread/--thread-title/
// --silent/--reference-id доходят до SendFileOpts без изменений.
func TestChatSendAttach_OptsPassThrough(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	cases := []struct {
		extra []string
		want  client.SendFileOpts
	}{
		{[]string{"--thread-title", "Тема", "--silent", "--reference-id", "ref-1"},
			client.SendFileOpts{Caption: "подпись", ThreadTitle: "Тема", Silent: true, ReferenceId: "ref-1"}},
		{[]string{"--reply-to", "5"}, client.SendFileOpts{Caption: "подпись", ReplyTo: 5}},
		{[]string{"--thread=7"}, client.SendFileOpts{Caption: "подпись", ThreadId: 7}},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
		deps := newChatSendDeps(spy, strings.NewReader("подпись"))
		if ee := chatSendHandler(context.Background(), deps, append([]string{"TOK", "--attach", p}, tc.extra...), false); ee.Code != ExitOK {
			t.Fatalf("%v: code %d (err=%v)", tc.extra, ee.Code, ee.Err)
		}
		if spy.gotFileOpts != tc.want {
			t.Errorf("%v: opts %+v, want %+v", tc.extra, spy.gotFileOpts, tc.want)
		}
	}
}

// TestChatSendAttach_Output — id найден: текст `<id>\n` / JSON {"id": <id>};
// не найден: пустой stdout / {"id": null}, предупреждение в stderr, exit 0.
func TestChatSendAttach_Output(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	lookupErr := errors.New("сообщение с referenceId abc не найдено среди 50 последних сообщений комнаты")
	cases := []struct {
		name       string
		res        client.SendFileResult
		jsonOut    bool
		wantStdout string
		wantStderr string
	}{
		{"id текстом", client.SendFileResult{MessageId: 901}, false, "901\n", ""},
		{"id в JSON", client.SendFileResult{MessageId: 901}, true, "{\n  \"id\": 901\n}\n", ""},
		{"нет id текстом", client.SendFileResult{Path: "/Talk/report.pdf", LookupErr: lookupErr}, false, "",
			"chat send: файл отправлен, но id сообщения не определён: " + lookupErr.Error() + "\n"},
		{"нет id в JSON", client.SendFileResult{Path: "/Talk/report.pdf", LookupErr: lookupErr}, true, "{\n  \"id\": null\n}\n",
			"chat send: файл отправлен, но id сообщения не определён: " + lookupErr.Error() + "\n"},
		{"нет id без причины", client.SendFileResult{}, false, "",
			"chat send: файл отправлен, но id сообщения не определён: причина не указана\n"},
	}
	for _, tc := range cases {
		spy := &chatSpyClient{fileResult: tc.res}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, tc.jsonOut); ee.Code != ExitOK || ee.Err != nil {
			t.Fatalf("%s: code=%d err=%v, want 0 без ошибки", tc.name, ee.Code, ee.Err)
		}
		if got := deps.Stdout.(*bytes.Buffer).String(); got != tc.wantStdout {
			t.Errorf("%s: stdout %q, want %q", tc.name, got, tc.wantStdout)
		}
		if got := deps.Stderr.(*bytes.Buffer).String(); got != tc.wantStderr {
			t.Errorf("%s: stderr %q, want %q", tc.name, got, tc.wantStderr)
		}
	}
}

// TestChatSendAttach_ClientErrors — *OCSError{404} (комната/тред/сообщение) →
// exit 2; прочие ошибки → 1; --name: 0 совпадений → 2, >1 → 3 без SendFile.
func TestChatSendAttach_ClientErrors(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&client.OCSError{Code: 404, Message: "SendFile: тред 9 не найден (или комната недоступна)"}, ExitNotFound},
		{errors.New("client: SendFile: PUT /Talk/report.pdf: HTTP 507"), ExitGeneric},
		{&client.OCSError{Code: 403, Message: "Forbidden"}, ExitGeneric},
	} {
		spy := &chatSpyClient{fileErr: tc.err}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != tc.want {
			t.Errorf("%v: code %d, want %d", tc.err, ee.Code, tc.want)
		}
	}

	for _, tc := range []struct {
		rooms []client.Room
		want  int
	}{
		{nil, ExitNotFound},
		{[]client.Room{{Token: "A", DisplayName: "Команда 1"}, {Token: "B", DisplayName: "Команда 2"}}, ExitAmbiguous},
	} {
		spy := &chatSpyClient{findRooms: tc.rooms}
		deps := newChatSendDeps(spy, strings.NewReader(""))
		ee := chatSendHandler(context.Background(), deps, []string{"--name", "Команда", "--attach", p}, false)
		if ee.Code != tc.want || spy.fileCalls != 0 {
			t.Errorf("--name (%d комнат): code=%d SendFile=%d, want %d и 0 вызовов", len(tc.rooms), ee.Code, spy.fileCalls, tc.want)
		}
	}
}

// TestChatSendAttach_FileClosed — файл закрывается по завершении handler-а.
func TestChatSendAttach_FileClosed(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 1}}
	deps := newChatSendDeps(spy, strings.NewReader(""))
	if ee := chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false); ee.Code != ExitOK {
		t.Fatalf("code %d (err=%v)", ee.Code, ee.Err)
	}
	fh, ok := spy.gotFile.Body.(*os.File)
	if !ok {
		t.Fatalf("Body: got %T, want *os.File", spy.gotFile.Body)
	}
	if _, err := fh.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("файл после handler-а: Read err=%v, want os.ErrClosed", err)
	}
}

// TestChatSendAttach_ViaRun — полный путь через Run: роутинг, глобальный --json.
func TestChatSendAttach_ViaRun(t *testing.T) {
	p := attachFile(t, "report.pdf", "x")
	spy := &chatSpyClient{fileResult: client.SendFileResult{MessageId: 901}}
	deps := newChatSendDeps(spy, strings.NewReader("подпись"))
	if code := Run([]string{"chat", "send", "TOK", "--attach", p, "--json"}, deps); code != ExitOK {
		t.Fatalf("Run code %d, stderr=%s", code, deps.Stderr.(*bytes.Buffer).String())
	}
	var got struct {
		Id int `json:"id"`
	}
	if err := json.Unmarshal(deps.Stdout.(*bytes.Buffer).Bytes(), &got); err != nil || got.Id != 901 {
		t.Errorf("stdout %q: id=%d err=%v, want 901", deps.Stdout.(*bytes.Buffer).String(), got.Id, err)
	}
	if spy.gotFileOpts.Caption != "подпись" {
		t.Errorf("Caption %q, want «подпись»", spy.gotFileOpts.Caption)
	}
}
