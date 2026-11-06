// Package sse provides incremental framing for server-sent events.
package sse

// Framer buffers incomplete events and preserves their original bytes, including
// line endings. Its zero value is ready to use.
type Framer struct {
	buffer    []byte
	scanned   int
	lineStart int
	pendingLF bool
}

// Feed returns events terminated by a blank line. CR ends a line immediately;
// the optional LF is consumed without creating another line. If that LF arrives
// after its event was emitted, it is returned as a separate whitespace frame to
// preserve the original bytes without delaying CR-only streams.
func (f *Framer) Feed(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	f.buffer = append(f.buffer, chunk...)
	var events [][]byte
	start := 0
	if f.pendingLF {
		f.pendingLF = false
		if f.buffer[f.scanned] == '\n' {
			f.scanned++
			f.lineStart = f.scanned
			if f.scanned == 1 {
				events = append(events, f.buffer[:1])
				start = 1
			}
		}
	}
	for f.scanned < len(f.buffer) {
		pos := f.scanned
		switch f.buffer[pos] {
		case '\r':
			f.scanned++
			if f.scanned == len(f.buffer) {
				f.pendingLF = true
			} else if f.buffer[f.scanned] == '\n' {
				f.scanned++
			}
		case '\n':
			f.scanned++
		default:
			f.scanned++
			continue
		}
		if pos == f.lineStart {
			events = append(events, f.buffer[start:f.scanned])
			start = f.scanned
		}
		f.lineStart = f.scanned
	}
	return f.consume(events, start)
}

func (f *Framer) consume(events [][]byte, start int) [][]byte {
	f.buffer = f.buffer[start:]
	f.scanned -= start
	f.lineStart -= start
	if len(f.buffer) == 0 {
		f.buffer = nil
	}
	return events
}

// Flush returns the remaining bytes, including an unterminated final event,
// and resets the framer.
func (f *Framer) Flush() []byte {
	out := f.buffer
	*f = Framer{}
	return out
}
