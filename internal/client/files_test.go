package client

// files_test.go — SendFile (спека-дельта 2026-10-09 §5, §7): httptest-сервер
// fakeNC с маршрутизацией по методу и пути; ответы по умолчанию — живые
// фикстуры Task 1 (обезличенные), отдельные шаги переключаются полями fakeNC.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/transport"
)

// Нормализованные значения живых фикстур (Task 1, протокол нормализации).
const (
	fixtureUID   = "anna.s"
	fixtureToken = "tok-team"

	msgPlain         = 1500 // файл без подписи (refPlain)
	msgCaption       = 1501 // файл с подписью (refCaption)
	msgThreadRoot    = 1502 // файл + threadTitle: корень нового треда (refThread)
	msgThreadCreated = 1503 // system thread_created того же шаринга (refThread)
	msgNeighbor      = 1510 // обычное сообщение — «сосед» для context несуществующего id

	fixtureCaption = "nctalk attach fixture — подпись"
)

var (
	refPlain   = strings.Repeat("0", 63) + "1"
	refCaption = strings.Repeat("0", 63) + "2"
	refThread  = strings.Repeat("0", 63) + "3"
)

// fileFixture читает фикстуру из корневого testdata/.
func fileFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// ncReq — запись одного запроса к fakeNC.
type ncReq struct {
	Method           string
	Path             string // декодированный путь
	EscapedPath      string
	Query            url.Values
	Header           http.Header
	Body             []byte
	ContentLength    int64
	TransferEncoding []string
}

// fakeNC — httptest-сервер Nextcloud для SendFile. Фикстуры читаются в
// newFakeNC (t.Fatalf из горутины сервера недопустим); handler сериализован
// мьютексом — поля-ручки настраиваются тестом ДО вызова SendFile.
type fakeNC struct {
	srv *httptest.Server
	mu  sync.Mutex

	reqs []ncReq

	uid             string          // uid в путях WebDAV (совпадает с телом user)
	user            []byte          // тело cloud/user
	caps            []byte          // тело capabilities
	threadStatus    int             // threads/{id}: 0/200 → thread_info, 404 → thread_not_found
	context         []byte          // тело context при HTTP 200
	contextCode     int             // HTTP context: 0 → 200; 304; 404
	existing        map[string]bool // занятые пути (от корня пользователя): HEAD 200
	headStatus      int             // HEAD незанятого пути: 0 → 404
	putStatuses     []int           // очередь статусов PUT; пусто → 201
	putMessage      string          // s:message для PUT >= 400 (кроме 404/409/412 — живые XML)
	putRedirect     string          // непусто → PUT отвечает редиректом на этот URL
	putRedirectCode int             // код редиректа PUT (302/307)
	mkcolStatus     int             // 0 → 201
	shareStatus     int             // 0 → 200 (share_create.json)
	shareMessage    string          // meta.message ошибки шаринга
	deleteStatus    int             // 0 → 204
	chatStatus      int             // страница chat: 0 → 200
	chat            func(lastRef string) []byte
	lastRef         string // referenceId последнего шаринга

	fx map[string][]byte
}

func newFakeNC(t *testing.T) *fakeNC {
	t.Helper()
	f := &fakeNC{uid: fixtureUID, existing: map[string]bool{}, fx: map[string][]byte{}}
	for _, name := range []string{
		"cloud_user.json", "capabilities_attachments.json", "share_create.json",
		"chat_file_messages.json", "thread_info.json", "thread_not_found.json",
		"chat_context_message.json", "dav_error_409.xml", "dav_error_412.xml",
	} {
		f.fx[name] = fileFixture(t, name)
	}
	f.user = f.fx["cloud_user.json"]
	f.caps = f.fx["capabilities_attachments.json"]
	f.context = f.fx["chat_context_message.json"]
	chat := f.fx["chat_file_messages.json"]
	f.chat = func(string) []byte { return chat }
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// client — TalkClient на fakeNC через production-конструктор (настоящие
// http.Client с SameHostRedirectPolicy, отдельный upload-клиент).
func (f *fakeNC) client() *TalkClient { return NewTalkClient(testCfg(f.srv.URL)) }

func (f *fakeNC) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, ncReq{
		Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(),
		Query: r.URL.Query(), Header: r.Header.Clone(), Body: body,
		ContentLength: r.ContentLength, TransferEncoding: r.TransferEncoding,
	})

	const davPrefix = pathDAVFiles + "/"
	chatPrefix := pathChat + "/" + fixtureToken
	p := r.URL.Path
	switch {
	case p == "/redirected":
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(p, davPrefix+f.uid+"/"):
		f.handleDAV(w, r, strings.TrimPrefix(p, davPrefix+f.uid))
	case r.Method == http.MethodGet && p == pathCloudUser:
		writeOCS(w, http.StatusOK, f.user)
	case r.Method == http.MethodGet && p == pathCapabilities:
		writeOCS(w, http.StatusOK, f.caps)
	case r.Method == http.MethodPost && p == pathShares:
		var req struct {
			ReferenceId string `json:"referenceId"`
		}
		_ = json.Unmarshal(body, &req)
		f.lastRef = req.ReferenceId
		if f.shareStatus != 0 && f.shareStatus != http.StatusOK {
			writeOCS(w, f.shareStatus, ocsFailure(f.shareStatus, f.shareMessage))
			return
		}
		writeOCS(w, http.StatusOK, f.fx["share_create.json"])
	case r.Method == http.MethodGet && p == chatPrefix:
		if f.chatStatus != 0 && f.chatStatus != http.StatusOK {
			writeOCS(w, f.chatStatus, ocsFailure(f.chatStatus, "Internal Server Error"))
			return
		}
		writeOCS(w, http.StatusOK, f.chat(f.lastRef))
	case r.Method == http.MethodGet && strings.HasPrefix(p, chatPrefix+"/threads/"):
		if f.threadStatus == http.StatusNotFound {
			writeOCS(w, http.StatusNotFound, f.fx["thread_not_found.json"])
			return
		}
		writeOCS(w, http.StatusOK, f.fx["thread_info.json"])
	case r.Method == http.MethodGet && strings.HasPrefix(p, chatPrefix+"/") && strings.HasSuffix(p, "/context"):
		switch f.contextCode {
		case http.StatusNotModified:
			w.WriteHeader(http.StatusNotModified)
		case http.StatusNotFound:
			writeOCS(w, http.StatusNotFound, ocsFailure(http.StatusNotFound, "Message not found"))
		default:
			writeOCS(w, http.StatusOK, f.context)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeNC) handleDAV(w http.ResponseWriter, r *http.Request, userPath string) {
	switch r.Method {
	case http.MethodHead:
		switch {
		case f.existing[userPath]:
			w.WriteHeader(http.StatusOK)
		case f.headStatus != 0:
			w.WriteHeader(f.headStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		if f.putRedirect != "" {
			http.Redirect(w, r, f.putRedirect, f.putRedirectCode)
			return
		}
		status := http.StatusCreated
		if len(f.putStatuses) > 0 {
			status, f.putStatuses = f.putStatuses[0], f.putStatuses[1:]
		}
		switch {
		case status == http.StatusNotFound || status == http.StatusConflict:
			writeXML(w, status, f.fx["dav_error_409.xml"])
		case status == http.StatusPreconditionFailed:
			writeXML(w, status, f.fx["dav_error_412.xml"])
		case status >= 400:
			writeXML(w, status, sabreXML(f.putMessage))
		default:
			w.WriteHeader(status)
		}
	case "MKCOL":
		status := f.mkcolStatus
		if status == 0 {
			status = http.StatusCreated
		}
		if status >= 400 {
			writeXML(w, status, sabreXML("mkcol failed"))
			return
		}
		w.WriteHeader(status)
	case http.MethodDelete:
		status := f.deleteStatus
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeOCS(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeXML(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// ocsFailure — OCS-конверт ошибки (OCS v2: HTTP-статус = statuscode).
func ocsFailure(status int, message string) []byte {
	msg, _ := json.Marshal(message)
	return []byte(fmt.Sprintf(`{"ocs":{"meta":{"status":"failure","statuscode":%d,"message":%s},"data":[]}}`, status, msg))
}

// sabreXML — XML-ошибка Sabre с заданным s:message (форма живых фикстур).
func sabreXML(message string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns">` +
		`<s:exception>Sabre\DAV\Exception</s:exception><s:message>` + message + `</s:message></d:error>`)
}

// capsBody — capabilities с заданными spreed/allowed/folder.
func capsBody(t *testing.T, spreed, allowed bool, folder string) []byte {
	t.Helper()
	caps := map[string]any{}
	if spreed {
		caps["spreed"] = map[string]any{"config": map[string]any{"attachments": map[string]any{"allowed": allowed, "folder": folder}}}
	}
	return ocsBody(t, 200, "OK", map[string]any{"capabilities": caps})
}

func (f *fakeNC) requests() []ncReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ncReq(nil), f.reqs...)
}

// sequence — «МЕТОД путь» всех запросов по порядку.
func (f *fakeNC) sequence() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// byMethod — запросы с методом method и путём, начинающимся с prefix.
func (f *fakeNC) byMethod(method, prefix string) []ncReq {
	var out []ncReq
	for _, r := range f.requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// shareBody — JSON-тело единственного запроса шаринга.
func (f *fakeNC) shareBody(t *testing.T) map[string]any {
	t.Helper()
	shares := f.byMethod(http.MethodPost, pathShares)
	if len(shares) != 1 {
		t.Fatalf("запросов шаринга: got %d, want 1", len(shares))
	}
	var m map[string]any
	if err := json.Unmarshal(shares[0].Body, &m); err != nil {
		t.Fatalf("тело шаринга не JSON: %v; body=%s", err, shares[0].Body)
	}
	return m
}

func upload(name, content string) FileUpload {
	return FileUpload{Name: name, Size: int64(len(content)), Body: strings.NewReader(content)}
}

const davAnna = pathDAVFiles + "/" + fixtureUID

// TestSendFile_HappyPath — HEAD 404 → PUT 201 → шаринг → поиск (§7): порядок
// запросов, заголовки PUT (If-None-Match, Content-Type, Content-Length без
// chunked, Authorization, без OCS-заголовков), тело шаринга, query поиска.
func TestSendFile_HappyPath(t *testing.T) {
	f := newFakeNC(t)
	const content = "PDF-содержимое"
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", content), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.MessageId != msgPlain || res.Path != "/Talk/report.pdf" || res.LookupErr != nil {
		t.Errorf("result: got %+v, want MessageId=%d Path=/Talk/report.pdf LookupErr=nil", res, msgPlain)
	}
	want := []string{
		"GET " + pathCloudUser,
		"GET " + pathCapabilities,
		"HEAD " + davAnna + "/Talk/report.pdf",
		"PUT " + davAnna + "/Talk/report.pdf",
		"POST " + pathShares,
		"GET " + pathChat + "/" + fixtureToken,
	}
	if got := f.sequence(); !reflect.DeepEqual(got, want) {
		t.Fatalf("запросы:\n got %q\nwant %q", got, want)
	}

	put := f.byMethod(http.MethodPut, davAnna)[0]
	for h, v := range map[string]string{
		"If-None-Match":  "*",
		"Content-Type":   "application/octet-stream",
		"Authorization":  wantAuth(),
		"OCS-APIRequest": "",
	} {
		if got := put.Header.Get(h); got != v {
			t.Errorf("PUT %s: got %q, want %q", h, got, v)
		}
	}
	if put.ContentLength != int64(len(content)) || len(put.TransferEncoding) != 0 {
		t.Errorf("PUT: ContentLength=%d TransferEncoding=%v, want %d без chunked", put.ContentLength, put.TransferEncoding, len(content))
	}
	if string(put.Body) != content {
		t.Errorf("PUT body: got %q, want %q", put.Body, content)
	}

	share := f.byMethod(http.MethodPost, pathShares)[0]
	if share.Header.Get("Content-Type") != "application/json" || share.Header.Get("OCS-APIRequest") != "true" {
		t.Errorf("шаринг: Content-Type=%q OCS-APIRequest=%q", share.Header.Get("Content-Type"), share.Header.Get("OCS-APIRequest"))
	}
	body := f.shareBody(t)
	wantBody := map[string]any{
		"shareType":    float64(10),
		"shareWith":    fixtureToken,
		"path":         "/Talk/report.pdf",
		"referenceId":  refPlain,
		"talkMetaData": "{}",
	}
	if !reflect.DeepEqual(body, wantBody) {
		t.Errorf("тело шаринга:\n got %v\nwant %v", body, wantBody)
	}

	chat := f.byMethod(http.MethodGet, pathChat+"/"+fixtureToken)[0]
	for k, v := range map[string]string{"limit": "50", "lookIntoFuture": "0", "setReadMarker": "0"} {
		if got := chat.Query.Get(k); got != v {
			t.Errorf("поиск: query %s=%q, want %q", k, got, v)
		}
	}
}

// TestSendFile_TalkMetaData — talkMetaData — JSON-СТРОКА только с заданными
// полями; пустой набор → "{}" (§3.5).
func TestSendFile_TalkMetaData(t *testing.T) {
	cases := []struct {
		name string
		opts SendFileOpts
		want string
	}{
		{"пустой набор", SendFileOpts{}, `{}`},
		{"подпись", SendFileOpts{Caption: fixtureCaption}, `{"caption":"nctalk attach fixture — подпись"}`},
		{"ответ", SendFileOpts{ReplyTo: msgCaption}, `{"replyTo":1501}`},
		{"тред", SendFileOpts{ThreadId: msgThreadRoot}, `{"threadId":1502}`},
		{"новый тред без уведомления", SendFileOpts{ThreadTitle: "Тема", Silent: true}, `{"threadTitle":"Тема","silent":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			tc.opts.ReferenceId = refPlain
			if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), tc.opts); err != nil {
				t.Fatalf("SendFile: %v", err)
			}
			got, ok := f.shareBody(t)["talkMetaData"].(string)
			if !ok {
				t.Fatalf("talkMetaData: не строка (%T)", f.shareBody(t)["talkMetaData"])
			}
			if got != tc.want {
				t.Errorf("talkMetaData: got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSendFile_GeneratedReferenceId — пустой или пустой после нормализации
// referenceId → 64 hex из crypto/rand (§2.4); поиск находит сообщение по нему.
func TestSendFile_GeneratedReferenceId(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, given := range []string{"", "   \t"} {
		f := newFakeNC(t)
		chat := f.fx["chat_file_messages.json"]
		f.chat = func(lastRef string) []byte { return bytes.ReplaceAll(chat, []byte(refPlain), []byte(lastRef)) }
		res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: given})
		if err != nil {
			t.Fatalf("ReferenceId=%q: SendFile: %v", given, err)
		}
		ref, _ := f.shareBody(t)["referenceId"].(string)
		if !hex64.MatchString(ref) {
			t.Errorf("ReferenceId=%q: в шаринг ушёл %q, want 64 hex", given, ref)
		}
		if res.MessageId != msgPlain {
			t.Errorf("ReferenceId=%q: MessageId=%d, want %d (LookupErr=%v)", given, res.MessageId, msgPlain, res.LookupErr)
		}
	}
}

// TestSendFile_ReferenceIdNormalized — заданный referenceId уходит серверу как
// есть, поиск сравнивает с серверной нормализацией: 64 байта, затем trim (§3.6).
func TestSendFile_ReferenceIdNormalized(t *testing.T) {
	f := newFakeNC(t)
	raw := "  " + strings.Repeat("ab", 40)
	stored := strings.Repeat("ab", 31) // trim(substr(raw, 0, 64)) — как сохранит сервер
	chat := f.fx["chat_file_messages.json"]
	f.chat = func(string) []byte { return bytes.ReplaceAll(chat, []byte(refPlain), []byte(stored)) }
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: raw})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if got := f.shareBody(t)["referenceId"]; got != raw {
		t.Errorf("referenceId шаринга: got %q, want исходный %q", got, raw)
	}
	if res.MessageId != msgPlain {
		t.Errorf("MessageId: got %d, want %d (LookupErr=%v)", res.MessageId, msgPlain, res.LookupErr)
	}
}

// TestSendFile_UniqueName — занятые имена пропускаются по правилу веб-клиента
// (§1): HEAD 200 → следующий кандидат; путь шаринга = итоговое имя.
func TestSendFile_UniqueName(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		want     string
	}{
		{"report.pdf", []string{"/Talk/report.pdf", "/Talk/report (2).pdf"}, "/Talk/report (3).pdf"},
		{"x (3).pdf", []string{"/Talk/x (3).pdf"}, "/Talk/x (4).pdf"},
		{"Makefile", []string{"/Talk/Makefile"}, "/Talk/Makefile (2)"},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		for _, p := range tc.existing {
			f.existing[p] = true
		}
		res, err := f.client().SendFile(context.Background(), fixtureToken, upload(tc.name, "x"), SendFileOpts{ReferenceId: refPlain})
		if err != nil {
			t.Fatalf("%s: SendFile: %v", tc.name, err)
		}
		if res.Path != tc.want {
			t.Errorf("%s: Path=%q, want %q", tc.name, res.Path, tc.want)
		}
		if n := len(f.byMethod(http.MethodHead, davAnna)); n != len(tc.existing)+1 {
			t.Errorf("%s: HEAD-запросов %d, want %d", tc.name, n, len(tc.existing)+1)
		}
		puts := f.byMethod(http.MethodPut, davAnna)
		if len(puts) != 1 || puts[0].Path != davAnna+tc.want {
			t.Errorf("%s: PUT %v, want один PUT %s", tc.name, f.sequence(), davAnna+tc.want)
		}
		if got := f.shareBody(t)["path"]; got != tc.want {
			t.Errorf("%s: path шаринга %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSendFile_NameCandidatesLimit — все имена заняты: после 1000 кандидатов
// ошибка без PUT и шаринга (§3.4).
func TestSendFile_NameCandidatesLimit(t *testing.T) {
	f := newFakeNC(t)
	f.headStatus = http.StatusOK
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "не удалось подобрать свободное имя") {
		t.Fatalf("err: got %v, want «не удалось подобрать свободное имя»", err)
	}
	if n := len(f.byMethod(http.MethodHead, davAnna)); n != maxNameCandidates {
		t.Errorf("HEAD-запросов %d, want %d", n, maxNameCandidates)
	}
	if n := len(f.byMethod(http.MethodPut, davAnna)) + len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
		t.Errorf("PUT/шаринг: %d запросов, want 0", n)
	}
}

// closeCountingBody — ReadSeeker с Close-счётчиком: net/http закрыл бы его,
// будь он передан телом запроса без не-закрывающей обёртки.
type closeCountingBody struct {
	*bytes.Reader
	closes int32
}

func (b *closeCountingBody) Close() error {
	atomic.AddInt32(&b.closes, 1)
	return nil
}

// TestSendFile_PutRace412 — PUT 412 (имя заняли между HEAD и PUT) → следующий
// кандидат; тело перемотано и не закрыто (повторный PUT несёт тот же файл).
func TestSendFile_PutRace412(t *testing.T) {
	f := newFakeNC(t)
	f.putStatuses = []int{http.StatusPreconditionFailed, http.StatusCreated}
	const content = "race-content"
	body := &closeCountingBody{Reader: bytes.NewReader([]byte(content))}
	res, err := f.client().SendFile(context.Background(), fixtureToken,
		FileUpload{Name: "report.pdf", Size: int64(len(content)), Body: body}, SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.Path != "/Talk/report (2).pdf" {
		t.Errorf("Path: got %q, want /Talk/report (2).pdf", res.Path)
	}
	puts := f.byMethod(http.MethodPut, davAnna)
	if len(puts) != 2 {
		t.Fatalf("PUT: %d запросов, want 2 (412 → следующее имя)", len(puts))
	}
	if string(puts[0].Body) != content || string(puts[1].Body) != content {
		t.Errorf("тела PUT: %q / %q, want дважды %q", puts[0].Body, puts[1].Body, content)
	}
	if n := atomic.LoadInt32(&body.closes); n != 0 {
		t.Errorf("тело закрыто %d раз, want 0", n)
	}
}

// TestSendFile_PutRetryRealFile — тот же сценарий 412 на настоящем *os.File:
// без не-закрывающей обёртки второй PUT упал бы «file already closed».
func TestSendFile_PutRetryRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(p, []byte("file-content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fh, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()

	f := newFakeNC(t)
	f.putStatuses = []int{http.StatusPreconditionFailed, http.StatusCreated}
	if _, err := f.client().SendFile(context.Background(), fixtureToken,
		FileUpload{Name: "report.pdf", Size: 12, Body: fh}, SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	puts := f.byMethod(http.MethodPut, davAnna)
	if len(puts) != 2 || string(puts[1].Body) != "file-content" {
		t.Fatalf("PUT: %d запросов, want 2 с телом file-content", len(puts))
	}
}

// TestSendFile_MissingFolder — PUT 404 (живой сервер) или 409 (голый Sabre) —
// нет папки вложений → MKCOL → повтор PUT (один раз); MKCOL 405 (гонка) — ок;
// прочие исходы — ошибка (§3.4).
func TestSendFile_MissingFolder(t *testing.T) {
	cases := []struct {
		name    string
		puts    []int
		mkcol   int
		wantErr string // "" — успех
	}{
		{"404 → MKCOL 201 → PUT 201", []int{404, 201}, 201, ""},
		{"409 → MKCOL 201 → PUT 201", []int{409, 201}, 201, ""},
		{"MKCOL 405 (уже есть)", []int{409, 201}, 405, ""},
		{"MKCOL 409 (нет родителя)", []int{409}, 409, "папка вложений /Talk недоступна (MKCOL: HTTP 409"},
		{"PUT 409 после MKCOL", []int{409, 409}, 201, "папка вложений /Talk недоступна (PUT: HTTP 409)"},
		{"PUT 404 после MKCOL", []int{404, 404}, 201, "папка вложений /Talk недоступна (PUT: HTTP 404)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.putStatuses = tc.puts
			f.mkcolStatus = tc.mkcol
			_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
			mk := f.byMethod("MKCOL", davAnna)
			if len(mk) != 1 || mk[0].Path != davAnna+"/Talk" {
				t.Errorf("MKCOL: %v, want один MKCOL %s/Talk", f.sequence(), davAnna)
			}
			shares := len(f.byMethod(http.MethodPost, pathShares))
			if tc.wantErr == "" {
				if err != nil || shares != 1 {
					t.Fatalf("err=%v, шарингов %d; want успех и 1 шаринг", err, shares)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.wantErr)
			}
			var oe *OCSError
			if errors.As(err, &oe) {
				t.Errorf("ошибка WebDAV стала *OCSError{%d}: cli дал бы exit 2 вместо 1", oe.Code)
			}
			if shares != 0 {
				t.Errorf("шаринг при ошибке загрузки: %d запросов, want 0", shares)
			}
		})
	}
}

// TestSendFile_DAVErrors — отказы WebDAV → ошибка с методом, путём, кодом,
// подсказкой и s:message; шаринга нет (§3.4).
func TestSendFile_DAVErrors(t *testing.T) {
	cases := []struct {
		status int
		msg    string
		want   []string
	}{
		{413, "Request Entity Too Large", []string{"PUT /Talk/report.pdf: HTTP 413", "лимита", "Request Entity Too Large"}},
		{507, "Insufficient Storage", []string{"HTTP 507", "места", "Insufficient Storage"}},
		{403, "Access denied", []string{"HTTP 403", "нет прав", "Access denied"}},
		{423, "File is locked", []string{"HTTP 423", "заблокирован", "File is locked"}},
		{500, "Internal error", []string{"HTTP 500", "Internal error"}},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		f.putStatuses = []int{tc.status}
		f.putMessage = tc.msg
		_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
		if err == nil {
			t.Fatalf("PUT %d: err = nil", tc.status)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("PUT %d: err %q не содержит %q", tc.status, err.Error(), w)
			}
		}
		if n := len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
			t.Errorf("PUT %d: шаринг вызван %d раз, want 0", tc.status, n)
		}
	}

	f := newFakeNC(t)
	f.headStatus = http.StatusUnauthorized
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "HEAD /Talk/report.pdf: HTTP 401") {
		t.Errorf("HEAD 401: err %v, want «HEAD /Talk/report.pdf: HTTP 401»", err)
	}
}

// TestSendFile_ShareNotFoundCleanup — шаринг 404 «Conversation not found» →
// *OCSError{404} (exit 2 в cli) и DELETE загруженного файла (§3.5).
func TestSendFile_ShareNotFoundCleanup(t *testing.T) {
	f := newFakeNC(t)
	f.shareStatus = http.StatusNotFound
	f.shareMessage = "Conversation not found"
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound {
		t.Fatalf("err: got %v, want *OCSError{404}", err)
	}
	dels := f.byMethod(http.MethodDelete, davAnna)
	if len(dels) != 1 || dels[0].Path != davAnna+"/Talk/report.pdf" {
		t.Errorf("DELETE: %v, want один DELETE %s/Talk/report.pdf", f.sequence(), davAnna)
	}
}

// TestSendFile_ShareErrorCleanupFailed — шаринг 403 и неудачный DELETE → путь
// файла в тексте ошибки (удалить вручную); код ошибки шаринга сохранён.
func TestSendFile_ShareErrorCleanupFailed(t *testing.T) {
	f := newFakeNC(t)
	f.shareStatus = http.StatusForbidden
	f.shareMessage = "Sharing is not allowed"
	f.deleteStatus = http.StatusInternalServerError
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil {
		t.Fatal("err = nil, want ошибку шаринга")
	}
	for _, w := range []string{"Sharing is not allowed", "/Talk/report.pdf", "удалите вручную"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err %q не содержит %q", err.Error(), w)
		}
	}
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusForbidden {
		t.Errorf("err: want *OCSError{403} в цепочке, got %v", err)
	}
}

// TestSendFile_LookupSkipsSystem — thread_created того же шаринга несёт тот же
// referenceId и больший id — пропускается: берётся сообщение-файл (§3.6).
func TestSendFile_LookupSkipsSystem(t *testing.T) {
	f := newFakeNC(t)
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ReferenceId: refThread, ThreadTitle: "Фикстурный тред с файлом"})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.MessageId != msgThreadRoot {
		t.Errorf("MessageId: got %d, want %d (не system %d)", res.MessageId, msgThreadRoot, msgThreadCreated)
	}
}

// TestSendFile_LookupMiss — сообщения нет в окне или поиск упал: файл уже
// отправлен → error == nil, MessageId 0 и причина в LookupErr (§2.5).
func TestSendFile_LookupMiss(t *testing.T) {
	f := newFakeNC(t)
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ReferenceId: strings.Repeat("f", 64)})
	if err != nil {
		t.Fatalf("SendFile: %v (промах поиска — не ошибка)", err)
	}
	if res.MessageId != 0 || res.Path != "/Talk/report.pdf" || res.LookupErr == nil ||
		!strings.Contains(res.LookupErr.Error(), "не найдено") {
		t.Errorf("result: got %+v, want MessageId 0, Path /Talk/report.pdf, LookupErr «не найдено»", res)
	}

	f2 := newFakeNC(t)
	f2.chatStatus = http.StatusInternalServerError
	res, err = f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v (сбой поиска — не ошибка)", err)
	}
	if res.MessageId != 0 || res.LookupErr == nil {
		t.Errorf("сбой поиска: got %+v, want MessageId 0 и LookupErr", res)
	}
}

// TestSendFile_PrecheckThread — тред есть (200) → загрузка идёт; нет (404) →
// *OCSError{404} и ни одного HEAD/PUT (§3.3).
func TestSendFile_PrecheckThread(t *testing.T) {
	f := newFakeNC(t)
	if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
		SendFileOpts{ThreadId: msgThreadRoot, ReferenceId: refPlain}); err != nil {
		t.Fatalf("тред 200: SendFile: %v", err)
	}
	seq := f.sequence()
	if len(seq) < 4 || seq[2] != "GET "+pathChat+"/"+fixtureToken+"/threads/1502" || !strings.HasPrefix(seq[3], "HEAD ") {
		t.Errorf("тред 200: запросы %q, want threads/1502 перед HEAD", seq)
	}

	f2 := newFakeNC(t)
	f2.threadStatus = http.StatusNotFound
	_, err := f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ThreadId: 999999})
	var oe *OCSError
	if !errors.As(err, &oe) || oe.Code != http.StatusNotFound || !strings.Contains(err.Error(), "тред 999999 не найден") {
		t.Fatalf("тред 404: err %v, want *OCSError{404} «тред 999999 не найден»", err)
	}
	if n := len(f2.byMethod(http.MethodHead, davAnna)) + len(f2.byMethod(http.MethodPut, davAnna)); n != 0 {
		t.Errorf("тред 404: HEAD/PUT %d запросов, want 0", n)
	}
}

// TestSendFile_PrecheckReply — context?limit=0 (§3.3): совпал id и replyable →
// загрузка; сосед, 304, пустой массив, 404 сервера → *OCSError{404};
// isReplyable=false → обычная ошибка. Без загрузки во всех отказах.
func TestSendFile_PrecheckReply(t *testing.T) {
	empty := []byte(`{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":[]}}`)
	cases := []struct {
		name     string
		fixture  string // фикстура context ("" — тело empty)
		code     int    // HTTP context
		replyTo  int
		want404  bool
		wantText string // "" — успех
	}{
		{"совпал и replyable", "chat_context_message.json", 0, msgCaption, false, ""},
		{"соседнее сообщение", "chat_context_neighbor.json", 0, 999999, true, "сообщение 999999 не найдено"},
		{"304", "chat_context_message.json", http.StatusNotModified, msgCaption, true, "не найдено"},
		{"пустой массив", "", 0, msgCaption, true, "не найдено"},
		{"404 сервера", "chat_context_message.json", http.StatusNotFound, msgCaption, true, "(или комната недоступна)"},
		{"system не replyable", "chat_context_system.json", 0, msgThreadCreated, false, "нельзя ответить"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.context = empty
			if tc.fixture != "" {
				f.context = fileFixture(t, tc.fixture)
			}
			f.contextCode = tc.code
			_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"),
				SendFileOpts{ReplyTo: tc.replyTo, ReferenceId: refPlain})
			ctxReqs := f.byMethod(http.MethodGet, pathChat+"/"+fixtureToken+"/")
			if len(ctxReqs) != 1 || ctxReqs[0].Query.Get("limit") != "0" || !strings.HasSuffix(ctxReqs[0].Path, fmt.Sprintf("/%d/context", tc.replyTo)) {
				t.Fatalf("context-запрос: %v", f.sequence())
			}
			heads := len(f.byMethod(http.MethodHead, davAnna))
			if tc.wantText == "" {
				if err != nil || heads != 1 {
					t.Fatalf("err=%v HEAD=%d, want успех и загрузку", err, heads)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.wantText)
			}
			var oe *OCSError
			if is404 := errors.As(err, &oe) && oe.Code == http.StatusNotFound; is404 != tc.want404 {
				t.Errorf("OCSError{404}: got %v, want %v (err=%v)", is404, tc.want404, err)
			}
			if heads != 0 {
				t.Errorf("загрузка при отказе pre-check: HEAD %d, want 0", heads)
			}
		})
	}
}

// TestSendFile_AttachmentsSettings — allowed=false → ошибка до pre-check и
// загрузки (с ThreadId — ни одного threads/{id}); нет spreed → «Talk
// недоступен» (§3.2, §5 шаг 2).
func TestSendFile_AttachmentsSettings(t *testing.T) {
	f := newFakeNC(t)
	f.caps = capsBody(t, true, false, "/Talk")
	_, err := f.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{ThreadId: msgThreadRoot})
	if err == nil || !strings.Contains(err.Error(), "вложения запрещены") {
		t.Fatalf("allowed=false: err %v, want «вложения запрещены»", err)
	}
	if want := []string{"GET " + pathCloudUser, "GET " + pathCapabilities}; !reflect.DeepEqual(f.sequence(), want) {
		t.Errorf("allowed=false: запросы %q, want %q", f.sequence(), want)
	}

	f2 := newFakeNC(t)
	f2.caps = capsBody(t, false, false, "")
	_, err = f2.client().SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
	if err == nil || !strings.Contains(err.Error(), "Talk недоступен") {
		t.Fatalf("нет spreed: err %v, want «Talk недоступен»", err)
	}
}

// TestSendFile_CustomFolderAndUID — нестандартная папка и uid со спецсимволами:
// сегменты экранированы транспортом, path шаринга — от корня пользователя.
func TestSendFile_CustomFolderAndUID(t *testing.T) {
	f := newFakeNC(t)
	f.uid = "anna smith@corp"
	f.user = ocsBody(t, 200, "OK", map[string]any{"id": "anna smith@corp"})
	f.caps = capsBody(t, true, true, "/Docs/Chat/")
	res, err := f.client().SendFile(context.Background(), fixtureToken, upload("отчёт #1.pdf", "x"), SendFileOpts{ReferenceId: refPlain})
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.Path != "/Docs/Chat/отчёт #1.pdf" {
		t.Errorf("Path: got %q", res.Path)
	}
	heads := f.byMethod(http.MethodHead, pathDAVFiles)
	const want = "/remote.php/dav/files/anna%20smith@corp/Docs/Chat/%D0%BE%D1%82%D1%87%D1%91%D1%82%20%231.pdf"
	if len(heads) != 1 || heads[0].EscapedPath != want {
		t.Errorf("HEAD escaped path: %v, want %s", heads, want)
	}
	if got := f.shareBody(t)["path"]; got != "/Docs/Chat/отчёт #1.pdf" {
		t.Errorf("path шаринга: got %q", got)
	}
}

// TestSendFile_Validation — клиентская валидация до сети (§5 шаг 1): ни одного
// запроса.
func TestSendFile_Validation(t *testing.T) {
	ok := upload("report.pdf", "x")
	cases := []struct {
		name string
		f    FileUpload
		opts SendFileOpts
	}{
		{"пустое имя", FileUpload{Name: "", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя .", FileUpload{Name: ".", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя ..", FileUpload{Name: "..", Body: strings.NewReader("")}, SendFileOpts{}},
		{"имя с /", FileUpload{Name: "a/b.txt", Body: strings.NewReader("")}, SendFileOpts{}},
		{"нет Body", FileUpload{Name: "a.txt"}, SendFileOpts{}},
		{"Size < 0", FileUpload{Name: "a.txt", Size: -1, Body: strings.NewReader("")}, SendFileOpts{}},
		{"ReplyTo < 0", ok, SendFileOpts{ReplyTo: -1}},
		{"ThreadId < 0", ok, SendFileOpts{ThreadId: -2}},
		{"ReplyTo + ThreadId", ok, SendFileOpts{ReplyTo: 1, ThreadId: 2}},
		{"ReplyTo + ThreadTitle", ok, SendFileOpts{ReplyTo: 1, ThreadTitle: "Т"}},
		{"ThreadId + ThreadTitle", ok, SendFileOpts{ThreadId: 2, ThreadTitle: "Т"}},
	}
	for _, tc := range cases {
		f := newFakeNC(t)
		if _, err := f.client().SendFile(context.Background(), fixtureToken, tc.f, tc.opts); err == nil {
			t.Errorf("%s: err = nil, want ошибку валидации", tc.name)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("%s: %d запросов, want 0 (валидация до сети)", tc.name, n)
		}
	}
}

// TestSendFile_EmptyFile — пустой файл допустим (§2.3): PUT с Content-Length 0
// без chunked.
func TestSendFile_EmptyFile(t *testing.T) {
	f := newFakeNC(t)
	if _, err := f.client().SendFile(context.Background(), fixtureToken, upload("empty.txt", ""), SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	put := f.byMethod(http.MethodPut, davAnna)[0]
	if put.ContentLength != 0 || len(put.TransferEncoding) != 0 || len(put.Body) != 0 {
		t.Errorf("PUT пустого файла: ContentLength=%d TransferEncoding=%v body=%d байт", put.ContentLength, put.TransferEncoding, len(put.Body))
	}
}

// deadlineDoer — Doer-обёртка: запоминает методы запросов и остаток срока
// контекста по методу (0 — срока нет).
type deadlineDoer struct {
	mu      sync.Mutex
	methods []string
	left    map[string]time.Duration
	inner   *http.Client
}

func newDeadlineDoer() *deadlineDoer {
	return &deadlineDoer{left: map[string]time.Duration{}, inner: &http.Client{}}
}

func (d *deadlineDoer) Do(r *http.Request) (*http.Response, error) {
	var left time.Duration
	if dl, ok := r.Context().Deadline(); ok {
		left = time.Until(dl)
	}
	d.mu.Lock()
	d.methods = append(d.methods, r.Method)
	d.left[r.Method] = left
	d.mu.Unlock()
	return d.inner.Do(r)
}

// TestSendFile_UploadDoerAndDeadline — PUT идёт через uploadDoer (без общего
// таймаута) со сроком контекста NEXTCLOUD_TIMEOUT + 1 с на 64 КиБ (§4.2);
// остальные запросы — через обычный doer и без контекстного срока.
func TestSendFile_UploadDoerAndDeadline(t *testing.T) {
	f := newFakeNC(t)
	main, up := newDeadlineDoer(), newDeadlineDoer()
	c := NewTalkClientWithDoer(testCfg(f.srv.URL), main) // testCfg: Timeout 5s
	c.uploadDoer = up
	content := strings.Repeat("x", 200<<10) // 200 КиБ → 4 куска по 64 КиБ
	if _, err := c.SendFile(context.Background(), fixtureToken, upload("big.bin", content), SendFileOpts{ReferenceId: refPlain}); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	want := uploadTimeout(5*time.Second, int64(len(content)))
	if want != 9*time.Second {
		t.Fatalf("uploadTimeout(5s, 200 КиБ) = %v, want 9s", want)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	main.mu.Lock()
	defer main.mu.Unlock()
	if !reflect.DeepEqual(up.methods, []string{http.MethodPut}) {
		t.Errorf("uploadDoer: запросы %v, want только PUT", up.methods)
	}
	for _, m := range main.methods {
		if m == http.MethodPut {
			t.Errorf("PUT ушёл через обычный doer (Timeout оборвал бы большой файл): %v", main.methods)
		}
	}
	if got := up.left[http.MethodPut]; got <= want-time.Second || got > want {
		t.Errorf("срок PUT: осталось %v, want (%v, %v]", got, want-time.Second, want)
	}
	if got := main.left[http.MethodHead]; got != 0 {
		t.Errorf("HEAD: контекстный срок %v, want нет", got)
	}
}

// TestSendFile_PutRedirect — редиректы PUT (§4.1): чужой хост блокируется без
// пароля в тексте ошибки; 302 на свой хост превращает PUT в GET (200), 307 Go
// не переотправляет — в обоих случаях ошибка, молчаливого успеха и шаринга нет.
func TestSendFile_PutRedirect(t *testing.T) {
	var evilHits int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&evilHits, 1)
	}))
	defer evil.Close()

	cases := []struct {
		name string
		to   func(f *fakeNC) string
		code int
		want string
	}{
		{"чужой хост 302", func(*fakeNC) string { return evil.URL + "/leak" }, http.StatusFound, "cross-host redirect blocked"},
		{"свой хост 302 → GET", func(f *fakeNC) string { return f.srv.URL + "/redirected" }, http.StatusFound, "PUT /Talk/report.pdf: HTTP 200"},
		{"свой хост 307", func(f *fakeNC) string { return f.srv.URL + "/redirected" }, http.StatusTemporaryRedirect, "PUT /Talk/report.pdf: HTTP 307"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNC(t)
			f.putRedirect = tc.to(f)
			f.putRedirectCode = tc.code
			cfg := testCfg(f.srv.URL)
			cfg.Password = "SECRET_MARKER"
			_, err := NewTalkClient(cfg).SendFile(context.Background(), fixtureToken, upload("report.pdf", "x"), SendFileOpts{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err: got %v, want содержит %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Errorf("УТЕЧКА пароля в тексте ошибки: %q", err.Error())
			}
			if n := len(f.byMethod(http.MethodPost, pathShares)); n != 0 {
				t.Errorf("шаринг после неудачного PUT: %d, want 0", n)
			}
		})
	}
	if n := atomic.LoadInt32(&evilHits); n != 0 {
		t.Errorf("чужой хост получил %d запросов, want 0", n)
	}
}

// TestFileFixtures_LiveFormat — инварианты живого формата (Task 1, спека §1
// «модель»), на которые опираются SendFile и тесты выше: сообщение-файл —
// comment c messageParameters.file (объект), message "{file}" без подписи и
// текст подписи с ней; thread_created — system с тем же referenceId; context —
// одно сообщение. Расхождение с живым API правится здесь и в спеке, не в
// фикстурах.
func TestFileFixtures_LiveFormat(t *testing.T) {
	raw := fileFixture(t, "chat_file_messages.json")
	if bytes.Contains(raw, []byte(`"messageParameters": [{`)) || bytes.Contains(raw, []byte(`"messageParameters":[{`)) {
		t.Error("непустой массив messageParameters — формат /v4/room, в chat-API недопустим")
	}
	var env transport.OCSEnvelope[[]Message]
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode chat_file_messages.json: %v", err)
	}
	byID := map[int]Message{}
	for _, m := range env.OCS.Data {
		byID[m.Id] = m
	}
	if len(byID) != 4 {
		t.Fatalf("сообщений %d, want 4 (1500–1503: только свои)", len(byID))
	}
	for _, id := range []int{msgPlain, msgCaption, msgThreadRoot} {
		m := byID[id]
		if m.MessageType != "comment" || m.SystemMessage != "" || m.MessageParameters["file"].Type != "file" || m.MessageParameters["file"].Name == "" {
			t.Errorf("%d: type=%q system=%q file=%+v, want comment с параметром file", id, m.MessageType, m.SystemMessage, m.MessageParameters["file"])
		}
	}
	if m := byID[msgPlain]; m.Message != "{file}" || m.ReferenceId != refPlain {
		t.Errorf("%d: message=%q ref=%q, want {file}/refPlain", msgPlain, m.Message, m.ReferenceId)
	}
	if m := byID[msgCaption]; m.Message != fixtureCaption || m.ReferenceId != refCaption {
		t.Errorf("%d: message=%q ref=%q, want подпись без {file}/refCaption", msgCaption, m.Message, m.ReferenceId)
	}
	if m := byID[msgThreadRoot]; m.ReferenceId != refThread || m.ThreadTitle == "" {
		t.Errorf("%d: ref=%q threadTitle=%q, want refThread и название треда", msgThreadRoot, m.ReferenceId, m.ThreadTitle)
	}
	if m := byID[msgThreadCreated]; m.MessageType != "system" || m.SystemMessage != "thread_created" || m.ReferenceId != refThread {
		t.Errorf("%d: type=%q system=%q ref=%q, want system thread_created с refThread", msgThreadCreated, m.MessageType, m.SystemMessage, m.ReferenceId)
	}

	for name, want := range map[string]struct {
		id        int
		replyable bool
	}{
		"chat_context_message.json":  {msgCaption, true},
		"chat_context_neighbor.json": {msgNeighbor, true},
		"chat_context_system.json":   {msgThreadCreated, false},
	} {
		var ctxEnv transport.OCSEnvelope[[]Message]
		if err := json.Unmarshal(fileFixture(t, name), &ctxEnv); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		d := ctxEnv.OCS.Data
		if len(d) != 1 || d[0].Id != want.id || d[0].IsReplyable != want.replyable {
			t.Errorf("%s: %d сообщений, первое %+v; want одно id=%d isReplyable=%v", name, len(d), d, want.id, want.replyable)
		}
	}
}
