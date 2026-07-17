package client

// OCSEnvelope — стандартный конверт ответа OCS Nextcloud (спека §6).
// Параметр-тип T определяет, во что распаковывается поле ocs.data; это даёт
// переиспользование одной обёртки для всех эндпоинтов.
//
// Внутри doOCS используется как OCSEnvelope[json.RawMessage], чтобы сначала
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
