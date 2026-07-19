// Package ogg — минимальный pure-Go OGG-демультиплексор/мультиплексор (stdlib
// only). Режет OGG-страницы из ffmpeg-encode-вывода на отдельные raw
// Opus-пакеты (для передачи в pion WriteSample), и оборачивает raw
// Opus-пакеты в OGG-страницы перед ffmpeg-decode-input. Спека 2026-07-19 §8
// (стратегия a1).
//
// Реализован только минимум, нужный для Opus-потока:
//   - Reader: читает OGG-страницы из io.Reader, отдаёт raw Opus-пакеты,
//     прозрачно пропуская служебные пакеты OpusHead/OpusTags (RFC 5334/6716).
//   - Writer: пишет BOS-страницу с OpusHead (моно/48к/pre-skip=312), страницу
//     с минимальным vorbis-comment, далее одну audio-страницу на каждый
//     WritePacket, и EOS-страницу на Flush. Без корректных заголовков ffmpeg
//     `-f opus -i -` падает «Invalid OpusHead» (review замечание 4).
//
// Формат страницы — RFC 3533 §6 framing (см. pageHeaderSize, pageLayout).
// CRC32 — НЕ-reflected, полином 0x04c11db7, init=0, без final-XOR
// (libogg framing.c: ogg_page_checksum_set). crc32.IEEE (0xEDB88320 reflected)
// НЕ подходит — это другой полином; ffmpeg валидирует именно OGG-CRC.
package ogg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Смещения полей в OGG-заголовке страницы (RFC 3533 §6).
const (
	capturePattern    = "OggS" // 4 байта, смещение 0
	pageVersionOffset = 4      // 1 байт, всегда 0
	pageFlagsOffset   = 5      // 1 байт header_type
	// 0x1 = continuation, 0x2 = BOS, 0x4 = EOS
	pageGranuleOffset = 6  // 8 байт int64 LE
	pageSerialOffset  = 14 // 4 байта uint32 LE
	pageSeqOffset     = 18 // 4 байта uint32 LE
	pageCRCOffset     = 22 // 4 байта uint32 LE
	pageSegCountOff   = 26 // 1 байт uint8 — число элементов segment_table
	pageSegTableOff   = 27 // N байт segment_table
	// Сразу за segment_table — payload суммарной длиной sum(segment_table).

	pageHeaderFixedSize = 27 // без учёта segment_table и payload
)

// Флаги header_type.
const (
	flagContinuation byte = 0x1
	flagBOS          byte = 0x2
	flagEOS          byte = 0x4
)

// Параметры OpusHead (фиксированы для проекта: моно/48к/libopus voip).
const (
	opusHeadMagic        = "OpusHead"
	opusHeadVersion byte = 1
	opusChannels    byte = 1
	opusSampleRate        = 48000
	// opusPreSkip — задержка input-output в сэмплах (RFC 7895 §5.1). libopus voip
	// default = 312 при 48к (~6.5мс). review замечание 12: при encoder/decoder
	// с разными application-mode значение отличается; рассинхрон даст смещение
	// звука на pre-skip сэмплов. Для spike (детекция 440 Гц) незаметно, для
	// AEC/диаризации (out of scope, спека §1) существенно.
	// TODO review-12: парсить pre-skip из encoder-вывода (Reader уже видит
	// OpusHead) и подставлять в Writer — отложено до production-этапа (Этап 3).
	opusPreSkip           = uint16(312)
	opusOutputGain        = uint16(0)
	opusMappingFamily byte = 0
)

// Константы vorbis-comment (минимальный блок для ffmpeg `-f ogg`).
const (
	opusTagsMagic    = "OpusTags"
	opusVendorString = "nctalk" // произвольный — валиден любой непустой
)

// ---- CRC32 (не-reflected, полином 0x04c11db7, init 0, без XOR) ----
//
// Стандартная crc32.MakeTable(crc32.IEEE) НЕ подходит: IEEE — reflected
// полином 0xEDB88320. OGG использует MSB-first над тем же многочленом
// 0x04c11db7. Таблицу строим один раз при init — см. init() ниже.

var oggCRCTable [256]uint32

func init() {
	const poly uint32 = 0x04c11db7
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ poly
			} else {
				r <<= 1
			}
		}
		oggCRCTable[i] = r
	}
}

// oggCRCUpdate делает один проход OGG-CRC (полином 0x04c11db7, MSB-first,
// init=0, без final-XOR) над байтами p, начиная с накопленного значения crc.
// Используется внутри pageChecksum; наружу не выставлен — потребителям
// нужен только целиком посчитанный checksum страницы.
func oggCRCUpdate(crc uint32, p []byte) uint32 {
	for _, b := range p {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

// pageChecksum считает OGG-CRC над страницей с обнулённым CRC-полем.
// Не модифицирует страницу — копию поля делаем через replace-на-лету
// тривиально: crc считается над байтами page с подменой 4 байтов CRC на 0.
func pageChecksum(page []byte) uint32 {
	if len(page) < pageHeaderFixedSize {
		return 0
	}
	var crc uint32
	// Заголовок до CRC-поля.
	crc = oggCRCUpdate(crc, page[:pageCRCOffset])
	// 4 нулевых байта «виртуального» CRC-поля.
	crc = oggCRCUpdate(crc, []byte{0, 0, 0, 0})
	// Остальное (pageSeqOff + 8 байт granule… до конца, включая segment_table
	// и payload).
	crc = oggCRCUpdate(crc, page[pageCRCOffset+4:])
	return crc
}

// ---- Reader ----

// Reader читает OGG-страницы из io.Reader и отдаёт только raw Opus-пакеты
// (audio). Служебные пакеты OpusHead (BOS-страница) и OpusTags
// (vorbis-comment) прозрачно пропускаются.
type Reader struct {
	r io.Reader

	// Текущая страница — payload + segment_table. После чтения страницы
	// пакеты из неё выдаются по одному, потом читается следующая страница.
	segTable  []byte // segment_table текущей страницы
	payload   []byte // payload текущей страницы
	segIndex  int    // индекс следующего сегмента в segTable
	pageOff   int    // смещение внутри payload (до segTable[segIndex])
	partialPk []byte // накопленные байты текущего пакета (continuation logic)

	// Гарантия BOS: пока не увидели первую страницу — флаг false. Любой пакет
	// с префиксом OpusHead/OpusTags скипается (с проверкой только по
	// префиксу payload, чтобы не зависеть от порядка страниц). Этого
	// достаточно для валидных Opus-стримов от ffmpeg.
	seenBOS bool
}

// NewReader создаёт OGG-Reader над r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// NextPacket возвращает следующий raw Opus-пакет. Служебные пакеты
// (OpusHead, OpusTags) прозрачно пропускаются — caller получает только
// audio-payload. io.EOF — конец потока.
func (r *Reader) NextPacket() ([]byte, error) {
	for {
		// Достаём следующий OGG-пакет (возможно spanning несколько страниц
		// через continuation — реализовано полностью).
		pkt, err := r.nextOGGPacket()
		if err != nil {
			return nil, err
		}
		// Пропуск OpusHead/OpusTags. Идентификация — по magic-префиксу
		// (RFC 5334 / 7895): OpusHead в первой странице BOS, OpusTags во
		// второй. На практике ffmpeg именно так и пишет, но проверяем только
		// префикс, чтобы не ломаться на слегка нестандартных потоках.
		if isOpusHead(pkt) || isOpusTags(pkt) {
			r.seenBOS = true
			continue
		}
		r.seenBOS = true
		return pkt, nil
	}
}

// nextOGGPacket достаёт следующий OGG-пакет с учётом 255-byte continuation
// logic и spanning пакетов через страницы (continuation flag).
func (r *Reader) nextOGGPacket() ([]byte, error) {
	for {
		// Если в текущей странице остались сегменты — обработаем их.
		if r.segIndex < len(r.segTable) {
			// Обрабатываем сегменты до конца пакета (сегмент <255 байт).
			for r.segIndex < len(r.segTable) {
				segLen := int(r.segTable[r.segIndex])
				r.segIndex++
				if r.pageOff+segLen > len(r.payload) {
					return nil, fmt.Errorf("ogg: segment выходит за payload (page corrupt)")
				}
				r.partialPk = append(r.partialPk, r.payload[r.pageOff:r.pageOff+segLen]...)
				r.pageOff += segLen
				if segLen < 255 {
					// Конец пакета.
					pkt := r.partialPk
					r.partialPk = nil
					return pkt, nil
				}
				// segLen == 255: пакет продолжается следующим сегментом.
			}
			// Дошли до конца segment_table, но пакет не закрыт (последний
			// сегмент ==255) — continuation на следующей странице. Читаем
			// следующую страницу.
		}
		// Текущая страница исчерпана — читаем следующую.
		if err := r.readNextPage(); err != nil {
			if r.partialPk != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return nil, fmt.Errorf("ogg: поток обрывается внутри пакета (continuation без закрывающей страницы)")
			}
			return nil, err
		}
	}
}

// readNextPage читает и разбирает следующую OGG-страницу.
func (r *Reader) readNextPage() error {
	// Заголовок фиксированного размера.
	var hdr [pageHeaderFixedSize]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		return err // io.EOF / io.ErrUnexpectedEOF — наверх
	}
	if string(hdr[:4]) != capturePattern {
		return fmt.Errorf("ogg: неверный capture pattern %q (ожидается %q)", hdr[:4], capturePattern)
	}
	segCount := int(hdr[pageSegCountOff])
	// segment_table.
	segTable := make([]byte, segCount)
	if segCount > 0 {
		if _, err := io.ReadFull(r.r, segTable); err != nil {
			return err
		}
	}
	// Payload — сумма длин сегментов.
	payloadLen := 0
	for _, s := range segTable {
		payloadLen += int(s)
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r.r, payload); err != nil {
			return err
		}
	}
	// CRC не валидируем при чтении (ffmpeg уже валидирует на decode; для
	// нашего read-only сценария пропуск битых страниц не нужен — если
	// стрим битый, следующие ошибки дадут сбой раньше).
	r.segTable = segTable
	r.payload = payload
	r.segIndex = 0
	r.pageOff = 0
	return nil
}

// isOpusHead возвращает true, если payload начинается с magic "OpusHead".
func isOpusHead(p []byte) bool { return hasPrefix(p, opusHeadMagic) }

// isOpusTags возвращает true, если payload начинается с magic "OpusTags".
func isOpusTags(p []byte) bool { return hasPrefix(p, opusTagsMagic) }

func hasPrefix(p []byte, prefix string) bool {
	if len(p) < len(prefix) {
		return false
	}
	return string(p[:len(prefix)]) == prefix
}

// ---- Writer ----

// Writer оборачивает raw Opus-пакеты в OGG-страницы. NewWriter сразу пишет
// BOS-страницу с OpusHead (19 байт) и страницу с минимальным
// vorbis-comment (vendor="nctalk", 0 комментариев). Audio-пакеты пишутся
// по одному на страницу (header_type=0). Flush() пишет EOS-страницу с
// пустым payload.
//
// Granule_position на audio-страницах аккумулирует 48к-отсчёты: приращение
// для пакета = duration * 48000 / 1e9. Поскольку точная длительность raw
// Opus-пакета требует парсинга TOC byte, консервативно фиксируем 20мс
// (960 семплов на 48к, стандартный libopus voip frame size). METADATA для
// pion (он сам считает timestamp через RTP) — между encoder и decoder
// рассинхрон не критичен.
type Writer struct {
	w          io.Writer
	serial     uint32
	pageSeq    uint32
	granule    int64
	packetsOut int // сколько audio-пакетов записали (для диагностики)
}

// NewWriter пишет BOS с OpusHead и страницу с vorbis-comment. error — если
// не удалось записать любую из них.
func NewWriter(w io.Writer) (*Writer, error) {
	// serial — произвольный; для детерминированности берём фиксированное
	// число (зашито — 1). pion/ffmpeg не требуют уникальности между
	// сессиями, только в рамках одного потока.
	wr := &Writer{w: w, serial: 1}

	// Страница 0: BOS с OpusHead.
	if err := wr.writePage(flagBOS, -1, buildOpusHead()); err != nil {
		return nil, fmt.Errorf("ogg: не удалось записать OpusHead: %w", err)
	}
	// Страница 1: vorbis-comment (минимальный).
	if err := wr.writePage(0, -1, buildOpusTags()); err != nil {
		return nil, fmt.Errorf("ogg: не удалось записать vorbis-comment: %w", err)
	}
	return wr, nil
}

// WritePacket пишет один raw Opus-пакет как отдельную audio-страницу.
func (w *Writer) WritePacket(payload []byte) error {
	// Консервативная длительность Opus-пакета = 20мс (960 семплов при 48к).
	const samplesPerPacket = 960
	w.granule += samplesPerPacket
	if err := w.writePage(0, w.granule, payload); err != nil {
		return err
	}
	w.packetsOut++
	return nil
}

// Flush пишет EOS-страницу с пустым payload (granule = последний audio-granule).
func (w *Writer) Flush() error {
	return w.writePage(flagEOS, w.granule, nil)
}

// writePage собирает OGG-страницу и пишет её в w, подставляя корректный
// CRC32. granule<0 → записывается как -1 (int64 LE).
func (w *Writer) writePage(flags byte, granule int64, payload []byte) error {
	// segment_table для одного пакета: N байт по 255, завершающий <255.
	// Если payload пустой — segment_table пустая (number_page_segments=0).
	var segTable []byte
	if len(payload) > 0 {
		// Полные 255-байтные блоки.
		n := len(payload) / 255
		// Если payload кратен 255 — нужен завершающий 0 (terminator).
		// Если нет — последний сегмент = остаток <255.
		rem := len(payload) % 255
		if rem == 0 {
			// n полных по 255 + один 0 (terminator).
			segTable = make([]byte, n+1)
			for i := 0; i < n; i++ {
				segTable[i] = 255
			}
			// segTable[n] = 0
		} else {
			segTable = make([]byte, n+1)
			for i := 0; i < n; i++ {
				segTable[i] = 255
			}
			segTable[n] = byte(rem)
		}
	}
	if len(segTable) > 255 {
		return fmt.Errorf("ogg: payload слишком велик для одной страницы (%d сегментов)", len(segTable))
	}

	// Собираем страницу целиком, считаем CRC, перезаписываем CRC-поле.
	page := make([]byte, 0, pageHeaderFixedSize+len(segTable)+len(payload))
	page = append(page, capturePattern...)
	page = append(page, 0)              // version
	page = append(page, flags)          // header_type
	var g [8]byte
	binary.LittleEndian.PutUint64(g[:], uint64(granule))
	page = append(page, g[:]...)
	var s [4]byte
	binary.LittleEndian.PutUint32(s[:], w.serial)
	page = append(page, s[:]...)
	binary.LittleEndian.PutUint32(s[:], w.pageSeq)
	page = append(page, s[:]...)
	// CRC заполним после сборки.
	page = append(page, 0, 0, 0, 0)
	page = append(page, byte(len(segTable))) // number_page_segments
	page = append(page, segTable...)
	page = append(page, payload...)

	// CRC: полином 0x04c11db7, init=0, без final-XOR, над всей страницей
	// с обнулённым CRC-полем.
	crc := pageChecksum(page)
	binary.LittleEndian.PutUint32(page[pageCRCOffset:pageCRCOffset+4], crc)

	if _, err := w.w.Write(page); err != nil {
		return err
	}
	w.pageSeq++
	return nil
}

// buildOpusHead собирает 19-байтный OpusHead (RFC 7895 §5.1).
func buildOpusHead() []byte {
	buf := make([]byte, 0, 19)
	buf = append(buf, opusHeadMagic...)
	buf = append(buf, opusHeadVersion)
	buf = append(buf, opusChannels)
	var tmp [4]byte
	binary.LittleEndian.PutUint16(tmp[:2], opusPreSkip)
	buf = append(buf, tmp[:2]...)
	binary.LittleEndian.PutUint32(tmp[:4], opusSampleRate)
	buf = append(buf, tmp[:4]...)
	binary.LittleEndian.PutUint16(tmp[:2], opusOutputGain)
	buf = append(buf, tmp[:2]...)
	buf = append(buf, opusMappingFamily)
	return buf
}

// buildOpusTags собирает минимальный vorbis-comment: "OpusTags" + vendor
// (nctalk) + 0 комментариев.
func buildOpusTags() []byte {
	buf := make([]byte, 0, 8+4+len(opusVendorString)+4)
	buf = append(buf, opusTagsMagic...)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:4], uint32(len(opusVendorString)))
	buf = append(buf, tmp[:4]...)
	buf = append(buf, opusVendorString...)
	binary.LittleEndian.PutUint32(tmp[:4], 0) // comment count = 0
	buf = append(buf, tmp[:4]...)
	return buf
}
