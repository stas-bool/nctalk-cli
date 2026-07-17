package client

// Message — каноническое представление сообщения Nextcloud Talk (спека §12).
//
// ЕДИНОЕ определение этого типа в пакете client: используется и в
// Room.LastMessage (Task 2.2), и в GetChat (Task 2.5). Переопределять в других
// задачах НЕЛЬЗЯ — иначе compile error «type redeclared».
type Message struct {
	Id                  int                 `json:"id"`
	ActorType           string              `json:"actorType"`
	ActorId             string              `json:"actorId"`
	ActorDisplayName    string              `json:"actorDisplayName"`
	MessageType         string              `json:"messageType"`       // "comment" / "system" / ...
	SystemMessage       string              `json:"systemMessage"`
	Message             string              `json:"message"`           // с плейсхолдерами {file}/{actor}/{mention-*}
	MessageParameters   map[string]MsgParam `json:"messageParameters"` // key = имя плейсхолдера
	Reactions           map[string]int      `json:"reactions"`         // emoji → count
	ReferenceId         string              `json:"referenceId"`
	Timestamp           int64               `json:"timestamp"`         // СЕКУНДЫ Unix
	IsReplyable         bool                `json:"isReplyable"`
	Markdown            bool                `json:"markdown"`
	ThreadId            int                 `json:"threadId"`
	ExpirationTimestamp int                 `json:"expirationTimestamp"`
	Token               string              `json:"token"`
}

// MsgParam — параметр сообщения (спека §12). Ключ в Message.MessageParameters —
// имя плейсхолдера (например "file", "actor", "mention-user1").
type MsgParam struct {
	Type string `json:"type"` // "file"/"user"/...
	Id   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Link string `json:"link,omitempty"`
}
