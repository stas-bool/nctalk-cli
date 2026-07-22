package interactive

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestVolumeWriter_Gain100_Identity(t *testing.T) {
	var inner bytes.Buffer
	v := &volumeWriter{inner: &inner}
	v.SetGain(100) // default
	pcm := []byte{0x10, 0x20, 0x30, 0x40}
	if _, err := v.Write(pcm); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(inner.Bytes(), pcm) {
		t.Errorf("gain=100: got % x, want % x (identity)", inner.Bytes(), pcm)
	}
}

func TestVolumeWriter_Gain0_Silence(t *testing.T) {
	var inner bytes.Buffer
	v := &volumeWriter{inner: &inner}
	v.SetGain(0)
	// Несколько ненулевых сэмплов.
	pcm := make([]byte, 8)
	neg1000 := int16(-1000) // переменная — чтобы uint16()-каст был non-constant (Go 1.21)
	binary.LittleEndian.PutUint16(pcm[0:2], 1000)
	binary.LittleEndian.PutUint16(pcm[2:4], uint16(neg1000))
	binary.LittleEndian.PutUint16(pcm[4:6], 32000)
	binary.LittleEndian.PutUint16(pcm[6:8], 0)
	if _, err := v.Write(pcm); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i := 0; i < inner.Len(); i++ {
		if inner.Bytes()[i] != 0 {
			t.Errorf("gain=0: byte[%d]=%d, want 0 (silence)", i, inner.Bytes()[i])
		}
	}
}

func TestVolumeWriter_Gain200_Clipping(t *testing.T) {
	var inner bytes.Buffer
	v := &volumeWriter{inner: &inner}
	v.SetGain(200)
	pcm := make([]byte, 4)
	negFullScale := int16(-32767) // переменная — чтобы uint16()-каст был non-constant (Go 1.21)
	binary.LittleEndian.PutUint16(pcm[0:2], 32767)            // +full-scale
	binary.LittleEndian.PutUint16(pcm[2:4], uint16(negFullScale)) // -full-scale
	if _, err := v.Write(pcm); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got0 := int16(binary.LittleEndian.Uint16(inner.Bytes()[0:2]))
	got1 := int16(binary.LittleEndian.Uint16(inner.Bytes()[2:4]))
	if got0 != 32767 {
		t.Errorf("+full-scale ×2: got %d, want 32767 (clipped)", got0)
	}
	if got1 != -32767 {
		t.Errorf("-full-scale ×2: got %d, want -32767 (clipped)", got1)
	}
}

func TestVolumeWriter_ClampRange(t *testing.T) {
	v := &volumeWriter{}
	v.SetGain(-10)
	if got := v.Gain(); got != 0 {
		t.Errorf("SetGain(-10) → Gain=%d, want 0 (clamped)", got)
	}
	v.SetGain(300)
	if got := v.Gain(); got != 200 {
		t.Errorf("SetGain(300) → Gain=%d, want 200 (clamped)", got)
	}
}

func TestVolumeWriter_IncDec(t *testing.T) {
	v := &volumeWriter{}
	v.SetGain(100)
	v.Inc(10)
	if got := v.Gain(); got != 110 {
		t.Errorf("Inc(10) → %d, want 110", got)
	}
	v.Dec(10)
	if got := v.Gain(); got != 100 {
		t.Errorf("Dec(10) → %d, want 100", got)
	}
	v.Dec(200)
	if got := v.Gain(); got != 0 {
		t.Errorf("Dec(200) → %d, want 0 (clamped)", got)
	}
}
