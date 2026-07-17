package client

// Именованные канонические пути эндпоинтов Nextcloud Talk (спека §6/§12).
// Все методы клиента ссылаются на эти константы вместо жёстко зашитых строк —
// правки сконцентрированы в одном месте.
const (
	// pathRooms — ListRooms (Task 2.2); v4 — канонический путь spreed API.
	pathRooms = "/ocs/v2.php/apps/spreed/api/v4/room"
	// pathChat, pathReaction, pathSearchRooms, pathSearchMessages
	// добавляются по мере реализации соответствующих задач
	// (Task 2.4 / 2.5 / 2.7 / 2.8). НЕ добавлять здесь заранее.
)
