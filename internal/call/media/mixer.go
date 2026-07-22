// internal/call/media/mixer.go — Mixer: сведение N входящих PCM-потоков.
// Task 2.7. Спека 2026-07-19 §7/§9. Только stdlib (sync) — изоляция §3.

package media

import "sync"

// Mixer сводит N входящих PCM (s16le) потоков в один. Спека §7/§9:
// простое суммирование отсчётов с насыщением/clipping на ±32767.
//
// Используется в mesh (Task 2.8): на каждого remote-пира — отдельный
// FFmpegDecoder (decoder-per-peer); их PCM-выводы сводит Mixer → единый
// PCM-поток → stdout (или audio device в TUI).
//
// Контракт:
//   - NewMixer(N) фиксирует количество слотов; idx ∈ [0,N) у Push.
//   - Push(idx, pcm) хранит последний буфер каждого слота (last-write-wins).
//     nil/empty помечает слот inactive — Mix его игнорирует, но не затирает
//     данные (если тот же idx позже запушит снова — слот снова активен).
//   - idx вне [0,N) → panic (баг вызывающего, как с slice out-of-range).
//   - Mix() возвращает буфер длиной = min по активным слотам, clip на ±32767.
//     Активных нет → nil.
//
// Потокобезопасен: sync.Mutex защищает Push и Mix. Несколько decoder-горутин
// могут звать Push параллельно (каждая в свой слот); читающая горутина agent'а
// зовёт Mix. Mix возвращает «снимок» — отдельный slice, который caller волен
// менять (алиасинга с внутренним состоянием нет).
type Mixer struct {
	mu     sync.Mutex
	slots  [][]int16 // последний буфер каждого слота
	active []bool    // флаг: слот получил непустой pcm с последнего Push
}

// NewMixer создаёт Mixer с N слотами источников. Каждый слот соответствует
// одному remote-пиру (idx = идентификатор пира в agent'е).
//
// N ≤ 0 допустимо и даёт всегда-пустой Mixer (Mix → nil); практического смысла
// не имеет, но не падаем — это упрощает инициализацию в edge-кейсах agent'а.
func NewMixer(numSources int) *Mixer {
	return &Mixer{
		slots:  make([][]int16, numSources),
		active: make([]bool, numSources),
	}
}

// Push добавляет PCM-буфер от источника idx. Mixer хранит последний полученный
// буфер каждого источника; устаревшие данные НЕ переиспользуются бесконечно —
// следующий Push того же idx полностью их перезаписывает.
//
// Push с nil/empty помечает слот inactive: Mix его игнорирует. Это даёт
// источнику способ сигнализировать «я замолчал, не подмешивай мой старый буфер»
// (decoder-per-peer может слать тишину между Talk-сообщениями).
//
// idx вне [0,numSources) → panic (баг вызывающего).
//
// pcm копируется во внутреннее хранилище — caller может менять/переиспользовать
// свой slice после Push (включая из другой горутины).
func (m *Mixer) Push(idx int, pcm []int16) {
	if idx < 0 || idx >= len(m.slots) {
		// Явный panic с осмысленным сообщением — как slice out-of-range.
		panic("media: Mixer.Push idx out of range")
	}

	// Копируем — caller может переиспользовать свой slice, плюс без копии
	// возникает race между горутиной-источником и Mix (даже под mutex, т.к.
	// caller пишет в slice без ведома Mixer'а).
	stored := make([]int16, len(pcm))
	copy(stored, pcm)

	m.mu.Lock()
	m.slots[idx] = stored
	m.active[idx] = len(stored) > 0
	m.mu.Unlock()
}

// Mix возвращает сведённый PCM-буфер длиной = min по активным (непустым)
// источникам. Clipping на ±32767 (s16 saturation, спека §7/§9 — асимметричный
// диапазон). Если активных источников нет — возвращает nil.
//
// Алгоритм: out[i] = clamp(Σ src[idx][i], -32767, +32767).
//
// Возвращаемый slice — всегда свежий (caller может его менять; между двумя
// вызовами Mix алиасинга нет).
func (m *Mixer) Mix() []int16 {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Собираем активные слоты и считаем min-длину.
	minLen := -1
	for i, a := range m.active {
		if !a {
			continue
		}
		n := len(m.slots[i])
		if minLen < 0 || n < minLen {
			minLen = n
		}
	}
	if minLen <= 0 {
		// Активных нет (или все с длиной 0 — что эквивалентно тишине).
		return nil
	}

	// 2. Суммируем. Накапливаем в int32 (хоть int и хватил бы — int32 даёт
	//    явный сигнал о диапазоне; максимум N * 32767 для N≤8 укладывается
	//    в int32 с огромным запасом).
	out := make([]int32, minLen)
	for i, a := range m.active {
		if !a {
			continue
		}
		src := m.slots[i]
		for j := 0; j < minLen; j++ {
			out[j] += int32(src[j])
		}
	}

	// 3. Clamp на ±32767 + конверсия в int16.
	res := make([]int16, minLen)
	const maxV = 32767
	const minV = -32767
	for i, v := range out {
		switch {
		case v > maxV:
			res[i] = maxV
		case v < minV:
			res[i] = minV
		default:
			res[i] = int16(v)
		}
	}
	return res
}
