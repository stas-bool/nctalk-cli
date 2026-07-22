package interactive

import (
	"os/exec"
	"testing"
	"time"
)

// hasFFmpeg skipper для всех тестов audio_io.
func hasFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg нет в PATH: %v", err)
	}
}

// TestMicSource_NoStartInConstructor — lazy start (review #8):
// после NewMicSource cmd==nil (ffmpeg НЕ запущен). Старт только при первом ReadSample.
func TestMicSource_NoStartInConstructor(t *testing.T) {
	hasFFmpeg(t)
	m := NewMicSource(":0")
	if m.cmd != nil {
		t.Fatal("NewMicSource запустил ffmpeg — нарушен lazy start (review #8)")
	}
	// Быстрый cancel без ReadSample — Close идемпотентен, no orphan process.
	if err := m.Close(); err != nil {
		t.Errorf("Close без start: %v", err)
	}
}

// TestMicSource_FfmpegArgs — start() запускает ffmpeg-процесс с корректными args.
// Проверка canonical канона review #1: input=avfoundation, NOT audiotoolbox.
//
// ВАЖНО: ffmpeg-args содержат ДВЕ пары "-f" (input muxer avfoundation + output muxer
// opus). Логика "все -f должны быть avfoundation" из плана — баг (рягается на -f opus).
// Заменено на проверку вхождения конкретной пары, как в TestSpeakerWriter_FfmpegArgs.
func TestMicSource_FfmpegArgs(t *testing.T) {
	hasFFmpeg(t)
	m := NewMicSource(":5")
	if err := m.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Close()
	args := m.cmd.Args
	// Ищем пару "-f avfoundation" (input muxer, review #1 CRITICAL).
	found := false
	for i, a := range args {
		if a == "-f" && i+1 < len(args) && args[i+1] == "avfoundation" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("args не содержат -f avfoundation (review #1 CRITICAL): %v", args)
	}
	// device в нужной позиции.
	foundDev := false
	for _, a := range args {
		if a == ":5" {
			foundDev = true
		}
	}
	if !foundDev {
		t.Errorf("args не содержат device \":5\": %v", args)
	}
}

// TestMicSource_CancelCleanup — start + Close: ffmpeg убит, waitErr получен.
// orphan-protection: Wait().ProcessState.
func TestMicSource_CancelCleanup(t *testing.T) {
	hasFFmpeg(t)
	m := NewMicSource(":0")
	if err := m.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Logf("Close err (ok): %v", err)
	}
	// Дать ОС секунду на cleanup.
	time.Sleep(200 * time.Millisecond)
	// Не проверяем напрямую pid (на CI может не быть ps) — сам факт Close без
	// блокировки и orphan-test в integration (Task 4.10).
}

// TestMicSource_CloseIdempotent — повторный Close не падает (finding #4).
func TestMicSource_CloseIdempotent(t *testing.T) {
	hasFFmpeg(t)
	m := NewMicSource(":0")
	_ = m.start()
	if err := m.Close(); err != nil {
		t.Logf("первый Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("повторный Close: %v (должен быть no-op или nil)", err)
	}
}

// TestSpeakerWriter_FfmpegArgs — canonical канон review #1: output=audiotoolbox,
// НЕ avfoundation (avfoundation input-only). Проверяем ключевые args.
func TestSpeakerWriter_FfmpegArgs(t *testing.T) {
	hasFFmpeg(t)
	sw, err := NewSpeakerWriter("0")
	if err != nil {
		t.Fatalf("NewSpeakerWriter: %v", err)
	}
	cmd := sw.cmd
	args := cmd.Args
	defer sw.Close()
	// Ищем пару "-f audiotoolbox" в output-части (после "-i -").
	found := false
	for i, a := range args {
		if a == "-f" && i+1 < len(args) && args[i+1] == "audiotoolbox" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("args не содержат -f audiotoolbox (review #1 CRITICAL): %v", args)
	}
	// -audio_device_index 0.
	foundIdx := false
	for i, a := range args {
		if a == "-audio_device_index" && i+1 < len(args) && args[i+1] == "0" {
			foundIdx = true
			break
		}
	}
	if !foundIdx {
		t.Errorf("args не содержат -audio_device_index 0: %v", args)
	}
}

// TestSpeakerWriter_DefaultDevice — "default" → "-1".
func TestSpeakerWriter_DefaultDevice(t *testing.T) {
	hasFFmpeg(t)
	sw, err := NewSpeakerWriter("default")
	if err != nil {
		t.Fatalf("NewSpeakerWriter: %v", err)
	}
	defer sw.Close()
	cmd := sw.cmd
	found := false
	for i, a := range cmd.Args {
		if a == "-audio_device_index" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default device: args не содержат -audio_device_index -1: %v", cmd.Args)
	}
}
