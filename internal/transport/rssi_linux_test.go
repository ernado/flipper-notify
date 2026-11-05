package transport

import "testing"

func TestParseBDAddr(t *testing.T) {
	a, err := parseBDAddr("AA:BB:CC:DD:EE:0f")
	if err != nil {
		t.Fatal(err)
	}
	if a != [6]byte{0x0f, 0xee, 0xdd, 0xcc, 0xbb, 0xaa} {
		t.Fatalf("got % x", a)
	}
	for _, bad := range []string{"", "AA:BB:CC:DD:EE", "AA:BB:CC:DD:EE:FG", "AAA:B:CC:DD:EE:FF"} {
		if _, err := parseBDAddr(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
