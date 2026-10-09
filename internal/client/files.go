package client

// files.go — отправка локального файла в чат (спека-дельта 2026-10-09
// `chat send --attach`, §3, §5). Классический механизм Talk: WebDAV-загрузка в
// папку вложений + OCS-шаринг shareType=10. Сообщение в чате создаёт listener
// Talk на событие создания шара, и ответ шаринга id сообщения НЕ содержит —
// сообщение находится поиском по referenceId. Чистые хелперы (имена,
// referenceId, talkMetaData, папка, срок загрузки) — в files_helpers.go.
//
// Безопасность — как во всём пакете (шапка client.go): креды только в
// заголовке Authorization (собирают transport.DoDAV/DoOCS), ошибки WebDAV
// проходят transport.SanitizeErr, dump запросов/ответов запрещён.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/stas-bool/nctalk-cli/internal/transport"
)

const (
	// pathCloudUser — текущий пользователь (§3.1): ocs.data.id = uid (≠ логин).
	pathCloudUser = "/ocs/v2.php/cloud/user"
	// pathCapabilities — capabilities сервера (§3.2): spreed.config.attachments.
	pathCapabilities = "/ocs/v2.php/cloud/capabilities"
	// pathShares — OCS-шаринг (§3.5).
	pathShares = "/ocs/v2.php/apps/files_sharing/api/v1/shares"
	// pathDAVFiles — база WebDAV файлов пользователя (§3.4): + "/" + uid.
	pathDAVFiles = "/remote.php/dav/files"
	// shareTypeRoom — тип шаринга «в комнату Talk» (IShare::TYPE_ROOM).
	shareTypeRoom = 10
	// lookupWindow — окно поиска id: одна страница из 50 самых свежих
	// сообщений (§3.6, осознанный компромисс).
	lookupWindow = 50
	// maxNameCandidates — потолок подбора имени (§3.4): защита от
	// зацикливания на неисправном сервере.
	maxNameCandidates = 1000
)

// FileUpload — загружаемый файл. Body перематывается (Seek 0) перед каждой
// попыткой PUT: повторы после 404/409/412.
type FileUpload struct {
	Name string // имя на сервере (filepath.Base локального пути)
	Size int64
	Body io.ReadSeeker
}

// SendFileOpts — параметры SendFile (§5). ReplyTo/ThreadId/ThreadTitle
// взаимоисключаемы (дубль cli-guard-а в validateSendFile).
type SendFileOpts struct {
	Caption     string // пусто → без подписи
	ReplyTo     int
	ThreadId    int
	ThreadTitle string
	Silent      bool
	ReferenceId string // пусто → генерируется
}

// SendFileResult — итог SendFile.
type SendFileResult struct {
	MessageId int    // 0 — сообщение не найдено (файл отправлен), см. LookupErr
	Path      string // итоговый путь на сервере, напр. "/Talk/report (2).pdf"
	LookupErr error  // причина, если MessageId == 0
}

// SendFile загружает файл в папку вложений и публикует его в комнату token
// (спека-дельта 2026-10-09 §5). Шаги:
//
//  1. валидация до сети (дубль cli-guard-ов, прецедент EditMessage);
//  2. uid (§3.1) и настройки вложений (§3.2) — ДО pre-check: при
//     allowed=false файл не отправить ни с какими аргументами, и эта причина
//     не должна маскироваться ошибкой аргумента («тред не найден»);
//  3. pre-check (§3.3): ThreadId → тред существует; ReplyTo → сообщение есть
//     и replyable. Для файлов сервер эти параметры МОЛЧА игнорирует (§1);
//  4. подбор имени + PUT (+ MKCOL при 404/409) (§3.4);
//  5. шаринг (§3.5); при любой его ошибке — cleanup DELETE файла;
//  6. поиск id по referenceId (§3.6): промах или ошибка поиска — НЕ ошибка
//     (файл уже отправлен): MessageId 0 + LookupErr, error == nil.
//
// «Не найдено» на шагах 3 и 5 — *OCSError{Code: 404} (cli даёт exit 2 через
// существующий exitFromClientErr); прочее — обычные ошибки (exit 1).
func (c *TalkClient) SendFile(ctx context.Context, token string, f FileUpload, opts SendFileOpts) (SendFileResult, error) {
	// 1. Валидация и подготовка — без сети.
	if err := validateSendFile(f, opts); err != nil {
		return SendFileResult{}, err
	}
	// referenceId уходит серверу как задан (нормализует сервер); пустой после
	// нормализации — генерируется (§2.4).
	ref := opts.ReferenceId
	if normalizeReferenceId(ref) == "" {
		gen, err := generateReferenceId()
		if err != nil {
			return SendFileResult{}, err
		}
		ref = gen
	}
	meta, err := encodeTalkMetaData(talkMetaData{
		Caption:     opts.Caption,
		ReplyTo:     opts.ReplyTo,
		ThreadId:    opts.ThreadId,
		ThreadTitle: opts.ThreadTitle,
		Silent:      opts.Silent,
	})
	if err != nil {
		return SendFileResult{}, err
	}

	// 2. uid и настройки вложений.
	uid, err := c.currentUserId(ctx)
	if err != nil {
		return SendFileResult{}, err
	}
	folder, err := c.attachmentFolder(ctx)
	if err != nil {
		return SendFileResult{}, err
	}

	// 3. Pre-check — до загрузки (неверный аргумент не должен оставить файл).
	if opts.ThreadId > 0 {
		if err := c.checkThreadExists(ctx, token, opts.ThreadId); err != nil {
			return SendFileResult{}, err
		}
	}
	if opts.ReplyTo > 0 {
		if err := c.checkReplyable(ctx, token, opts.ReplyTo); err != nil {
			return SendFileResult{}, err
		}
	}

	// 4. Подбор имени и загрузка.
	sharePath, err := c.uploadFile(ctx, uid, folder, f)
	if err != nil {
		return SendFileResult{}, err
	}

	// 5. Публикация; при ошибке — best-effort cleanup тем же ctx (§3.5).
	if err := c.shareToRoom(ctx, token, sharePath, ref, meta); err != nil {
		if derr := c.davDelete(ctx, uid, sharePath); derr != nil {
			return SendFileResult{}, fmt.Errorf("%w; загруженный файл %s не удалён (%v) — удалите вручную", err, sharePath, derr)
		}
		return SendFileResult{}, err
	}

	// 6. Поиск id — промах не ошибка (§2.5).
	id, lerr := c.findFileMessage(ctx, token, normalizeReferenceId(ref))
	return SendFileResult{MessageId: id, Path: sharePath, LookupErr: lerr}, nil
}

// validateSendFile — клиентская валидация SendFile до сети (§5 шаг 1). Имя —
// один сегмент пути: пустое, «.», «..» и с «/» отвергаются (isPlainSegment —
// защитное расширение §5: path.Join схлопнул бы «.»/«..»).
func validateSendFile(f FileUpload, opts SendFileOpts) error {
	if !isPlainSegment(f.Name) {
		return fmt.Errorf("client: SendFile: недопустимое имя файла %q (пустое, «.», «..» или содержит «/»)", f.Name)
	}
	if f.Body == nil {
		return errors.New("client: SendFile: нет содержимого файла (FileUpload.Body == nil)")
	}
	if f.Size < 0 {
		return fmt.Errorf("client: SendFile: отрицательный размер файла %d", f.Size)
	}
	if opts.ReplyTo < 0 || opts.ThreadId < 0 {
		return fmt.Errorf("client: SendFile: id ожидает положительное число (replyTo=%d, threadId=%d)", opts.ReplyTo, opts.ThreadId)
	}
	n := 0
	if opts.ReplyTo > 0 {
		n++
	}
	if opts.ThreadId > 0 {
		n++
	}
	if opts.ThreadTitle != "" {
		n++
	}
	if n > 1 {
		return errors.New("client: SendFile: ReplyTo, ThreadId и ThreadTitle взаимоисключаемы — задайте не больше одного")
	}
	return nil
}

// cloudUser — минимальная форма ocs.data ответа cloud/user: нужен только uid.
type cloudUser struct {
	Id string `json:"id"`
}

// currentUserId — uid текущего пользователя (§3.1): путь WebDAV строится по
// uid, а он ≠ логину (на боевом логин — e-mail). Пустой или не-сегментный id
// — drift формата, ошибка.
func (c *TalkClient) currentUserId(ctx context.Context) (string, error) {
	var u cloudUser
	if _, err := c.doOCS(ctx, http.MethodGet, pathCloudUser, nil, nil, false, &u); err != nil {
		return "", err
	}
	if !isPlainSegment(u.Id) {
		return "", fmt.Errorf("client: SendFile: cloud/user вернул недопустимый id %q (drift формата)", u.Id)
	}
	return u.Id, nil
}

// capabilitiesResp — минимальная форма ocs.data capabilities (§3.2). Spreed —
// указатель: nil = Talk на сервере не установлен/выключен.
type capabilitiesResp struct {
	Capabilities struct {
		Spreed *struct {
			Config struct {
				Attachments struct {
					Allowed bool   `json:"allowed"`
					Folder  string `json:"folder"`
				} `json:"attachments"`
			} `json:"config"`
		} `json:"spreed"`
	} `json:"capabilities"`
}

// attachmentFolder — папка вложений из capabilities (§3.2): нет spreed →
// ошибка; allowed != true → ошибка; folder пуст → /Talk.
func (c *TalkClient) attachmentFolder(ctx context.Context) (string, error) {
	var caps capabilitiesResp
	if _, err := c.doOCS(ctx, http.MethodGet, pathCapabilities, nil, nil, false, &caps); err != nil {
		return "", err
	}
	sp := caps.Capabilities.Spreed
	if sp == nil {
		return "", errors.New("client: SendFile: Talk недоступен на сервере (нет spreed в capabilities)")
	}
	if !sp.Config.Attachments.Allowed {
		return "", errors.New("client: SendFile: вложения запрещены на сервере (spreed.config.attachments.allowed != true)")
	}
	return normalizeAttachmentFolder(sp.Config.Attachments.Folder), nil
}

// checkThreadExists — pre-check треда (§3.3): GET chat/{token}/threads/{id},
// тело 200 не разбирается. 404 (нет треда или комнаты) → *OCSError{404} с
// понятным текстом.
func (c *TalkClient) checkThreadExists(ctx context.Context, token string, threadId int) error {
	// Token в путь КАК ЕСТЬ — эскейп сегмента делает транспорт (инвариант пакета).
	p := pathChat + "/" + token + "/threads/" + strconv.Itoa(threadId)
	if _, err := c.doOCS(ctx, http.MethodGet, p, nil, nil, false, nil); err != nil {
		var oe *OCSError
		if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
			return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: тред %d не найден (или комната недоступна)", threadId)}
		}
		return err
	}
	return nil
}

// checkReplyable — pre-check ответа (§3.3): GET chat/{token}/{id}/context
// ?limit=0. Сервер отдаёт БЛИЖАЙШЕЕ сообщение с id <= запрошенного — для
// несуществующего id это сосед, поэтому id сверяется. Нет совпадения, пустой
// массив или 304 → *OCSError{404}; совпало, но isReplyable=false — обычная
// ошибка (exit 1).
func (c *TalkClient) checkReplyable(ctx context.Context, token string, messageId int) error {
	p := pathChat + "/" + token + "/" + strconv.Itoa(messageId) + "/context"
	q := url.Values{}
	q.Set("limit", "0")
	var msgs []Message
	if _, err := c.doOCS(ctx, http.MethodGet, p, q, nil, false, &msgs); err != nil {
		var oe *OCSError
		if errors.As(err, &oe) && oe.Code == http.StatusNotFound {
			return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: сообщение %d не найдено (или комната недоступна)", messageId)}
		}
		return err
	}
	for _, m := range msgs {
		if m.Id != messageId {
			continue
		}
		if !m.IsReplyable {
			return fmt.Errorf("client: SendFile: на сообщение %d нельзя ответить (isReplyable=false)", messageId)
		}
		return nil
	}
	return &OCSError{Code: http.StatusNotFound, Message: fmt.Sprintf("SendFile: сообщение %d не найдено", messageId)}
}

// uploadFile подбирает свободное имя и загружает файл (§3.4); возвращает путь
// файла относительно корня пользователя (с ведущим «/») — он же path шаринга.
//
// Кандидаты — по правилу веб-клиента (nameCandidate): HEAD 200 → занято,
// следующий; 404 → PUT с If-None-Match: * (существующее не перезаписывается
// никогда). PUT 412 — имя заняли между HEAD и PUT → следующий кандидат; 404
// (живой сервер) или 409 (голый Sabre) — нет папки вложений (классический путь
// её не создаёт) → MKCOL и один повтор.
func (c *TalkClient) uploadFile(ctx context.Context, uid, folder string, f FileUpload) (string, error) {
	mkcolTried := false
	for i := 0; i < maxNameCandidates; i++ {
		sharePath := path.Join(folder, nameCandidate(f.Name, i))
		davPath := davFilesPath(uid, sharePath)

		head, err := transport.DoDAV(ctx, c.doer, c.auth, http.MethodHead, davPath, nil, nil, 0)
		if err != nil {
			return "", err
		}
		switch head.StatusCode {
		case http.StatusOK:
			continue // имя занято
		case http.StatusNotFound:
			// свободно — загружаем
		default:
			return "", davStatusErr(http.MethodHead, sharePath, head)
		}

		status, err := c.davPut(ctx, davPath, sharePath, f)
		if err != nil {
			return "", err
		}
		if (status == http.StatusNotFound || status == http.StatusConflict) && !mkcolTried {
			mkcolTried = true
			if err := c.davMkcol(ctx, uid, folder); err != nil {
				return "", err
			}
			if status, err = c.davPut(ctx, davPath, sharePath, f); err != nil {
				return "", err
			}
		}
		switch status {
		case http.StatusCreated, http.StatusNoContent:
			return sharePath, nil
		case http.StatusPreconditionFailed:
			continue // гонка: имя заняли между HEAD и PUT
		default: // 404/409 после MKCOL
			return "", fmt.Errorf("client: SendFile: папка вложений %s недоступна (PUT: HTTP %d)", folder, status)
		}
	}
	return "", fmt.Errorf("client: SendFile: не удалось подобрать свободное имя для %q (проверено %d кандидатов)", f.Name, maxNameCandidates)
}

// davPut — одна попытка PUT (§3.4, §4.2): тело перематывается, срок — по
// размеру файла (uploadTimeout) через контекст, транспорт — uploadDoer без
// общего таймаута. Статусы 201/204/404/409/412 возвращаются вызывающему (их смысл
// знает цикл подбора имени), прочие — ошибка с s:message.
func (c *TalkClient) davPut(ctx context.Context, davPath, sharePath string, f FileUpload) (int, error) {
	if _, err := f.Body.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("client: SendFile: не удалось перемотать файл перед загрузкой: %w", err)
	}
	h := http.Header{}
	h.Set("If-None-Match", "*")
	h.Set("Content-Type", "application/octet-stream")
	putCtx := ctx
	if d := uploadTimeout(c.cfg.Timeout, f.Size); d > 0 {
		var cancel context.CancelFunc
		putCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	resp, err := transport.DoDAV(putCtx, c.uploadDoer, c.auth, http.MethodPut, davPath, h, f.Body, f.Size)
	if err != nil {
		return 0, err
	}
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed:
		return resp.StatusCode, nil
	}
	return 0, davStatusErr(http.MethodPut, sharePath, resp)
}

// davMkcol создаёт папку вложений (§3.4): 201 — создана, 405 — уже есть
// (гонка) → ок; прочее (в т.ч. 409 — нет родителя у вложенной папки) —
// «папка вложений недоступна».
func (c *TalkClient) davMkcol(ctx context.Context, uid, folder string) error {
	resp, err := transport.DoDAV(ctx, c.doer, c.auth, "MKCOL", davFilesPath(uid, folder), nil, nil, 0)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMethodNotAllowed:
		return nil
	}
	return fmt.Errorf("client: SendFile: папка вложений %s недоступна (MKCOL: HTTP %d%s)", folder, resp.StatusCode, davMessageSuffix(resp))
}

// davDelete удаляет загруженный файл (cleanup после неудачного шаринга,
// §3.5): любой 2xx или 404 (файла уже нет) — успех.
func (c *TalkClient) davDelete(ctx context.Context, uid, sharePath string) error {
	resp, err := transport.DoDAV(ctx, c.doer, c.auth, http.MethodDelete, davFilesPath(uid, sharePath), nil, nil, 0)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return davStatusErr(http.MethodDelete, sharePath, resp)
}

// davFilesPath — путь WebDAV для пути userPath (с ведущим «/») в хранилище
// uid. Сегменты НЕэкранированы — экранирует transport.DoDAV.
func davFilesPath(uid, userPath string) string {
	return pathDAVFiles + "/" + uid + userPath
}

// davStatusErr — ошибка WebDAV-статуса: метод, путь, HTTP-код, подсказка и
// s:message Sabre (§3.4).
func davStatusErr(method, userPath string, resp *transport.DAVResponse) error {
	return fmt.Errorf("client: SendFile: %s %s: HTTP %d%s%s", method, userPath, resp.StatusCode, davStatusHint(resp.StatusCode), davMessageSuffix(resp))
}

// davStatusHint — человекочитаемая подсказка к частым отказам WebDAV.
func davStatusHint(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return " (нет авторизации — проверьте NEXTCLOUD_LOGIN/NEXTCLOUD_PASS)"
	case http.StatusForbidden:
		return " (нет прав на запись)"
	case http.StatusRequestEntityTooLarge:
		return " (файл больше лимита сервера или прокси)"
	case http.StatusLocked:
		return " (файл заблокирован)"
	case http.StatusInsufficientStorage:
		return " (недостаточно места в хранилище)"
	}
	return ""
}

// davMessageSuffix — «: <s:message>» или пусто.
func davMessageSuffix(resp *transport.DAVResponse) string {
	if resp.Message == "" {
		return ""
	}
	return ": " + resp.Message
}

// shareReq — JSON-тело шаринга (§3.5). talkMetaData — JSON-СТРОКА.
type shareReq struct {
	ShareType    int    `json:"shareType"`
	ShareWith    string `json:"shareWith"`
	Path         string `json:"path"`
	ReferenceId  string `json:"referenceId"`
	TalkMetaData string `json:"talkMetaData"`
}

// shareToRoom публикует загруженный файл в комнату (§3.5). Тело ответа
// (share-объект) не разбирается — проверяется только OCS-статус; 404
// «Conversation not found» (нет комнаты / не участник / нет права писать) →
// *OCSError{404}.
func (c *TalkClient) shareToRoom(ctx context.Context, token, sharePath, ref, meta string) error {
	body, err := json.Marshal(shareReq{
		ShareType:    shareTypeRoom,
		ShareWith:    token,
		Path:         sharePath,
		ReferenceId:  ref,
		TalkMetaData: meta,
	})
	if err != nil {
		return fmt.Errorf("client: SendFile: не удалось собрать тело шаринга: %w", err)
	}
	_, err = c.doOCS(ctx, http.MethodPost, pathShares, nil, bytes.NewReader(body), true, nil)
	return err
}

// findFileMessage ищет id сообщения с файлом (§3.6): одна страница GetChat
// (lookupWindow), кандидаты — referenceId == ref и messageType != "system"
// (thread_created того же запроса несёт тот же referenceId), из них —
// наибольший id (не зависит от порядка выдачи). ref — уже нормализованный.
func (c *TalkClient) findFileMessage(ctx context.Context, token, ref string) (int, error) {
	msgs, err := c.GetChat(ctx, token, GetChatOpts{Limit: lookupWindow})
	if err != nil {
		return 0, err
	}
	best := 0
	for _, m := range msgs {
		if m.ReferenceId == ref && m.MessageType != "system" && m.Id > best {
			best = m.Id
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("сообщение с referenceId %s не найдено среди %d последних сообщений комнаты", ref, len(msgs))
	}
	return best, nil
}
