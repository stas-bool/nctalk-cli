// internal/call/interactive/interactive.go — оркестратор TUI-режима.
// Спека 2026-07-21 §4.6. Тонкая связка: MicSource→muteSource→agent.AudioIn,
// speakerWriter→volumeWriter→agent.Stdout, View→OnState-enrichment→отрисовка.
package interactive

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stas/nctalk/internal/call/agent"
	"github.com/stas/nctalk/internal/call/signaling"
	"github.com/stas/nctalk/internal/config"
)

// agentRunner — внутренняя точка инъекции для тестов (production = agent.Run).
type agentRunner = func(ctx context.Context, cfg agent.Config) error

// Config для interactive.Run. Большинство полей пробрасывается в agent.Config.
type Config struct {
	Cfg          config.Config
	Token        string
	Signaling    *signaling.Client
	ICEServers   []webrtc.ICEServer
	OwnUserId    string
	OwnSessionId string
	ICETimeout   time.Duration
	DeviceIn     string // NCTALK_AUDIO_DEVICE_IN — avfoundation-строка (default ":0")
	DeviceOut    string // NCTALK_AUDIO_DEVICE_OUT — audiotoolbox int-idx или "default"
	LogFile      io.Writer
	View         View // nil → NewAnsiView(os.Stdin, os.Stdout)

	// internal: для тестов. nil → agent.Run.
	runner agentRunner
}

// Run связывает primitives и запускает agent.Run (sendrecv всегда).
// Поток (спека §4.6):
//  1. Создать MicSource (lazy), speakerWriter.
//  2. Обернуть: audioIn = &muteSource{inner: mic}, stdout = &volumeWriter{inner: speaker}.
//  3. Создать View (если не задан — NewAnsiView). defer view.Close() сразу после создания (review #11).
//  4. OnState enrichment (agent.CallState → interactive.CallState) с Speaking-гистерезисом.
//  5. Event-loop: EvMuteToggle→mute.Toggle, EvVolUp/Down→volume.Inc/Dec, EvLeave→cancel.
//  6. agent.Run(ctx, agent.Config{ AudioIn: audioIn, Stdout: stdout, OnState: onState, InFlags: 3, ... }).
//  7. Cleanup: close speakerWriter (НЕ mic — agent.Run уже вызвал encoder.Close = AudioIn.Close).
func Run(ctx context.Context, cfg Config) error {
	runner := cfg.runner
	if runner == nil {
		runner = agent.Run
	}
	// 1. MicSource (lazy — ffmpeg НЕ запущен до первого ReadSample) + speakerWriter.
	mic := NewMicSource(cfg.DeviceIn)
	speaker, err := NewSpeakerWriter(cfg.DeviceOut)
	if err != nil {
		return fmt.Errorf("interactive: speaker: %w", err)
	}

	// 2. Wrappers.
	mute := &muteSource{inner: mic}
	vol := &volumeWriter{inner: speaker}
	vol.SetGain(100)

	// 3. View (review #11: defer сразу после создания — Restore при panic в agent.Run).
	view := cfg.View
	if view == nil {
		view, err = NewAnsiView(os.Stdin, os.Stdout)
		if err != nil {
			_ = speaker.Close()
			return fmt.Errorf("interactive: view: %w", err)
		}
	}
	defer view.Close()

	// 4. OnState enrichment (agent.CallState → interactive.CallState).
	enricher := &stateEnricher{
		view:     view,
		mute:     mute,
		vol:      vol,
		onLevel:  30,
		offLevel: 15,
	}

	// 5. Event-loop: читает view.Events() и обновляет mute/volume/cancel.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go eventLoop(view.Events(), mute, vol, cancel)

	// 6. agent.Run.
	agentErr := runner(ctx, agent.Config{
		Signaling:    cfg.Signaling,
		Token:        cfg.Token,
		InFlags:      3, // sendrecv всегда в TUI-режиме (спека §4.6)
		OwnUserId:    cfg.OwnUserId,
		OwnSessionId: cfg.OwnSessionId,
		ICEServers:   cfg.ICEServers,
		ICETimeout:   cfg.ICETimeout,
		AudioIn:      mute,
		Stdout:       vol,
		Stderr:       cfg.LogFile,
		OnState:      enricher.onState,
	})

	// 7. Cleanup: только speakerWriter. mic (MicSource) закрывает agent.Run через
	// encoder.Close = AudioIn.Close = muteSource.Close = MicSource.Close (review #4).
	_ = speaker.Close()
	return agentErr
}

// eventLoop читает events и применяет их к mute/volume, либо cancel ctx.
func eventLoop(events <-chan Event, mute *muteSource, vol *volumeWriter, cancel context.CancelFunc) {
	for ev := range events {
		switch ev {
		case EvMuteToggle:
			mute.Toggle()
		case EvVolUp:
			vol.Inc(10)
		case EvVolDown:
			vol.Dec(10)
		case EvLeave:
			cancel()
			return
		}
	}
}

// stateEnricher — обогащает agent.CallState (raw) до interactive.CallState (с
// SelfMuted/Volume/Speaking-гистерезисом) и пушит в View.
type stateEnricher struct {
	view     View
	mute     *muteSource   // для SelfMuted (atomic read)
	vol      *volumeWriter // для Volume (atomic read)
	onLevel  int           // порог Speaking ON (default 30)
	offLevel int           // порог Speaking OFF (default 15)

	mu       sync.Mutex
	last     CallState
	speaking map[string]bool // sessionId → текущее Speaking
}

// onState — callback для agent.Config.OnState. Под enriched-блокировкой НЕ блокирует agent.
func (e *stateEnricher) onState(raw agent.CallState) {
	e.mu.Lock()
	if e.speaking == nil {
		e.speaking = make(map[string]bool)
	}
	enriched := CallState{
		Status:    raw.Status,
		SelfMuted: e.mute.IsMuted(),
		Volume:    e.vol.Gain(),
	}
	for _, p := range raw.Participants {
		spk := e.speaking[p.SessionId]
		if spk {
			if p.Level < e.offLevel {
				e.speaking[p.SessionId] = false
				spk = false
			}
		} else {
			if p.Level >= e.onLevel {
				e.speaking[p.SessionId] = true
				spk = true
			}
		}
		enriched.Participants = append(enriched.Participants, ParticipantState{
			SessionId: p.SessionId,
			Name:      p.Name,
			Level:     p.Level,
			Speaking:  spk,
		})
	}
	e.last = enriched
	e.mu.Unlock()
	// Всегда пушим Update — Level-полоска живая (review #12: dedup без Level
	// = перерисовка на каждом OnState, Level в дедапе НЕ участвует).
	e.view.Update(enriched)
}

// participantsKey — ключ дедупа по count+sorted(sessionId,name). Заготовка для
// future оптимизации (сейчас всегда пушим — review #12).
func participantsKey(ps []ParticipantState) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "%s|%s;", p.SessionId, p.Name)
	}
	return b.String()
}
