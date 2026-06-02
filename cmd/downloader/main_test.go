package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDownloadURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["username"] != "release-reader" || body["password"] != "secret" {
				t.Fatalf("login body = %#v", body)
			}
			w.Write([]byte(`{"code":200,"message":"success","data":{"token":"jwt-token"}}`))
		case "/api/fs/get":
			if got := r.Header.Get("Authorization"); got != "jwt-token" {
				t.Fatalf("Authorization = %q, want jwt-token", got)
			}
			w.Write([]byte(`{"code":200,"message":"success","data":{"raw_url":"https://download.example/env_tool.tar"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBaseURL, oldFilePath := alistBaseURL, alistFilePath
	oldUser, oldPass := alistUserB64, alistPassB64
	t.Cleanup(func() {
		alistBaseURL, alistFilePath = oldBaseURL, oldFilePath
		alistUserB64, alistPassB64 = oldUser, oldPass
	})
	alistBaseURL = server.URL
	alistFilePath = "/releases/v-test/env_tool-v-test.tar"
	alistUserB64 = base64.StdEncoding.EncodeToString([]byte("release-reader"))
	alistPassB64 = base64.StdEncoding.EncodeToString([]byte("secret"))

	got, err := resolveDownloadURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://download.example/env_tool.tar" {
		t.Fatalf("download URL = %q", got)
	}
}

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

func TestDownloadReportsJSONErrorWithoutWritingOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"code":401,"message":"sign invalid","data":null}`))
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "env_tool.tar.part")
	err := download(output, server.URL)
	if err == nil || !strings.Contains(err.Error(), "sign invalid") {
		t.Fatalf("download error = %v, want sign invalid", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output should not be created, stat error = %v", err)
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
