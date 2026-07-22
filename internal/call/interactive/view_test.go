package interactive

import "testing"

func TestByteToEvent_Table(t *testing.T) {
	cases := []struct {
		b    byte
		want Event
		ok   bool
	}{
		{'m', EvMuteToggle, true},
		{'M', EvMuteToggle, true},
		{'q', EvLeave, true},
		{'Q', EvLeave, true},
		{0x03, EvLeave, true}, // Ctrl-C
		{'+', EvVolUp, true},
		{'-', EvVolDown, true},
		{'x', 0, false}, // прочее
		{'\n', 0, false},
	}
	for _, tc := range cases {
		got, ok := byteToEvent(tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("byteToEvent(%q) = (%v, %v), want (%v, %v)", tc.b, got, ok, tc.want, tc.ok)
		}
	}
}
