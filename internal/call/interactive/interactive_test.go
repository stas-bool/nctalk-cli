package interactive

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stas-bool/nctalk-cli/internal/call/agent"
)

// fakeView — запись всех Update-вызовов для тестов.
type fakeView struct {
	mu     sync.Mutex
	states []CallState
	events chan Event
}

func newFakeView() *fakeView {
	return &fakeView{events: make(chan Event, 8)}
}

func (f *fakeView) Update(s CallState) {
	f.mu.Lock()
	f.states = append(f.states, s)
	f.mu.Unlock()
}

func (f *fakeView) Events() <-chan Event { return f.events }

func (f *fakeView) Close() error { close(f.events); return nil }

func (f *fakeView) last() CallState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 {
		return CallState{}
	}
	return f.states[len(f.states)-1]
}

// compile-time: fakeView реализует View.
var _ View = (*fakeView)(nil)

func TestEnricher_StatusCopy(t *testing.T) {
	v := newFakeView()
	mute := &muteSource{}
	vol := &volumeWriter{}
	vol.SetGain(110)
	e := &stateEnricher{view: v, mute: mute, vol: vol, onLevel: 30, offLevel: 15}
	e.onState(agent.CallState{Status: "joined", Participants: nil})
	got := v.last()
	if got.Status != "joined" {
		t.Errorf("enricher Status = %q, want joined", got.Status)
	}
	if got.Volume != 110 {
		t.Errorf("enricher Volume = %d, want 110", got.Volume)
	}
}

func TestEnricher_SpeakingHysteresis(t *testing.T) {
	v := newFakeView()
	e := &stateEnricher{view: v, mute: &muteSource{}, vol: &volumeWriter{},
		onLevel: 30, offLevel: 15}
	sid := "peer-A"
	// Тихо → не говорит.
	e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
		{SessionId: sid, Name: "a", Level: 0},
	}})
	if v.last().Participants[0].Speaking {
		t.Error("Level 0 → Speaking=true, want false")
	}
	// Громко → говорит.
	e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
		{SessionId: sid, Name: "a", Level: 50},
	}})
	if !v.last().Participants[0].Speaking {
		t.Error("Level 50 → Speaking=false, want true")
	}
	// Средне (20) → всё ещё говорит (гистерезис).
	e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
		{SessionId: sid, Name: "a", Level: 20},
	}})
	if !v.last().Participants[0].Speaking {
		t.Error("Level 20 после Speaking=true → всё ещё false, want true (гистерезис)")
	}
	// Тихо (<15) → замолчал.
	e.onState(agent.CallState{Status: "joined", Participants: []agent.Participant{
		{SessionId: sid, Name: "a", Level: 5},
	}})
	if v.last().Participants[0].Speaking {
		t.Error("Level 5 → Speaking=true, want false (замолчал)")
	}
}

// TestRun_EventLoop_MuteToggle — fakeView.events → EvMuteToggle → mute.IsMuted()
// flips. Полный цикл через Run с фейк-runner.
func TestRun_EventLoop_MuteToggle(t *testing.T) {
	v := newFakeView()
	var capturedMute *muteSource
	var capturedVol *volumeWriter
	// Фейк-runner: запоминает AudioIn/Stdout, сразу выходит по ctx.
	fakeRunner := func(ctx context.Context, cfg agent.Config) error {
		capturedMute = cfg.AudioIn.(*muteSource)
		capturedVol = cfg.Stdout.(*volumeWriter)
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		v.events <- EvMuteToggle
		v.events <- EvVolUp
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_ = Run(ctx, Config{
		DeviceIn:  ":0",
		DeviceOut: "-1",
		LogFile:   io.Discard,
		View:      v,
		runner:    fakeRunner,
	})
	if capturedMute == nil {
		t.Fatal("AudioIn не captured")
	}
	if !capturedMute.IsMuted() {
		t.Error("после EvMuteToggle mute должен быть ON")
	}
	if capturedVol.Gain() != 110 {
		t.Errorf("после EvVolUp gain = %d, want 110", capturedVol.Gain())
	}
}

func TestRun_EventLoop_Leave_Cancels(t *testing.T) {
	v := newFakeView()
	runnerDone := make(chan struct{})
	fakeRunner := func(ctx context.Context, _ agent.Config) error {
		<-ctx.Done()
		close(runnerDone)
		return nil
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		v.events <- EvLeave
	}()
	_ = Run(context.Background(), Config{
		DeviceIn:  ":0",
		DeviceOut: "-1",
		LogFile:   io.Discard,
		View:      v,
		runner:    fakeRunner,
	})
	select {
	case <-runnerDone:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("EvLeave не отменил ctx — runner не вышел")
	}
}
