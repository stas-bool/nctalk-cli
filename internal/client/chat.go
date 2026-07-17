package client

// chat.go — чтение и отправка сообщений комнаты (Task 2.5 `chat show`,
// Task 2.6 `chat send`; спека §6, §12). Содержит пагинированный GetChat
// (limit=200 + перебор lastKnownMessageId назад) с ранним стопом по timestamp,
// query-aware transport doOCSGet и SendMessage (POST chat send).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// pathChat — канонический путь chat-эндпоинта (спека §6, §12). Локальная
// константа: paths.go намеренно не расширяется в этой задаче, чтобы не
// конфликтовать с параллельными Task 2.4 (search) и Task 2.7 (reactions).
const pathChat = "/ocs/v2.php/apps/spreed/api/v1/chat"

// chatPageSize — размер страницы выборки (серверный limit=). Тянем максимум
// разрешённого спекой — по 200 сообщений за запрос.
const chatPageSize = 200

// GetChatOpts — параметры GetChat.
type GetChatOpts struct {
	// Limit — потолок ВЫБОРКИ (не путать с серверным limit=, который всегда
	// равен chatPageSize, и с cli-флагом --last). <=0 → без потолка: тянем все
	// доступные страницы, пока не достигнем начала чата.
	Limit int
	// LastKnownMessageId — точка старта перебора назад: 0 = с самого свежего
	// сообщения; иначе сервер отдаёт сообщения строго старше этого id.
	LastKnownMessageId int
	// StopBeforeTs — ранний стоп (секунды Unix): при первом сообщении с
	// Timestamp СТРОГО меньше StopBeforeTs обрезаем выборку (само сообщение и
	// всё старше НЕ включаем) и прекращаем пагинацию. 0 = без раннего стопа.
	StopBeforeTs int64
}

// GetChat возвращает сообщения комнаты token, упорядоченные от новых к старым
// (спека §6 `chat show`, §12).
//
// Пагинация: страницы по chatPageSize (200) сообщений, перебор
// lastKnownMessageId назад (lookIntoFuture=0). Следующая страница тянется с
// lastKnownMessageId = id самого старого сообщения предыдущей страницы.
//
// Цикл продолжается, пока (а) предыдущая страница вернула ровно chatPageSize
// и (б) не достигнут потолок opts.Limit и (в) не сработал ранний стоп
// opts.StopBeforeTs. Пустая страница = достигли начала чата — выходим без
// ошибки (и не зацикливаемся).
//
// opts.Limit — потолок ВЫБОРКИ, а не серверный limit=: серверу всегда шлётся
// limit=200, излишек обрезается на клиенте после накопления.
func (c *TalkClient) GetChat(ctx context.Context, token string, opts GetChatOpts) ([]Message, error) {
	result := make([]Message, 0)
	lastKnown := opts.LastKnownMessageId
	for {
		page, err := c.getChatPage(ctx, token, lastKnown)
		if err != nil {
			return nil, err
		}
		// Пустая страница — достигли начала чата (или чат пустой). Без этой
		// проверки пустой чат зациклился бы на повторных запросах.
		if len(page) == 0 {
			break
		}

		// Ранний стоп: режем страницу по первому сообщению строго старше
		// opts.StopBeforeTs. Сообщения упорядочены от новых к старым, поэтому
		// найдя первое «слишком старое», всё после него тоже отбрасываем.
		if opts.StopBeforeTs != 0 {
			cut := -1
			for i := range page {
				if page[i].Timestamp < opts.StopBeforeTs {
					cut = i
					break
				}
			}
			if cut >= 0 {
				result = append(result, page[:cut]...)
				break // ранний стоп сработал — пагинацию прекращаем
			}
		}

		result = append(result, page...)
		lastKnown = page[len(page)-1].Id // самое старое в странице

		// Неполная страница — достигли начала чата.
		if len(page) < chatPageSize {
			break
		}
		// Потолок выборки достигнут — больше не тянем.
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}

	// Применяем потолок выборки (сервер отдаёт страницами по 200, поэтому
	// накопленное может превышать opts.Limit).
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// getChatPage выполняет один GET-запрос страницы chat-истории и декодирует
// OCS-конверт в []Message. query: limit=chatPageSize, lookIntoFuture=0,
// lastKnownMessageId (только если lastKnown > 0 — иначе сервер отдаёт самое
// свежее).
func (c *TalkClient) getChatPage(ctx context.Context, token string, lastKnown int) ([]Message, error) {
	p := pathChat + "/" + url.PathEscape(token)
	q := url.Values{}
	q.Set("limit", strconv.Itoa(chatPageSize))
	q.Set("lookIntoFuture", "0")
	if lastKnown > 0 {
		q.Set("lastKnownMessageId", strconv.Itoa(lastKnown))
	}
	var out []Message
	if err := c.doOCSGet(ctx, p, q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Message{}
	}
	return out, nil
}

// doOCSGet — query-aware вариант doOCS для GET-запросов к OCS-эндпоинтам.
//
// doOCS принимает только путь и собирает URL через path.Join, который
// экранирует '?' в '%3F' (query отрезается). Для chat-эндпоинта нужны
// query-параметры (limit/lookIntoFuture/lastKnownMessageId), поэтому здесь
// URL собирается с RawQuery отдельно.
//
// Контракт ошибок, заголовки и sanitize-политика — идентичны doOCS (см.
// client.go). Не вынесено в client.go, чтобы не конфликтовать с параллельными
// задачами Task 2.4/2.7; при необходимости рефакторится в общий хелпер позже.
func (c *TalkClient) doOCSGet(ctx context.Context, p string, q url.Values, out any) error {
	u := *c.baseURL
	joined := path.Join(c.baseURL.Path, p)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	u.Path = joined
	u.RawPath = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sanitizeErr(err)
	}
	// Authorization собирается на каждый запрос заново (не кэшируется) — тот же
	// контракт безопасности, что в doOCS (спека §5).
	creds := c.cfg.Login + ":" + c.cfg.Password
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")

	resp, err := c.doer.Do(req)
	if err != nil {
		// При redirect-ошибке http.Client может вернуть предыдущий Response с
		// уже закрытым Body — закрываем идемпотентно.
		if resp != nil {
			_ = resp.Body.Close()
		}
		return sanitizeErr(err)
	}
	defer resp.Body.Close()

	// Декодирование в две фазы (как в doOCS): сначала meta + RawMessage, при
	// успехе — распаковка data в out.
	var env OCSEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return sanitizeErr(fmt.Errorf("client: не удалось декодировать OCS-ответ: %w", err))
	}
	if env.OCS.Meta.StatusCode >= 400 {
		msg := env.OCS.Meta.Message
		if msg == "" {
			msg = fmt.Sprintf("OCS statusCode=%d", env.OCS.Meta.StatusCode)
		}
		return errors.New("client: " + msg)
	}
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return sanitizeErr(fmt.Errorf("client: не удалось распаковать ocs.data: %w", err))
		}
	}
	return nil
}

// SendMessageOpts — параметры SendMessage (спека §6 `chat send`).
type SendMessageOpts struct {
	// Message — текст сообщения, ОБЯЗАТЕЛЬНЫЙ. Пустая строка → клиентская
	// ошибка ДО сетевого вызова (не тратим запрос).
	Message string
	// ReplyTo — id сообщения, на которое это является ответом; 0 = не ответ
	// (поле replyTo в JSON не попадает через omitempty).
	ReplyTo int
	// Silent — отправить без уведомления получателей. Всегда сериализуется в
	// JSON (явный true/false) — единообразно с серверным контрактом.
	Silent bool
	// ReferenceId — опц. клиентский UUID для client-side dedup (спека §6).
	// Пустая строка → поле не попадает в JSON (omitempty).
	ReferenceId string
}

// sendMessageReq — форма JSON-тела запроса chat send (спека §6).
//
// Теги omitempty регулируют условное включение полей:
//   - message — всегда (обязательное);
//   - replyTo — только при >0 (omitempty для int пропускает 0);
//   - silent — ВСЕГДА (без omitempty): сервер получает явный true/false;
//   - referenceId — только при непустом (omitempty для string пропускает "").
type sendMessageReq struct {
	Message     string `json:"message"`
	ReplyTo     int    `json:"replyTo,omitempty"`
	Silent      bool   `json:"silent"`
	ReferenceId string `json:"referenceId,omitempty"`
}

// sendMessageResp — минимальная форма ocs.data ответа chat send: нужно только
// id нового сообщения. Сервер возвращает полный объект Message, но мы
// игнорируем лишние поля — десериализация в подмножество структур безопасна.
type sendMessageResp struct {
	Id int `json:"id"`
}

// SendMessage отправляет сообщение в комнату token и возвращает id нового
// сообщения (спека §6 `chat send`, §12).
//
// Запрос: POST pathChat/{token}, Content-Type: application/json (mutate=true в
// doOCS добавляет заголовок), тело — sendMessageReq.
// Ответ: ocs.data.id → int.
//
// Контракт:
//   - пустой opts.Message → клиентская ошибка ДО сетевого вызова;
//   - при OCS-error (meta.statusCode >= 400, например невалидный replyTo)
//     возвращается ошибка с meta.message — стандартная обработка doOCS (§9).
func (c *TalkClient) SendMessage(ctx context.Context, token string, opts SendMessageOpts) (int, error) {
	// Клиентская валидация: пустое сообщение не имеет смысла отправлять —
	// отказываем сразу, не дёргая сеть.
	if opts.Message == "" {
		return 0, errors.New("client: SendMessage: пустое сообщение (opts.Message обязательно)")
	}
	body, err := json.Marshal(sendMessageReq{
		Message:     opts.Message,
		ReplyTo:     opts.ReplyTo,
		Silent:      opts.Silent,
		ReferenceId: opts.ReferenceId,
	})
	if err != nil {
		// json.Marshal на этой структуре практически не может упасть, но на
		// всякий случай пропускаем через sanitizeErr.
		return 0, sanitizeErr(fmt.Errorf("client: не удалось собрать тело SendMessage: %w", err))
	}
	// pathChat — локальная константа без trailing '/'; token эскейпим как
	// path-сегмент через url.PathEscape. doOCS соберёт полный URL через
	// path.Join с baseURL.Path.
	p := pathChat + "/" + url.PathEscape(token)
	var out sendMessageResp
	if err := c.doOCS(ctx, http.MethodPost, p, bytes.NewReader(body), true, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}
