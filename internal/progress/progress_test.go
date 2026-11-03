package progress

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReaderThrottlesButAlwaysReportsFinal(t *testing.T) {
	total := int64(EmitEvery*2 + 500) // two full windows + a remainder
	var calls int
	var last int64
	r := &Reader{
		R:     strings.NewReader(strings.Repeat("x", int(total))),
		Total: total,
		CB:    func(copied, _ int64) { calls++; last = copied },
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if last != total {
		t.Errorf("final copied = %d, want %d", last, total)
	}
	// Throttled: far fewer callbacks than 32 KiB chunks would produce, but at least
	// one per window plus the final.
	if calls < 2 || calls > 10 {
		t.Errorf("callback count = %d, want a small throttled number", calls)
	}
}

func TestWriterReportsFinalEvenWhenLastChunkIsSmall(t *testing.T) {
	total := int64(EmitEvery + 10)
	var last int64
	w := &Writer{W: &bytes.Buffer{}, Total: total, CB: func(copied, _ int64) { last = copied }}
	// Write the whole thing in one big chunk then a tiny tail - the tail must still
	// trigger a final 100% callback even though it doesn't cross the throttle window.
	if _, err := w.Write(bytes.Repeat([]byte("y"), int(EmitEvery))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if last != total {
		t.Errorf("final copied = %d, want %d", last, total)
	}
}
