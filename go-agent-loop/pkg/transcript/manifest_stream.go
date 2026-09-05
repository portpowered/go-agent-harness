package transcript

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

func defaultRecordingWriteStream(path string, source io.Reader, mode os.FileMode) (int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

// redactingReader carries the final max-secret-length bytes between reads so
// credentials split across filesystem chunks are still replaced. It bounds
// memory by the read buffer plus the longest configured credential.
type redactingReader struct {
	source   io.Reader
	redactor credentialRedactor
	pending  []byte
	output   []byte
	done     bool
}

type countingReader struct {
	source    io.Reader
	bytesRead int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func newRedactingReader(source io.Reader, redactor credentialRedactor) io.Reader {
	return &redactingReader{source: source, redactor: redactor}
}

func (r *redactingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.output) == 0 && !r.done {
		buffer := make([]byte, 64*1024)
		n, err := r.source.Read(buffer)
		if n > 0 {
			combined := append(r.pending, buffer[:n]...)
			keep := r.redactor.maxSecretLength() - 1
			if keep < 0 {
				keep = 0
			}
			if len(combined) > keep {
				cut := safeRedactionCut(combined, len(combined)-keep, r.redactor.values)
				r.output = append(r.output, r.redactor.apply(combined[:cut])...)
				r.pending = append(r.pending[:0], combined[cut:]...)
			} else {
				r.pending = append(r.pending[:0], combined...)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.output = append(r.output, r.redactor.apply(r.pending)...)
				r.pending = nil
				r.done = true
			} else {
				return 0, err
			}
		}
		if n == 0 && err == nil {
			continue
		}
	}
	if len(r.output) > 0 {
		n := copy(p, r.output)
		r.output = r.output[n:]
		return n, nil
	}
	return 0, io.EOF
}

func safeRedactionCut(value []byte, limit int, secrets [][]byte) int {
	if limit <= 0 {
		return 0
	}
	cut := limit
	for _, secret := range secrets {
		for start := maxInt(0, cut-len(secret)+1); start < cut; start++ {
			end := start + len(secret)
			if end <= cut || end > len(value) {
				continue
			}
			if bytes.Equal(value[start:end-(end-cut)], secret[:cut-start]) {
				cut = start
			}
		}
	}
	return cut
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (r credentialRedactor) maxSecretLength() int {
	max := 0
	for _, secret := range r.values {
		if len(secret) > max {
			max = len(secret)
		}
	}
	return max
}

func digestRecordingFile(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil {
		return [sha256.Size]byte{}, copyErr
	}
	if closeErr != nil {
		return [sha256.Size]byte{}, closeErr
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func recordingFileContainsCredential(path string, secrets [][]byte) (bool, error) {
	maxSecret := 0
	for _, secret := range secrets {
		if len(secret) > maxSecret {
			maxSecret = len(secret)
		}
	}
	if maxSecret == 0 {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 64*1024)
	var pending []byte
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			combined := append(pending, buffer[:n]...)
			if containsCredential(combined, secrets) {
				return true, nil
			}
			keep := maxSecret - 1
			if len(combined) > keep {
				pending = append(pending[:0], combined[len(combined)-keep:]...)
			} else {
				pending = append(pending[:0], combined...)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return containsCredential(pending, secrets), nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}
