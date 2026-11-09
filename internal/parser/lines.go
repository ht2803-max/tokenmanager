// Package parser turns agent log files into normalized usage records.
//
// Parsers never see conversation content beyond what is needed to locate
// usage fields; message text is never returned or stored.
package parser

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// State is per-file context carried between incremental parses (for example
// the current model in a Codex rollout, which is announced once and then
// applies to every following usage event).
type State map[string]string

// LineFunc handles one complete JSONL line.
type LineFunc func(line []byte)

// ReadLines calls fn for each complete newline-terminated line in r and
// returns the number of bytes consumed. A trailing partial line (a file that
// is still being written) is not consumed so it can be re-read later.
func ReadLines(r io.Reader, fn LineFunc) (int64, error) {
	br := bufio.NewReaderSize(r, 256*1024)
	var consumed int64
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
		consumed += int64(len(line))
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		fn(line)
	}
}

// ParseTime accepts RFC3339 strings and unix seconds/milliseconds.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return UnixAuto(n), true
	}
	return time.Time{}, false
}

// UnixAuto converts a unix timestamp in seconds or milliseconds.
func UnixAuto(n float64) time.Time {
	if n > 1e12 {
		return time.UnixMilli(int64(n))
	}
	return time.Unix(int64(n), int64((n-float64(int64(n)))*1e9))
}

// TailSize is how much of a session file status probes read from the end.
const TailSize = 64 * 1024

// ReadTail returns up to max bytes from the end of the file at path. When the
// read starts mid-file, the first (partial) line is dropped so the result
// holds whole lines only.
func ReadTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	return trimPartialHead(buf, start > 0), nil
}

// trimPartialHead drops everything up to the first newline when the buffer
// starts in the middle of a line.
func trimPartialHead(b []byte, midFile bool) []byte {
	if !midFile {
		return b
	}
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[i+1:]
	}
	return nil // one huge line with no start in view
}
