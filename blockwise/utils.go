package blockwise

import (
	"io"
)

// SeekToSize calculate size via seek.
func SeekToSize(r io.ReadSeeker) (int64, error) {
	if r == nil {
		return 0, nil
	}
	orig, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return 0, err
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = r.Seek(orig, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return size, nil
}
