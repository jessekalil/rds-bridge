package state

import (
	"io"
	"os"
	"time"
)

// tailLines is how many trailing lines Tail prints before following.
const tailLines = 200

// pollInterval is how often Tail checks a followed file for new data.
const pollInterval = 500 * time.Millisecond

// Tail writes the last tailLines lines of the file at path to w. When follow is
// true it then blocks, streaming new data as the file grows and re-reading from
// the start if the file is truncated or rotated. It is a portable replacement
// for shelling out to `tail`.
func Tail(path string, follow bool, w io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := w.Write(lastLines(data, tailLines)); err != nil {
		return err
	}
	if !follow {
		return nil
	}

	offset := int64(len(data))
	for {
		time.Sleep(pollInterval)

		fi, err := os.Stat(path)
		if err != nil {
			// File temporarily gone (rotation); keep waiting.
			continue
		}
		size := fi.Size()
		if size < offset {
			// Truncated or rotated: restart from the beginning.
			offset = 0
		}
		if size == offset {
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		n, err := io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
		offset += n
	}
}

// lastLines returns the last n lines of data (including a trailing newline).
func lastLines(data []byte, n int) []byte {
	if len(data) == 0 {
		return data
	}
	// Trim a single trailing newline so it doesn't count as an empty line.
	end := len(data)
	trimmed := data
	if trimmed[end-1] == '\n' {
		trimmed = trimmed[:end-1]
	}
	count := 0
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] == '\n' {
			count++
			if count == n {
				return data[i+1:]
			}
		}
	}
	return data
}
