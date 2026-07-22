package interactive

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// TestAnsiView_NonTty_Fallback — review #9: non-tty stdin/stdout → fallback-режим,
// Update пишет status-строку (plain-text) в stdout, без \x1b[?1049h.
//
// ВНИМАНИЕ: stdout — *bytes.Buffer, НЕ io.Pipe. io.Pipe Writer блокирует
// пока Reader не consumed → Update()→Fprintln завис бы (тест на io.Pipe
// вызывал deadlock). bytes.Buffer не блокирует и не является *os.File
// (type-assert в NewAnsiView даёт v.tty=false — то же поведение).
func TestAnsiView_NonTty_Fallback(t *testing.T) {
	rIn, wIn := io.Pipe()
	defer rIn.Close()
	defer wIn.Close()
	var out bytes.Buffer // НЕ *os.File → v.tty=false

	v, err := NewAnsiView(rIn, &out)
	if err != nil {
		t.Fatalf("NewAnsiView: %v", err)
	}
	defer v.Close()

	v.Update(CallState{Status: "joined", SelfMuted: false, Volume: 100})

	s := out.String()
	if !strings.Contains(s, "status=joined") {
		t.Errorf("fallback output не содержит статус: %q", s)
	}
	if strings.Contains(s, "\x1b[?1049h") {
		t.Errorf("non-tty: alt-screen escape в выводе (не должен быть): %q", s)
	}
}

func TestAnsiView_EventsKeyPresses(t *testing.T) {
	rIn, wIn := io.Pipe()
	rOut, wOut := io.Pipe()
	defer rIn.Close()
	defer rOut.Close()
	defer wOut.Close()
	v, err := NewAnsiView(rIn, wOut)
	if err != nil {
		t.Fatalf("NewAnsiView: %v", err)
	}
	defer v.Close()

	// Послать 'm' и 'q'.
	go func() {
		wIn.Write([]byte{'m'})
		wIn.Write([]byte{'q'})
	}()

	got := []Event{}
	for len(got) < 2 {
		select {
		case ev := <-v.Events():
			got = append(got, ev)
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout: получили %d event'ов, want 2", len(got))
		}
	}
	if got[0] != EvMuteToggle || got[1] != EvLeave {
		t.Errorf("events = %v, want [EvMuteToggle EvLeave]", got)
	}
}

func TestAnsiView_CloseIdempotent(t *testing.T) {
	rIn, wIn := io.Pipe()
	rOut, wOut := io.Pipe()
	defer rIn.Close()
	defer wIn.Close()
	defer rOut.Close()
	defer wOut.Close()
	v, _ := NewAnsiView(rIn, wOut)
	if err := v.Close(); err != nil {
		t.Errorf("первый Close: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Errorf("повторный Close: %v (review #11 — должен быть nil)", err)
	}
}

// TestAnsiView_RenderTTY_Snapshot — ручная проверка renderTTY (без MakeRaw):
// статус, mute-метка, имена, level-bar, 'Q/Ctrl-C — выйти'.
func TestAnsiView_RenderTTY_Snapshot(t *testing.T) {
	var out bytes.Buffer
	v := &ansiView{
		stdout: &out,
		tty:    true, // принудительно (без MakeRaw — только render)
	}
	v.Update(CallState{
		Status:    "joined",
		SelfMuted: true,
		Volume:    130,
		Participants: []ParticipantState{
			{SessionId: "s1", Name: "alice", Level: 70, Speaking: true},
			{SessionId: "s2", Name: "bob", Level: 0, Speaking: false},
		},
	})
	s := out.String()
	checks := map[string]bool{
		"joined":           strings.Contains(s, "joined"),
		"ВЫКЛ":             strings.Contains(s, "ВЫКЛ"),
		"alice":            strings.Contains(s, "alice"),
		"bob":              strings.Contains(s, "bob"),
		"130%":             strings.Contains(s, "130%"),
		"Q/Ctrl-C — выйти": strings.Contains(s, "Q/Ctrl-C — выйти"),
	}
	for want, ok := range checks {
		if !ok {
			t.Errorf("render не содержит %q:\n%s", want, s)
		}
	}
}
