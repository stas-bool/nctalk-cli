//go:build darwin || linux

package cli

// handlers_chat_attach_unix_test.go — FIFO в `chat send --attach` (спека-дельта
// 2026-10-09 §2.3): syscall.Mkfifo есть только на unix — отдельный файл с
// build-тегом, чтобы пакет собирался везде.

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestChatSendAttach_FIFO — FIFO без писателя: Stat ДО Open отсекает не-обычный
// файл, и handler не зависает в open(2).
func TestChatSendAttach_FIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo недоступен: %v", err)
	}
	spy := &chatSpyClient{}
	deps := newChatSendDeps(spy, strings.NewReader(""))
	done := make(chan ExitError, 1)
	go func() {
		done <- chatSendHandler(context.Background(), deps, []string{"TOK", "--attach", p}, false)
	}()
	select {
	case ee := <-done:
		if ee.Code != ExitGeneric || ee.Err == nil || !strings.Contains(ee.Err.Error(), "не обычный файл") {
			t.Errorf("code=%d err=%v, want 1 «не обычный файл»", ee.Code, ee.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler завис на FIFO: os.Stat должен идти до os.Open")
	}
	if spy.fileCalls != 0 {
		t.Errorf("SendFile вызван %d раз, want 0", spy.fileCalls)
	}
}
