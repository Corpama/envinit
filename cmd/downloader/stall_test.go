package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type blockingReader struct {
	released chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.released
	return 0, io.EOF
}

func TestStallReaderAbortsWhenNoData(t *testing.T) {
	blocker := &blockingReader{released: make(chan struct{})}
	var canceled bool
	r := &stallReader{
		inner:   blocker,
		timeout: 100 * time.Millisecond,
		cancel:  func() { canceled = true; close(blocker.released) },
	}
	buf := make([]byte, 8)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected stall error")
	}
	if !r.stalled() {
		t.Fatal("stalled() should report true after timeout")
	}
	if !canceled {
		t.Fatal("cancel should fire on stall")
	}
}

func TestStallReaderPassesThroughData(t *testing.T) {
	r := &stallReader{
		inner:   strings.NewReader("hello"),
		timeout: time.Second,
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	if r.stalled() {
		t.Fatal("stalled() should be false on normal read")
	}
}

func TestStdinIsNonInteractiveFileDetectsPipe(t *testing.T) {
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer rp.Close()
	defer wp.Close()
	if !stdinIsNonInteractiveFile(rp) {
		t.Fatal("a pipe should be reported as non-interactive")
	}
	if stdinIsNonInteractiveFile(strings.NewReader("x")) {
		t.Fatal("non-*os.File readers must be treated as answerable")
	}
}
