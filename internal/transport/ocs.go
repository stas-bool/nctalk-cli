package transport

import "fmt"

// OCSError — ошибка на уровне OCS-конверта: сервер вернул HTTP 200, но внутри
// ocs.meta.statusCode >= 400 (Nextcloud так сигнализирует бизнес-ошибки —
// комната/сообщение/реакция не найдены, нет прав, невалидный replyTo и т.п.).
//
// Code — это ИМЕННО ocs.meta.statusCode (НЕ HTTP-статус ответа): например 404
// для «не найдено», 401/403 — auth/доступ, 400 — невалидный ввод. Слой cli
// различает 404 → exit 2 (NotFound) от остальных → exit 1 (Generic) по этому
// полю (см. cli.exitFromClientErr, спека §7/§9).
//
// Тип ведёт себя как обычная ошибка (реализует interface error и поддерживает
// errors.As/Is через сравнение указателя), но дополнительно несёт структурирован-
// ный код, который нужен вышележащему слою для выбора exit-кода.
//
// Принадлежность транспорту корректна: OCS — соглашение Nextcloud об ошибках
// на уровне HTTP-API, конверт сам по себе доменно-нейтрален. Переехал из
// internal/client/ocs.go без изменений логики (спека 2026-07-19 §3).
type OCSError struct {
	// Code — ocs.meta.statusCode (404, 401, 403, 400, 5xx-подобные …).
	Code int
	// Message — ocs.meta.message; человекочитаемый текст сервера. Пустая строка
	// заменяется в Error() на каноническую форму «OCS statusCode=<Code>», чтобы
	// не терять код в выводе, если сервер прислал пустое message.
	Message string
}

// Error возвращает текст ошибки с префиксом «client: » (единообразно с прочими
// ошибками пакета-прародителя; префикс сохранён для обратной совместимости
// текстов существующих тестов и сообщений). Message пусто → каноническая форма
// по Code. Nil-приёмник безопасен.
func (e *OCSError) Error() string {
	if e == nil {
		return "client: OCS error"
	}
	if e.Message != "" {
		return "client: " + e.Message
	}
	return fmt.Sprintf("client: OCS statusCode=%d", e.Code)
}

// OCSEnvelope — стандартный конверт ответа OCS Nextcloud (спека §6).
// Параметр-тип T определяет, во что распаковывается поле ocs.data; это даёт
// переиспользование одной обёртки для всех эндпоинтов.
//
// Внутри DoOCS используется как OCSEnvelope[json.RawMessage], чтобы сначала
// проверить meta.statusCode и только при успехе распаковать data в целевой
// тип — это избегает двойного декодирования и ошибок типизации при ошибках API.
type OCSEnvelope[T any] struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statusCode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data T `json:"data"`
	} `json:"ocs"`
}
