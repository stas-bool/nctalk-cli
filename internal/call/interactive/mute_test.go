package interactive

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource — тестовый media.AudioSource. ReadSample отдаёт payload из канала.
type fakeSource struct {
	pkts   chan []byte
	closed atomic.Bool
}

func newFakeSource() *fakeSource { return &fakeSource{pkts: make(chan []byte, 8)} }

func (f *fakeSource) ReadSample() ([]byte, time.Duration, error) {
	p, ok := <-f.pkts
	if !ok {
		return nil, 0, io.EOF
	}
	return p, 20 * time.Millisecond, nil
}

func (f *fakeSource) Close() error {
	f.closed.Store(true)
	close(f.pkts)
	return nil
}

func TestMuteSource_Unmuted_PassesPayload(t *testing.T) {
	src := newFakeSource()
	m := &muteSource{inner: src}
	src.pkts <- []byte("payload-1")
	p, _, err := m.ReadSample()
	if err != nil {
		t.Fatalf("ReadSample: %v", err)
	}
	if string(p) != "payload-1" {
		t.Errorf("got %q, want payload-1", p)
	}
}

func TestMuteSource_Muted_DropsPayload(t *testing.T) {
	src := newFakeSource()
	m := &muteSource{inner: src}
	m.Mute()
	src.pkts <- []byte("dropped-1")
	src.pkts <- []byte("dropped-2")

	// ReadSample в muted-режиме крутит — нужен timeout, чтобы убедиться, что не возвращает.
	errCh := make(chan error, 1)
	pktCh := make(chan []byte, 1)
	go func() {
		p, _, err := m.ReadSample()
		if err != nil {
			errCh <- err
			return
		}
		pktCh <- p
	}()
	select {
	case <-pktCh:
		t.Fatal("ReadSample вернул payload в muted-режиме (должен дропать)")
	case <-errCh:
		t.Fatal("ReadSample вернул ошибку в muted-режиме (должен крутить)")
	case <-time.After(100 * time.Millisecond):
		// ok — крутит, ничего не вернул.
	}

	// Unmute — следующий payload проходит.
	m.Unmute()
	src.pkts <- []byte("passed-3")
	select {
	case p := <-pktCh:
		if string(p) != "passed-3" {
			t.Errorf("got %q after unmute, want passed-3", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ReadSample не вернул payload после unmute")
	}
}

func TestMuteSource_Close_Delegates(t *testing.T) {
	src := newFakeSource()
	m := &muteSource{inner: src}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !src.closed.Load() {
		t.Error("inner.Close не вызван")
	}
}

// TestMuteSource_Toggle_Concurrent — Toggle под атомиком не даёт race под -race.
func TestMuteSource_Toggle_Concurrent(t *testing.T) {
	src := newFakeSource()
	m := &muteSource{inner: src}
	// Кормим payload постоянно.
	go func() {
		for i := 0; i < 100; i++ {
			src.pkts <- []byte{byte(i)}
		}
		close(src.pkts)
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := m.ReadSample()
			if err != nil {
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		m.Toggle()
	}
	<-done
}
