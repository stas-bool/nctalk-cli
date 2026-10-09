package client

// files_helpers.go — чистые хелперы отправки файла (спека-дельта 2026-10-09
// `chat send --attach`, §1, §3.4–§3.6, §4.2): имена-кандидаты по правилу
// веб-клиента Talk, referenceId, talkMetaData, папка вложений, срок загрузки.
// Без сети — тестируются таблицами (files_helpers_test.go); сам SendFile — в
// files.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// defaultAttachmentFolder — папка вложений Talk по умолчанию (§3.2): ею
// заменяется пустой или отсутствующий config.attachments.folder.
const defaultAttachmentFolder = "/Talk"

// uploadChunk — шаг надбавки к сроку загрузки (§4.2): 1 с на каждые начатые
// 64 КиБ, т.е. минимальная скорость канала около 512 кбит/с.
const uploadChunk = 64 << 10

// refTrimSet — символы PHP trim() по умолчанию: сервер нормализует
// referenceId как trim(substr(ref, 0, 64)) (spreed ChatManager::addSystemMessage).
const refTrimSet = " \t\n\r\x00\x0B"

var (
	// fileExtRe — расширение по правилу веб-клиента (src/utils/fileUpload.ts,
	// extensionRegex): точка и [0-9a-z]+ в конце имени, без учёта регистра.
	fileExtRe = regexp.MustCompile(`(?i)\.[0-9a-z]+$`)
	// fileSuffixRe — числовой суффикс « (N)» в конце имени без расширения
	// (suffixRegex веб-клиента).
	fileSuffixRe = regexp.MustCompile(` \((\d+)\)$`)
)

// isPlainSegment — s годится как ОДИН сегмент пути WebDAV: непустой, без «/»,
// не «.» и не «..». path.Join в транспорте схлопнул бы «.»/«..», и PUT ушёл бы
// на саму папку вложений или её родителя (защитное расширение §5, решение
// плана). Проверяются имя файла и uid.
func isPlainSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/")
}

// splitFileName разбирает имя по правилу веб-клиента (findUniquePath, §1):
// base, числовой суффикс n (1, если суффикса нет) и расширение ext.
//
// Отличие от веб-клиента (решение плана): совпадение расширения с позиции 0 —
// дотфайл («.bashrc») — расширением не считается: иначе base пуст и кандидат
// выглядел бы « (2).bashrc»; у нас — «.bashrc (2)». Суффикс, не влезающий в
// int, суффиксом не считается (остаётся частью base).
func splitFileName(name string) (base string, n int, ext string) {
	ext = fileExtRe.FindString(name)
	if len(ext) == len(name) {
		ext = ""
	}
	base = name[:len(name)-len(ext)]
	n = 1
	if m := fileSuffixRe.FindStringSubmatch(base); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			n = v
			base = base[:len(base)-len(m[0])]
		}
	}
	return base, n, ext
}

// nameCandidate — i-й кандидат имени файла на сервере (§1, модель): i=0 —
// исходное имя; i>=1 — base + « (n+i)» + ext, где n — суффикс исходного имени.
// report.pdf → report (2).pdf → report (3).pdf; x (3).pdf → x (4).pdf;
// a.tar.gz → a.tar (2).gz; Makefile → Makefile (2).
func nameCandidate(name string, i int) string {
	if i == 0 {
		return name
	}
	base, n, ext := splitFileName(name)
	return base + " (" + strconv.Itoa(n+i) + ")" + ext
}

// normalizeReferenceId повторяет серверную нормализацию referenceId (§1, §3.6):
// первые 64 БАЙТА, затем trim по refTrimSet. Поиск id сообщения сравнивает с
// этим значением.
func normalizeReferenceId(ref string) string {
	if len(ref) > 64 {
		ref = ref[:64]
	}
	return strings.Trim(ref, refTrimSet)
}

// generateReferenceId — случайный referenceId: 64 hex-символа из crypto/rand
// (docs Talk: «should be a random sha256», §2.4).
func generateReferenceId() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("client: SendFile: не удалось сгенерировать referenceId: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// talkMetaData — поля JSON-строки talkMetaData шаринга (§3.5): в строку
// попадают только заданные (omitempty). messageType не используется
// (голосовые сообщения — out of scope).
type talkMetaData struct {
	Caption     string `json:"caption,omitempty"`
	ReplyTo     int    `json:"replyTo,omitempty"`
	ThreadId    int    `json:"threadId,omitempty"`
	ThreadTitle string `json:"threadTitle,omitempty"`
	Silent      bool   `json:"silent,omitempty"`
}

// encodeTalkMetaData — talkMetaData как JSON-СТРОКА (её кладут строковым полем
// в тело шаринга, listener Talk делает json_decode): caption непуст,
// replyTo/threadId > 0, threadTitle непуст, silent = true; пустой набор →
// "{}" (как у веб-клиента).
func encodeTalkMetaData(m talkMetaData) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("client: SendFile: не удалось собрать talkMetaData: %w", err)
	}
	return string(b), nil
}

// normalizeAttachmentFolder — папка вложений из config.attachments.folder
// (§3.2): пусто → /Talk; иначе абсолютный путь без завершающего «/» и без
// «.»/«..» (path.Clean). Корень пользователя — «/».
func normalizeAttachmentFolder(folder string) string {
	if folder == "" {
		return defaultAttachmentFolder
	}
	return path.Clean("/" + folder)
}

// uploadTimeout — срок PUT по размеру файла (§4.2): base (NEXTCLOUD_TIMEOUT) +
// 1 с на каждые начатые 64 КиБ. base <= 0 — «без таймаута» (семантика
// http.Client.Timeout = 0) → 0: срок не ставится (решение плана).
// Переполнение (файлы больше ~600 ТБ) — потолок math.MaxInt64.
func uploadTimeout(base time.Duration, size int64) time.Duration {
	if base <= 0 {
		return 0
	}
	if size <= 0 {
		return base
	}
	chunks := (size-1)/uploadChunk + 1 // округление вверх без переполнения size+chunk
	if chunks > (math.MaxInt64-int64(base))/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return base + time.Duration(chunks)*time.Second
}
