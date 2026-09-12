package service

import (
	"errors"
	"io"
	"sync"
)

type output struct {
	writer io.Writer

	mu  sync.Mutex
	err error
}

func (o *output) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return 0, o.err
	}
	if o.writer == nil {
		o.err = errors.New("textseed: nil output writer")
		return 0, o.err
	}

	n, err := o.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.err = err
	}
	return n, err
}

func (o *output) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}
