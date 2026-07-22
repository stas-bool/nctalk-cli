// internal/call/interactive/view.go — типы Event/CallState/View, точка расширения
// bubbletea (future §13). Без терминал-специфики: ни ANSI, ни tea.*.
package interactive

// Event — TUI-событие (хоткей). TUI пушит ввод (M/Q/+/-) в Events().
type Event int

const (
	EvMuteToggle Event = iota
	EvLeave
	EvVolUp
	EvVolDown
)

// ParticipantState — один участник в enriched-снапшоте для View.
// В interactive.CallState (НЕ agent.CallState — тот без SelfMuted/Volume/Speaking).
type ParticipantState struct {
	SessionId string
	Name      string // MVP = ActorId (из agent), display → future §13
	Level     int    // 0..100, сырой из agent (RMS)
	Speaking  bool   // гистерезис в interactive (не в agent)
}

// CallState — enriched-снапшот для View. interactive обогащает agent.CallState
// своим SelfMuted/Volume + копией Status + Speaking-гистерезисом.
type CallState struct {
	Participants []ParticipantState
	SelfMuted    bool
	Volume       int    // %, current gain volumeWriter
	Status       string // копия из agent.CallState.Status
}

// View — точка расширения. ansiView (сейчас) и bubbleteaView (future §13) —
// сменные реализации. Update пушит состояние (в горутине отрисовки),
// Events возвращает канал ввода (M/Q/+/-), Close восстанавливает tty.
type View interface {
	Update(state CallState)
	Events() <-chan Event
	Close() error
}
