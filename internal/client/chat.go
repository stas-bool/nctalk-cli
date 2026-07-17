package client

// chat.go — чтение и отправка сообщений комнаты (Task 2.5 `chat show`,
// Task 2.6 `chat send`; спека §6, §12). Содержит пагинированный GetChat
// (перебор lastKnownMessageId назад, курсор — из заголовка X-Chat-Last-Given)
// с ранним стопом по timestamp и SendMessage (POST chat send).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// pathChat — канонический путь chat-эндпоинта (спека §6, §12). Локальная
// константа: paths.go намеренно не расширяется в этой задаче, чтобы не
// конфликтовать с параллельными Task 2.4 (search) и Task 2.7 (reactions).
const pathChat = "/ocs/v2.php/apps/spreed/api/v1/chat"

// chatPageSize — потолок размера страницы выборки (серверный limit= принимает
// 1..200). Фактический serverLimit ниже уменьшается до opts.Limit, когда она
// меньше 200 — чтобы в самом частом сценарии (chat show, дефолт 20) не тянуть
// по сети 200 сообщений и выкидывать 180.
const chatPageSize = 200

// headerChatLastGiven — заголовок ответа chat-эндпоинта: id самого старого
// сообщения в отданной странице (документация Nextcloud Talk). Это КАНОНИЧЕСКИЙ
// курсор пагинации назад: его значение (а не id из тела) нужно передавать как
// lastKnownMessageId следующего запроса. Не зависит от порядка сообщений в
// теле ответа.
const headerChatLastGiven = "X-Chat-Last-Given"

// GetChatOpts — параметры GetChat.
type GetChatOpts struct {
	// Limit — потолок ВЫБОРКИ (не путать с cli-флагом --last). <=0 → без потолка:
	// тянем все доступные страницы, пока не достигнем начала чата.
	Limit int
	// LastKnownMessageId — точка старта перебора назад: 0 = с самого свежего
	// сообщения; иначе сервер отдаёт сообщения строго старше этого id.
	LastKnownMessageId int
	// StopBeforeTs — ранний стоп (секунды Unix): сообщения с Timestamp СТРОГО
	// меньше StopBeforeTs НЕ включаются в результат, а пагинация прекращается,
	// как только страница пересекает порог (пагинация идёт в прошлое, поэтому
	// следующие страницы только старше). 0 = без раннего стопа.
	StopBeforeTs int64
}

// GetChat возвращает сообщения комнаты token (спека §6 `chat show`, §12).
//
// Пагинация НЕ опирается на порядок сообщений в теле ответа:
//   - курсором служит заголовок X-Chat-Last-Given (id самого старого сообщения
//     страницы) — его подставляем как lastKnownMessageId следующего запроса;
//   - стоп — пустая страница, HTTP 304/204 (transport трактует как пусто),
//     пустой/отсутствующий заголовок, либо неизменившийся курсор (нет прогресса);
//   - потолок opts.Limit обрезает накопленный результат.
//
// serverLimit выбирается как min(opts.Limit, chatPageSize) при opts.Limit>0 —
// для частого chat show с потолком 20 сервер отдаст 20, а не 200 (экономия
// трафика). При opts.Limit=0 (без потолка) — chatPageSize=200 за запрос.
//
// Ранний стоп (StopBeforeTs) фильтрует страницу по timestamp без допущения о
// порядке: из страницы оставляются только сообщения с ts>=StopBeforeTs, а при
// обнаружении хотя бы одного «слишком старого» — пагинация прекращается
// (следующие страницы строго старше). Корректность --since дополнительно
// дублируется в CLI-слое.
func (c *TalkClient) GetChat(ctx context.Context, token string, opts GetChatOpts) ([]Message, error) {
	// serverLimit — сколько просим у сервера за один запрос.
	serverLimit := chatPageSize
	if opts.Limit > 0 && opts.Limit < chatPageSize {
		serverLimit = opts.Limit
	}

	result := make([]Message, 0)
	lastKnown := opts.LastKnownMessageId
	for {
		page, lastGiven, err := c.getChatPage(ctx, token, lastKnown, serverLimit)
		if err != nil {
			return nil, err
		}
		// Пустая страница (или 304/204, отданные transport-ом как пусто) —
		// достигли начала чата (или чат пуст). Без этой проверки пустой чат
		// зациклился бы на повторных запросах.
		if len(page) == 0 {
			break
		}

		// Ранний стоп по timestamp — БЕЗ допущения о порядке сообщений: оставляем
		// только сообщения страницы с ts>=StopBeforeTs; если хотя бы одно
		// «слишком старое» попалось — дальше пагинации нет (следующая страница
		// строго старше), прекращаем.
		if opts.StopBeforeTs != 0 {
			trimmed := page[:0]
			stopped := false
			for i := range page {
				if page[i].Timestamp < opts.StopBeforeTs {
					stopped = true
					continue
				}
				trimmed = append(trimmed, page[i])
			}
			result = append(result, trimmed...)
			if stopped {
				break
			}
		} else {
			result = append(result, page...)
		}

		// Потолок выборки достигнут — больше не тянем.
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
		// Неполная страница — достигли начала чата (страховка поверх заголовка).
		if len(page) < serverLimit {
			break
		}
		// Курсор следующей страницы — из заголовка X-Chat-Last-Given. Нет
		// заголовка или значение не поменялось с прошлого захода — стоп
		// (сервер сообщил об отсутствии более старых сообщений).
		if lastGiven == "" {
			break
		}
		next, parseErr := strconv.Atoi(lastGiven)
		if parseErr != nil || next <= 0 || next == lastKnown {
			break
		}
		lastKnown = next
	}

	// Применяем потолок выборки (сервер отдаёт страницами, поэтому накопленное
	// может превышать opts.Limit на величину до одной страницы).
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// getChatPage выполняет один GET-запрос страницы chat-истории и декодирует
// OCS-конверт в []Message. Возвращает также значение заголовка
// X-Chat-Last-Given (курсор следующей страницы, может быть пустым).
//
// query: limit=serverLimit, lookIntoFuture=0, lastKnownMessageId (только при
// lastKnown>0 — иначе сервер отдаёт самое свежее), setReadMarker=0 (читающий
// `chat show` НЕ должен молча помечать комнату прочитанной — «отметка
// прочитанным» за рамками MVP, спека §11).
func (c *TalkClient) getChatPage(ctx context.Context, token string, lastKnown, serverLimit int) ([]Message, string, error) {
	p := pathChat + "/" + url.PathEscape(token)
	q := url.Values{}
	q.Set("limit", strconv.Itoa(serverLimit))
	q.Set("lookIntoFuture", "0")
	// setReadMarker=0: chat show — это «подглянуть», не «прочитать». Дефолт
	// сервера =1 сбросил бы unread; явно гасим (спека §11 выносит отметку
	// прочитанным за рамки MVP).
	q.Set("setReadMarker", "0")
	if lastKnown > 0 {
		q.Set("lastKnownMessageId", strconv.Itoa(lastKnown))
	}
	var out []Message
	header, err := c.doOCS(ctx, http.MethodGet, p, q, nil, false, &out)
	if err != nil {
		return nil, "", err
	}
	if out == nil {
		out = []Message{}
	}
	return out, header.Get(headerChatLastGiven), nil
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
	if _, err := c.doOCS(ctx, http.MethodPost, p, nil, bytes.NewReader(body), true, &out); err != nil {
		return 0, err
	}
	return out.Id, nil
}
