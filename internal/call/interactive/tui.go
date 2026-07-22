// internal/call/interactive/tui.go — ANSI-реализация View на golang.org/x/term.
// Спека 2026-07-21 §6. Review #9 (graceful non-tty fallback), #11 (defer Close).
package interactive

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/term"
)

// ansiView — реализация View через raw-mode + alt-screen + ANSI-отрисовку.
// В non-tty окружении (pipe, интеграционный тест) уходит в fallback: raw-mode
// НЕ включается, пишет status-строки в stdout (plain-text, по одной на update).
type ansiView struct {
	stdin  io.Reader
	stdout io.Writer
	inFd   uintptr // fd stdin для term.MakeRaw/IsTerminal
	outFd  uintptr // fd stdout
	tty    bool    // оба дескриптора — tty?

	// raw-mode state (только для tty-режима).
	oldState *term.State

	// events-канал: горутина reader'а кладёт Event'ы сюда.
	events   chan Event
	stopCh   chan struct{}
	stopped  atomic.Bool

	closeOnce sync.Once
	closeErr  error

	// последнее состояние для dedup в non-tty fallback.
	lastFallback string
}

// NewAnsiView — detect tty на stdin/stdout; tty → MakeRaw + alt-screen,
// non-tty → fallback. Возвращает View, готовый к Update/Events.
func NewAnsiView(stdin io.Reader, stdout io.Writer) (View, error) {
	v := &ansiView{
		stdin:  stdin,
		stdout: stdout,
		events: make(chan Event, 8),
		stopCh: make(chan struct{}),
	}
	// Detect tty через file descriptor. io.Reader/io.Writer в общем случае не
	// имеют fd — поэтому caller (cmd/nctalk-talk) передаёт os.Stdin/os.Stdout;
	// type-assert на *os.File для Fd().
	if f, ok := stdin.(*os.File); ok {
		v.inFd = f.Fd()
	}
	if f, ok := stdout.(*os.File); ok {
		v.outFd = f.Fd()
	}
	v.tty = v.inFd != 0 && v.outFd != 0 &&
		term.IsTerminal(int(v.inFd)) && term.IsTerminal(int(v.outFd))

	if v.tty {
		// MakeRaw + alt-screen. Review #11: defer Close() ставит ВЫЗЫВАЮЩИЙ
		// (interactive.Run) сразу после NewAnsiView — гарантия Restore при panic.
		state, err := term.MakeRaw(int(v.inFd))
		if err != nil {
			return nil, fmt.Errorf("tui: MakeRaw: %w", err)
		}
		v.oldState = state
		// Alternate screen.
		fmt.Fprint(stdout, "\x1b[?1049h")
	}
	// reader-горутина (tty → raw-mode single-byte reads; non-tty → построчно).
	go v.readLoop()
	return v, nil
}

// readLoop читает ввод и маппит в Event. TTY: single-byte (raw-mode).
// Non-tty: построчно (stdin может быть закрыт — выходим).
func (v *ansiView) readLoop() {
	defer close(v.events)
	r := bufio.NewReader(v.stdin)
	for {
		if v.stopped.Load() {
			return
		}
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		ev, ok := byteToEvent(b)
		if !ok {
			continue
		}
		select {
		case v.events <- ev:
		case <-v.stopCh:
			return
		}
	}
}

// byteToEvent — таблица байт → Event. m/M→EvMuteToggle, q/Q/0x03(Ctrl-C)→EvLeave,
// + → EvVolUp, - → EvVolDown, прочее → ничего.
func byteToEvent(b byte) (Event, bool) {
	switch b {
	case 'm', 'M':
		return EvMuteToggle, true
	case 'q', 'Q', 0x03: // 0x03 = Ctrl-C
		return EvLeave, true
	case '+', '=': // '=' рядом с '+' на US-клавиатуре — даём для удобства
		return EvVolUp, true
	case '-', '_':
		return EvVolDown, true
	}
	return 0, false
}

// Update пушит enriched-снапшот в отрисовку.
func (v *ansiView) Update(s CallState) {
	if v.tty {
		v.renderTTY(s)
	} else {
		v.renderFallback(s)
	}
}

// renderTTY — перерисовка in-place (alternate screen, без скролла).
// Cursor-home + per-line clear-to-EOL (\x1b[K) + clear-to-end-of-screen (\x1b[J)
// ВНИМАНИЕ full \x1b[2J на 10Гц мерцает (review LOW-3: flicker) — заменён на
// selective-clear: курсор домой, каждая строка дописывается \x1b[K (стирает хвост
// прошлой строки), в конце \x1b[J стирает строки ниже (если прошлый кадр был длиннее).
func (v *ansiView) renderTTY(s CallState) {
	var b strings.Builder
	// Cursor home (без full clear).
	b.WriteString("\x1b[H")
	fmt.Fprintf(&b, "nctalk-talk — статус: %s\x1b[K\n", s.Status)
	fmt.Fprintf(&b, "mute(M): %s   громкость(+/-): %d%%\x1b[K\n",
		mutedLabel(s.SelfMuted), s.Volume)
	b.WriteString("────────────────────────\x1b[K\n")
	if len(s.Participants) == 0 {
		b.WriteString("(нет участников)\x1b[K\n")
	}
	for _, p := range s.Participants {
		mark := " "
		if p.Speaking {
			mark = "▶"
		}
		fmt.Fprintf(&b, "%s %s  [%s]\x1b[K\n", mark, p.Name, levelBar(p.Level))
	}
	b.WriteString("────────────────────────\x1b[K\n")
	b.WriteString("Q/Ctrl-C — выйти\x1b[K\n")
	b.WriteString("\x1b[J") // стереть строки ниже (прошлый кадр длиннее)
	fmt.Fprint(v.stdout, b.String())
}

// renderFallback — non-tty (pipe): plain-text, по одной строке-статуса на update.
// Review #9: integration test piping stdin/stdout работает в этом режиме.
func (v *ansiView) renderFallback(s CallState) {
	line := fmt.Sprintf("nctalk-talk: status=%s muted=%v vol=%d parts=%d",
		s.Status, s.SelfMuted, s.Volume, len(s.Participants))
	if line == v.lastFallback {
		return // dedup одинаковых строк в fallback.
	}
	v.lastFallback = line
	fmt.Fprintln(v.stdout, line)
}

func mutedLabel(m bool) string {
	if m {
		return "ВЫКЛ"
	}
	return "вкл"
}

func levelBar(l int) string {
	if l < 0 {
		l = 0
	}
	if l > 100 {
		l = 100
	}
	n := l / 10
	return strings.Repeat("█", n) + strings.Repeat("░", 10-n)
}

// Events — канал ввода.
func (v *ansiView) Events() <-chan Event { return v.events }

// Close — idempotent. Restore tty (если был MakeRaw) + exit alt-screen.
// Review #11: вызывается через defer сразу после MakeRaw в interactive.Run.
func (v *ansiView) Close() error {
	v.closeOnce.Do(func() {
		v.stopped.Store(true)
		close(v.stopCh)
		// Дреин events (reader-горутина выйдет).
		if v.tty {
			// Exit alt-screen.
			fmt.Fprint(v.stdout, "\x1b[?1049l")
			// Restore tty.
			if v.oldState != nil {
				v.closeErr = term.Restore(int(v.inFd), v.oldState)
			}
		}
	})
	return v.closeErr
}

// compile-time: ansiView реализует View.
var _ View = (*ansiView)(nil)
