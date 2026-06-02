package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadAndVerify(t *testing.T) {
	content := []byte("complete env_tool package")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "env_tool.tar")
	if err := download(output, server.URL); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if err := verifySHA256(output, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadResumesPartialFile(t *testing.T) {
	content := []byte("complete env_tool package")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=9-" {
			t.Fatalf("Range header = %q, want %q", got, "bytes=9-")
		}
		w.Header().Set("Content-Range", "bytes 9-24/25")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[9:])
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "env_tool.tar")
	if err := os.WriteFile(output, content[:9], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download(output, server.URL); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
}
