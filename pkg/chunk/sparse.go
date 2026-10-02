package chunk

import (
	"fmt"
	"io"
)

// sparseWriter writes sequentially from off into w but skips every
// holeAlign-aligned block that is entirely zero, so a freshly truncated
// destination stays sparse. The destination range must already read as
// zeros.
type sparseWriter struct {
	w   io.WriterAt
	off int64
}

func (s *sparseWriter) Write(p []byte) (int, error) {
	total := len(p)
	runStart, runOff := -1, int64(0)
	flush := func(end int) error {
		if runStart < 0 {
			return nil
		}
		if _, err := s.w.WriteAt(p[runStart:end], runOff); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		runStart = -1
		return nil
	}
	for i := 0; i < len(p); {
		n := int(holeAlign - (s.off+int64(i))%holeAlign)
		n = min(n, len(p)-i)
		block := p[i : i+n]
		if isZero(block) {
			if err := flush(i); err != nil {
				return 0, err
			}
		} else if runStart < 0 {
			runStart, runOff = i, s.off+int64(i)
		}
		i += n
	}
	if err := flush(len(p)); err != nil {
		return 0, err
	}
	s.off += int64(total)
	return total, nil
}
