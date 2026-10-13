package main

import (
	"io"
	"os"
	"testing"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	// Read while fn runs. A pipe nobody is draining stops accepting writes long
	// before a command's output ends -- after 512 bytes on macOS -- so reading
	// only once fn has returned deadlocks on anything that prints more.
	type captured struct {
		out []byte
		err error
	}
	drained := make(chan captured, 1)
	go func() {
		out, err := io.ReadAll(r)
		drained <- captured{out, err}
	}()

	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	got := <-drained
	if got.err != nil {
		t.Fatalf("read stdout: %v", got.err)
	}
	return string(got.out), runErr
}
