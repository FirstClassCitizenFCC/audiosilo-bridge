// Package progress wraps an io.Reader or io.Writer to report byte progress to a
// callback, throttled so a large copy doesn't flood the caller (a job's progress
// updates). The Audible download (Reader) and the file transfer (Writer) share it
// so they report at the same cadence.
package progress

import "io"

// EmitEvery caps callbacks to at most one per this many bytes (plus a guaranteed
// final callback), so a multi-hundred-MB copy reports a few hundred times instead of
// once per ~32 KiB chunk.
const EmitEvery = 1 << 20

// Reader counts bytes read from R and reports (copied, total) to CB, throttled. Total
// is the expected size, or -1 when unknown; a final callback still fires at EOF (or on
// a read error). CB must be non-nil.
type Reader struct {
	R        io.Reader
	Total    int64
	CB       func(copied, total int64)
	read     int64
	lastEmit int64
}

func (r *Reader) Read(b []byte) (int, error) {
	n, err := r.R.Read(b)
	r.read += int64(n)
	if r.read-r.lastEmit >= EmitEvery || err != nil {
		r.lastEmit = r.read
		r.CB(r.read, r.Total)
	}
	return n, err
}

// Writer counts bytes written to W and reports (copied, total) to CB, throttled, with
// a guaranteed final callback once Total bytes have been written. CB must be non-nil.
type Writer struct {
	W        io.Writer
	Total    int64
	CB       func(copied, total int64)
	copied   int64
	lastEmit int64
}

func (w *Writer) Write(b []byte) (int, error) {
	n, err := w.W.Write(b)
	w.copied += int64(n)
	// Emit per throttle window, plus one guaranteed final callback the moment Total
	// is reached (the last chunk may be small). The lastEmit < Total guard fires that
	// final callback only once - the Writer's completion signal is Total, whereas the
	// Reader's is EOF (an error), which is why their conditions differ.
	if w.copied-w.lastEmit >= EmitEvery || (w.copied >= w.Total && w.lastEmit < w.Total) {
		w.lastEmit = w.copied
		w.CB(w.copied, w.Total)
	}
	return n, err
}
