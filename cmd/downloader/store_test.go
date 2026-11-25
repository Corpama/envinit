package main

import (
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"envinit/internal/cosstore"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func TestCOSIndexedDeliveryResumesAndVerifiesObject(t *testing.T) {
	const object = "repo/a+b #1.bin"
	const payload = "abcdefghij"
	const root = "env_init/data/profiles/test"
	catalog, err := json.Marshal([]cosstore.Entry{{Path: object, Size: int64(len(payload)), ETag: `"revision"`}})
	if err != nil {
		t.Fatal(err)
	}
	profile := manifestProfile{ID: "test", MaterialRoot: root, MaterialIndex: manifestAsset{Path: "r/materials.json", SHA256: testSHA256(catalog)}}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/r/materials.json":
			w.Write(catalog)
		case "/" + root + "/" + object:
			calls.Add(1)
			if r.Header.Get("If-Match") != `"revision"` {
				t.Error("missing If-Match")
			}
			w.Header().Set("ETag", `"revision"`)
			w.Header().Set("x-cos-hash-crc64ecma", fmt.Sprint(crc64.Checksum([]byte(payload), crc64.MakeTable(crc64.ECMA))))
			if offset := r.Header.Get("Range"); offset != "" {
				if offset != "bytes=3-" {
					t.Errorf("unexpected Range: %s", offset)
				}
				w.Header().Set("Content-Range", "bytes 3-9/10")
				w.WriteHeader(206)
				io.WriteString(w, payload[3:])
			} else {
				io.WriteString(w, payload)
			}
		default:
			t.Errorf("unexpected URL: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	store := deliveryStore{cos: cos.NewClient(&cos.BaseURL{BucketURL: u}, server.Client())}
	paths := projectPaths{OutputDir: t.TempDir(), BundleTemplate: filepath.Join(t.TempDir(), "bundle.json")}
	out := filepath.Join(paths.OutputDir, "data", object)
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		t.Fatal(err)
	}
	run := func() error { return downloadProfileMaterials(paths, profile, store, 2, io.Discard, nil) }
	for _, partial := range []string{"bad", payload[:3]} {
		if err := os.WriteFile(out+".part", []byte(partial), 0644); err != nil {
			t.Fatal(err)
		}
		err := run()
		if partial == "bad" {
			if err == nil || !strings.Contains(err.Error(), "CRC64 mismatch") {
				t.Fatalf("corrupt resumed download: %v", err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("installed corrupt material: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	assertFileContent(t, out, payload)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("material GETs=%d, want 2", n)
	}
	profile.MaterialIndex.SHA256 = strings.Repeat("0", 64)
	if err := run(); err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("invalid manifest digest: %v", err)
	}
}
