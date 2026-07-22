package media

import (
	"sync"
	"testing"
)

// Тесты для Mixer (Task 2.7). Спека §7/§9: сведение N входящих PCM (s16le)
// потоков в один с насыщением/clipping на ±32767.
//
// Сценарии (бриф Task 2.7):
//  1. TestMixer_OneSource          — один источник → копия без изменений.
//  2. TestMixer_TwoSources_Sum     — два источника → суммирование отсчётов.
//  3. TestMixer_Clipping           — переполнение int16 → clip на 32767.
//  4. TestMixer_NoActive           — все слоты пустые → Mix возвращает nil.
//  5. TestMixer_DifferentLengths   — буферы 100 и 80 → выход 80 (min).
//  6. TestMixer_OverwriteStaleData — перезапись слота: последние данные выигрывают.
//  7. TestMixer_Parallel           — N пишущих горутин + читающая Mix (-race).
//  8. TestMixer_PushOutOfRange     — idx вне [0,N) → panic (баг вызывающего).
//  9. TestMixer_SilenceMarksInactive — Push(nil/empty) → слот становится неактивным.

// ---- helpers ----

// int16SliceFrom заполняет []int16 значениями из fn(i).
func int16SliceFrom(n int, fn func(i int) int16) []int16 {
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = fn(i)
	}
	return out
}

// slicesEqual сравнивает два []int16 (nil и пустой считаем разными —
// тесты явно проверяют, что NoActive → nil, а один активный → непустой буфер).
func slicesEqual(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	// nil vs empty: различаем только если это принципиально (для наших тестов
	// достаточно длины — nil и len==0 считаем эквивалентами, кроме NoActive,
	// где явно проверяем == nil).
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- Тесты ----

// TestMixer_OneSource — один активный источник → Mix возвращает его данные
// как есть (без суммирования, но с копией — caller может менять свой буфер).
func TestMixer_OneSource(t *testing.T) {
	const N = 1
	in := []int16{100, -200, 300, -400, 500}

	m := NewMixer(N)
	m.Push(0, in)
	got := m.Mix()

	if len(got) != len(in) {
		t.Fatalf("len(out) = %d, want %d", len(got), len(in))
	}
	if !slicesEqual(got, in) {
		t.Errorf("out = %v, want %v (один источник → копия без изменений)", got, in)
	}

	// Изменяем исходный буфер — Mix должен был сделать копию (last-write-wins
	// не означает shared-slice).
	in[0] = 9999
	if got[0] == 9999 {
		t.Error("Mix вернул shared-slice — должен копировать (aliasing приведёт к race)")
	}
}

// TestMixer_TwoSources_Sum — два источника → сумма отсчётов.
func TestMixer_TwoSources_Sum(t *testing.T) {
	const N = 2
	a := []int16{10, 20, 30, 40}
	b := []int16{1, 2, 3, 4}
	want := []int16{11, 22, 33, 44}

	m := NewMixer(N)
	m.Push(0, a)
	m.Push(1, b)
	got := m.Mix()

	if !slicesEqual(got, want) {
		t.Errorf("out = %v, want %v (сумма отсчётов)", got, want)
	}
}

// TestMixer_Clipping — две амплитуды по 20000 → переполнение int16 → clip
// на ±32767 (не overflow wrap-around, не +32768).
func TestMixer_Clipping(t *testing.T) {
	const N = 2
	// 20000 + 20000 = 40000 → clip → 32767.
	a := []int16{20000, -20000, 32767, -32768}
	b := []int16{20000, -20000, 1, -1}
	// clamp на [-32767, 32767] (асимметричный по спеке §7/§9).
	want := []int16{32767, -32767, 32767, -32767}

	m := NewMixer(N)
	m.Push(0, a)
	m.Push(1, b)
	got := m.Mix()

	if !slicesEqual(got, want) {
		t.Errorf("out = %v, want %v (clip на ±32767)", got, want)
	}
}

// TestMixer_NoActive — ни один слот не получил данных → Mix возвращает nil.
func TestMixer_NoActive(t *testing.T) {
	m := NewMixer(3)
	if got := m.Mix(); got != nil {
		t.Errorf("Mix() = %v, want nil (нет активных источников)", got)
	}
}

// TestMixer_DifferentLengths — буферы 100 и 80 → выход 80 (min по активным).
func TestMixer_DifferentLengths(t *testing.T) {
	const N = 2
	a := int16SliceFrom(100, func(i int) int16 { return int16(i) })
	b := int16SliceFrom(80, func(i int) int16 { return int16(i * 2) })

	want := int16SliceFrom(80, func(i int) int16 { return int16(i + i*2) })

	m := NewMixer(N)
	m.Push(0, a)
	m.Push(1, b)
	got := m.Mix()

	if len(got) != 80 {
		t.Fatalf("len(out) = %d, want 80 (min по активным)", len(got))
	}
	if !slicesEqual(got, want) {
		t.Errorf("out[0..5] = %v..., want %v... (сумма с обрезкой по короткому)", got[:5], want[:5])
	}
}

// TestMixer_OverwriteStaleData — push 100 отсчётов в слот 0, потом push 50
// отсчётов в тот же слот → Mix использует последние 50 (не первые 100).
// Спека §7: last-write-wins per slot, устаревшие данные не переиспользуются.
func TestMixer_OverwriteStaleData(t *testing.T) {
	const N = 1
	first := int16SliceFrom(100, func(i int) int16 { return 1000 })
	second := int16SliceFrom(50, func(i int) int16 { return 7 })

	m := NewMixer(N)
	m.Push(0, first)
	m.Push(0, second)
	got := m.Mix()

	if len(got) != 50 {
		t.Fatalf("len(out) = %d, want 50 (перезапись: последние данные выигрывают)", len(got))
	}
	for i, v := range got {
		if v != 7 {
			t.Errorf("out[%d] = %d, want 7 (stale data от первого Push утёкли)", i, v)
		}
	}
}

// TestMixer_Parallel — N горутин пишут каждый в свой слот, одна горутина
// читает Mix. Должно проходить под `go test -race` (sync.Mutex защищает
// и Push, и Mix).
func TestMixer_Parallel(t *testing.T) {
	const N = 8
	const iters = 200

	m := NewMixer(N)
	var wg sync.WaitGroup

	// N пишущих горутин — каждая в свой слот.
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			buf := int16SliceFrom(32, func(j int) int16 { return int16(idx + j) })
			for k := 0; k < iters; k++ {
				m.Push(idx, buf)
			}
		}(i)
	}

	// Читающая горутина — зовёт Mix параллельно с Push.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; k < iters; k++ {
			_ = m.Mix() // не проверяем значения — только отсутствие race/deadlock
		}
	}()

	wg.Wait()

	// Финальный Mix должен дать согласованный результат: либо nil (если все
	// замолчали), либо буфер длиной 32. В нашем случае все слоты активны.
	got := m.Mix()
	if got == nil {
		t.Fatal("финальный Mix = nil, want len=32 (все слоты активны после Push)")
	}
	if len(got) != 32 {
		t.Errorf("len(финальный Mix) = %d, want 32", len(got))
	}
}

// TestMixer_PushOutOfRange — Push с idx вне [0, numSources) → panic
// (баг вызывающего, как с slice out-of-range).
func TestMixer_PushOutOfRange(t *testing.T) {
	m := NewMixer(2)
	defer func() {
		if r := recover(); r == nil {
			t.Error("Push(-1, ...) не запаниковал — должен (idx вне диапазона = баг)")
		}
	}()
	m.Push(-1, []int16{1})
}

// TestMixer_PushOutOfRange_High — симметричный случай: idx == numSources.
func TestMixer_PushOutOfRange_High(t *testing.T) {
	m := NewMixer(2)
	defer func() {
		if r := recover(); r == nil {
			t.Error("Push(numSources, ...) не запаниковал — должен")
		}
	}()
	m.Push(2, []int16{1})
}

// TestMixer_SilenceMarksInactive — Push с nil/empty помечает слот как паузу:
// даже если до этого были данные, Mix должен их игнорировать.
//
// Два варианта:
//   - сначала Push данных → Mix даёт буфер; потом Push(nil) → Mix даёт nil.
//   - если тот же idx позже запушит снова — слот снова активен.
func TestMixer_SilenceMarksInactive(t *testing.T) {
	m := NewMixer(2)

	// Шаг 1: оба активны, разные длины.
	m.Push(0, []int16{10, 20, 30})
	m.Push(1, []int16{1, 2, 3})
	if got := m.Mix(); !slicesEqual(got, []int16{11, 22, 33}) {
		t.Fatalf("шаг 1: Mix = %v, want [11 22 33]", got)
	}

	// Шаг 2: слот 1 замолчал → Mix должен дать только слот 0 (длина 3).
	m.Push(1, nil)
	if got := m.Mix(); !slicesEqual(got, []int16{10, 20, 30}) {
		t.Errorf("шаг 2: Mix = %v, want [10 20 30] (слот 1 inactive)", got)
	}

	// Шаг 3: слот 1 снова активен — данные суммируются снова.
	m.Push(1, []int16{100})
	if got := m.Mix(); !slicesEqual(got, []int16{110}) {
		t.Errorf("шаг 3: Mix = %v, want [110] (слот 1 реактивирован)", got)
	}

	// Шаг 4: оба замолчали → nil.
	m.Push(0, []int16{})
	m.Push(1, nil)
	if got := m.Mix(); got != nil {
		t.Errorf("шаг 4: Mix = %v, want nil (оба inactive)", got)
	}
}
