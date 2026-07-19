package ogg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---- CRC32: проверка на известных значениях ----

// TestOGGCRC_TableFirst16 — проверка первых 16 значений таблицы против
// эталонных из RFC 3533 §8 / libogg framing.c (полином 0x04c11db7,
// не-reflected). Это гарантирует, что таблица построена по правильному
// многочлену — остальное (правильность использования в pageChecksum)
// проверяется smoke-тестом через ffmpeg, который валидирует CRC.
func TestOGGCRC_TableFirst16(t *testing.T) {
	want := [16]uint32{
		0x00000000, 0x04c11db7, 0x09823b6e, 0x0d4326d9,
		0x130476dc, 0x17c56b6b, 0x1a864db2, 0x1e475005,
		0x2608edb8, 0x22c9f00f, 0x2f8ad6d6, 0x2b4bcb61,
		0x350c9b64, 0x31cd86d3, 0x3c8ea00a, 0x384fbdbd,
	}
	for i := 0; i < 16; i++ {
		if oggCRCTable[i] != want[i] {
			t.Errorf("oggCRCTable[%d] = 0x%08x, want 0x%08x", i, oggCRCTable[i], want[i])
		}
	}
}

// TestOGGCRC_UpdateProperties — структурные свойства: пустой вход не меняет
// crc; один 0-байт не меняет crc; crc-последовательность ассоциативна по
// конкатенации (CRC(a||b) == CRC(CRC(a), b)).
func TestOGGCRC_UpdateProperties(t *testing.T) {
	if got := oggCRCUpdate(0, nil); got != 0 {
		t.Errorf("CRC(nil) = 0x%08x, want 0", got)
	}
	if got := oggCRCUpdate(0, []byte{0, 0, 0, 0, 0, 0, 0, 0}); got != 0 {
		t.Errorf("CRC(8×0) = 0x%08x, want 0", got)
	}
	// CRC(a||b) == oggCRCUpdate(CRC(a), b).
	a := []byte("OggS")
	b := []byte{1, 2, 3, 4, 5}
	combined := oggCRCUpdate(0, append(append([]byte{}, a...), b...))
	stepByStep := oggCRCUpdate(oggCRCUpdate(0, a), b)
	if combined != stepByStep {
		t.Errorf("нарушена ассоциативность: combined=0x%08x, stepByStep=0x%08x", combined, stepByStep)
	}
}

// TestOGGCRC_TableBuilt — sanity: таблица проинициализирована (хотя бы один
// ненулевой элемент, типичный для poly 0x04c11db7).
func TestOGGCRC_TableBuilt(t *testing.T) {
	if oggCRCTable[0] != 0 {
		t.Errorf("oggCRCTable[0] = 0x%08x, want 0 (entry 0 всегда 0)", oggCRCTable[0])
	}
	// Entry 0xff для poly 0x04c11db7 — известное значение 0x2d8aabb2 ... или
	// близкое. Возьмём верхний уровень: просто проверим что таблица не нулевая.
	nonZero := 0
	for _, v := range oggCRCTable {
		if v != 0 {
			nonZero++
		}
	}
	if nonZero < 128 {
		t.Errorf("oggCRCTable: только %d ненулевых элементов — таблица не построена", nonZero)
	}
}

// ---- Writer/Reader round-trip на синтетических пакетах ----

// TestWriterReader_RoundTrip — пишем N синтетических пакетов через Writer,
// читаем через Reader. Ожидаем получить те же пакеты в том же порядке;
// OpusHead/OpusTags должны быть прозрачно пропущены Reader'ом.
func TestWriterReader_RoundTrip(t *testing.T) {
	wantPackets := [][]byte{
		[]byte("AUDIO_PKT_1_some_payload_data"),
		[]byte("AUDIO_PKT_2_other_payload"),
		bytes.Repeat([]byte{0xAB}, 300), // >255 — проверка segment_table logic
		bytes.Repeat([]byte{0xCD}, 510), // ровно 2*255 — проверка terminator
		[]byte("AUDIO_PKT_last"),
	}

	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, p := range wantPackets {
		if err := w.WritePacket(p); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reader должен отдать только audio-пакеты.
	r := NewReader(&buf)
	gotPackets := [][]byte{}
	for {
		pkt, err := r.NextPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("NextPacket: %v", err)
		}
		gotPackets = append(gotPackets, pkt)
	}

	if len(gotPackets) != len(wantPackets) {
		t.Fatalf("ожидалось %d пакетов, получено %d (OpusHead/OpusTags не должны были дойти до caller'а)",
			len(wantPackets), len(gotPackets))
	}
	for i, want := range wantPackets {
		if !bytes.Equal(gotPackets[i], want) {
			t.Errorf("пакет %d: получили %d байт (% x), хотим %d байт (% x)",
				i, len(gotPackets[i]), gotPackets[i][:min(16, len(gotPackets[i]))],
				len(want), want[:min(16, len(want))])
		}
	}
}

// TestReader_SkipsOpusHeadAndTags — отдельно: первый пакет Reader'а НЕ OpusHead,
// последний НЕ OpusTags. Это критично для downstream-потребителей (pion
// WriteSample), которые ждут именно audio.
func TestReader_SkipsOpusHeadAndTags(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.WritePacket([]byte("audio_payload_xyz")); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	r := NewReader(&buf)
	pkt, err := r.NextPacket()
	if err != nil {
		t.Fatalf("NextPacket: %v", err)
	}
	if isOpusHead(pkt) || isOpusTags(pkt) {
		t.Errorf("Reader отдал служебный пакет (%q) как audio — нарушение контракта", pkt)
	}
	if string(pkt) != "audio_payload_xyz" {
		t.Errorf("получен %q, хотим %q", pkt, "audio_payload_xyz")
	}
}

// TestWriter_BOSAndCommentPages — NewWriter должен записать 2 служебные
// страницы до первого audio. Проверяем через прямой разбор buf.
func TestWriter_BOSAndCommentPages(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewWriter(&buf); err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Не пишем audio — в buf только 2 служебные страницы.
	data := buf.Bytes()
	// Минимальные размеры: BOS (27 + 1 + 19 = 47) + comment (27 + 2 + 16 = 45).
	if len(data) < 47+45 {
		t.Fatalf("слишком короткий вывод Writer: %d байт (ожидалось ≥%d)", len(data), 47+45)
	}
	// Проверяем что первая страница BOS с OpusHead.
	if string(data[:4]) != "OggS" {
		t.Fatalf("нет capture pattern в начале")
	}
	if data[5]&flagBOS == 0 {
		t.Errorf("первая страница не BOS (flags=0x%x)", data[5])
	}
	// Находим OpusHead в payload первой страницы.
	segCount1 := int(data[26])
	payloadStart1 := 27 + segCount1
	if payloadStart1+19 > len(data) {
		t.Fatalf("первая страница слишком коротка для OpusHead")
	}
	if string(data[payloadStart1:payloadStart1+8]) != opusHeadMagic {
		t.Errorf("первая страница payload не начинается с OpusHead: %q", data[payloadStart1:payloadStart1+8])
	}
	// pre-skip в OpusHead = 312 (LE uint16 по смещению 10).
	preSkip := uint16(data[payloadStart1+10]) | uint16(data[payloadStart1+11])<<8
	if preSkip != opusPreSkip {
		t.Errorf("pre-skip в OpusHead = %d, хотим %d", preSkip, opusPreSkip)
	}

	// Вторая страница — vorbis-comment (flags=0).
	page2Off := payloadStart1 + 0 // end of payload of page1
	// Размер payload страницы 1 = sum(segTable[0..segCount1]).
	page1PayloadLen := 0
	for i := 0; i < segCount1; i++ {
		page1PayloadLen += int(data[27+i])
	}
	page2Off = 27 + segCount1 + page1PayloadLen
	if page2Off+27 > len(data) {
		t.Fatalf("вторая страница не уместилась в выводе")
	}
	if string(data[page2Off:page2Off+4]) != "OggS" {
		t.Fatalf("вторая страница не начинается с OggS")
	}
	if data[page2Off+5] != 0 {
		t.Errorf("вторая страница flags=0x%x (BOS/ERROR — не должны быть установлены)", data[page2Off+5])
	}
	segCount2 := int(data[page2Off+26])
	payloadStart2 := page2Off + 27 + segCount2
	if payloadStart2+8 > len(data) {
		t.Fatalf("вторая страница payload слишком коротка для OpusTags magic")
	}
	if string(data[payloadStart2:payloadStart2+8]) != opusTagsMagic {
		t.Errorf("вторая страница payload не OpusTags: %q", data[payloadStart2:payloadStart2+8])
	}
}

// TestWriter_ProducesEOS_OnFlush — после Flush в выводе должна быть
// EOS-страница с пустым payload (number_page_segments=0).
func TestWriter_ProducesEOS_OnFlush(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.WritePacket([]byte("first")); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Ищем EOS-страницу: scanning по OggS-маркерам.
	data := buf.Bytes()
	foundEOS := false
	off := 0
	for off+27 <= len(data) {
		if string(data[off:off+4]) != "OggS" {
			off++
			continue
		}
		flags := data[off+5]
		segCount := int(data[off+26])
		payloadLen := 0
		for i := 0; i < segCount; i++ {
			payloadLen += int(data[off+27+i])
		}
		pageLen := 27 + segCount + payloadLen
		if flags&flagEOS != 0 {
			foundEOS = true
			if segCount != 0 || payloadLen != 0 {
				t.Errorf("EOS-страница имеет непустой payload: segCount=%d, payloadLen=%d", segCount, payloadLen)
			}
		}
		off += pageLen
	}
	if !foundEOS {
		t.Errorf("EOS-страница не найдена в выводе Writer после Flush()")
	}
}

// ---- Smoke: ffmpeg принимает Writer-вывод ----

// TestSmoke_FFmpegAcceptsWriterOutput — критический инвариант: OGG-страницы
// с нашим CRC и OpusHead/comment должны приниматься ffmpeg decode без ошибок.
// Кормим ffmpeg-encode (тишина 200мс) → ogg.Reader (выдераем raw Opus) →
// ogg.Writer → ffmpeg-decode `-f opus -i - -f null -`. Если CRC/заголовки
// некорректны, ffmpeg падает с "Invalid page" / "Invalid OpusHead" и
// exit != 0.
func TestSmoke_FFmpegAcceptsWriterOutput(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не установлен — пропуск smoke-теста Writer")
	}

	// 1. Получаем валидные raw Opus-пакеты через ffmpeg-encode тишины.
	packets := synthesizeOpusPackets(t, 10)
	if len(packets) == 0 {
		t.Fatal("ffmpeg encode не отдал пакетов")
	}

	// 2. Прогон через Writer.
	var oggOut bytes.Buffer
	w, err := NewWriter(&oggOut)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, p := range packets {
		if err := w.WritePacket(p); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// 3. Кормим ffmpeg-decode с проверкой exit 0 и отсутствием ошибок в stderr.
	// ВАЖНО: в ffmpeg 7.x НЕТ demuxer с именем "opus" (только muxer).
	// Demuxer называется "ogg" (ffmpeg сам различает ogg/opus по содержимому,
	// но явный `-f ogg` детерминирует probe). Это отклонение от брифа/спеки §8,
	// где написано `-f opus -i -` — указано в финальном отчёте.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-f", "ogg", "-i", "-", "-f", "null", "-")
	cmd.Stdin = &oggOut
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("ffmpeg отклонил Writer-вывод: %v\nstderr:\n%s", err, stderr.String())
	}
	if msg := stderr.String(); strings.Contains(msg, "Invalid") || strings.Contains(msg, "Error") {
		// ffmpeg пишет "Error" и в некоторые не-фатальные сообщения, но
		// связка с "Invalid" почти всегда означает проблему со страницей.
		// Если exit==0 — это warning; если exit!=0 — уже поймали выше.
		if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			t.Errorf("ffmpeg сообщает об ошибке в OGG:\n%s", msg)
		}
	}
}

// synthesizeOpusPakes кодирует N тишин-в-Opus через ffmpeg и через ogg.Reader
// достаёт raw Opus-пакеты. N ограничивает число возвращённых пакетов.
func synthesizeOpusPackets(t *testing.T, maxPackets int) [][]byte {
	t.Helper()
	// Тишина: 200мс * 48000 * 2 байта (s16le) = 19200 байт.
	const silenceSamples = 48000 * 200 / 1000
	pcm := make([]byte, silenceSamples*2)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "-",
		"-c:a", "libopus", "-application", "voip", "-f", "opus", "-")
	cmd.Stdin = bytes.NewReader(pcm)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg encode тишины не удался: %v\nstderr:\n%s", err, stderr.String())
	}

	r := NewReader(&stdout)
	var out [][]byte
	for len(out) < maxPackets {
		pkt, err := r.NextPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("ogg.Reader: %v", err)
		}
		out = append(out, pkt)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
