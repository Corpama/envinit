package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestListAndSelectProfiles(t *testing.T) {
	manifest := releaseManifest{
		Version: "v-test",
		Profiles: []manifestProfile{
			{ID: "ubuntu22.04-x86_64", Name: "Ubuntu 22.04 x86_64", Description: "Ubuntu delivery"},
			{ID: "kylin10sp3-x86_64", Name: "Kylin V10 SP3 x86_64", Description: "Kylin delivery"},
		},
	}
	var listed strings.Builder
	printProfiles(&listed, manifest)
	if got := listed.String(); !strings.Contains(got, "ubuntu22.04-x86_64") || !strings.Contains(got, "kylin10sp3-x86_64") {
		t.Fatalf("profile list missing expected profiles:\n%s", got)
	}

	var prompt strings.Builder
	selected, err := selectProfile(manifest, "", strings.NewReader("2\n"), &prompt)
	if err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if selected.ID != "kylin10sp3-x86_64" {
		t.Fatalf("selected profile = %q, want kylin10sp3-x86_64", selected.ID)
	}
	gotPrompt := prompt.String()
	for _, want := range []string{"请选择本次交付系统", "1. Ubuntu 22.04 x86_64", "2. Kylin V10 SP3 x86_64"} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, gotPrompt)
		}
	}
}

func TestRunManifestModeDownloadsSelectedProfileAssets(t *testing.T) {
	contents := map[string][]byte{
		"/releases/v-test/env_tool-base.tar": deliveryTar(t, map[string]string{
			"env_tool/env_init":  "base binary",
			"env_tool/README.md": "base readme",
		}),
		"/releases/v-test/kylin/data.tar": deliveryTar(t, map[string]string{
			"env_tool/data/rpm-repo/repodata/repomd.xml": "kylin repo",
		}),
		"/releases/v-test/kylin/bundle.json": []byte(`{"platform":{"os_family":"kylin"}}`),
		"/releases/v-test/ubuntu/data.tar": deliveryTar(t, map[string]string{
			"env_tool/data/apt-repo/Packages": "ubuntu repo",
		}),
		"/releases/v-test/ubuntu/bundle.json": []byte(`{"platform":{"os_family":"ubuntu"}}`),
	}
	manifest := releaseManifest{
		Version: "v-test",
		Base: manifestAsset{
			Name:   "env_tool-base.tar",
			Path:   "/releases/v-test/env_tool-base.tar",
			SHA256: testSHA256(contents["/releases/v-test/env_tool-base.tar"]),
		},
		Profiles: []manifestProfile{
			{
				ID:   "ubuntu22.04-x86_64",
				Name: "Ubuntu 22.04 x86_64",
				Assets: []manifestAsset{{
					Name:   "data/ubuntu-data.tar",
					Path:   "/releases/v-test/ubuntu/data.tar",
					SHA256: testSHA256(contents["/releases/v-test/ubuntu/data.tar"]),
				}},
				Bundle: manifestAsset{
					Path:   "/releases/v-test/ubuntu/bundle.json",
					SHA256: testSHA256(contents["/releases/v-test/ubuntu/bundle.json"]),
				},
			},
			{
				ID:   "kylin10sp3-x86_64",
				Name: "Kylin V10 SP3 x86_64",
				Assets: []manifestAsset{{
					Name:   "data/kylin-data.tar",
					Path:   "/releases/v-test/kylin/data.tar",
					SHA256: testSHA256(contents["/releases/v-test/kylin/data.tar"]),
				}},
				Bundle: manifestAsset{
					Path:   "/releases/v-test/kylin/bundle.json",
					SHA256: testSHA256(contents["/releases/v-test/kylin/bundle.json"]),
				},
			},
		},
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"message":"success","data":{"token":"jwt-token"}}`))
		case "/api/fs/get":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			path, _ := body["path"].(string)
			if _, ok := contents[path]; !ok {
				t.Fatalf("unexpected AList path lookup: %s", path)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"message":"success","data":{"raw_url":"` + serverRawURL(r, path) + `"}}`))
		case "/raw":
			path := r.URL.Query().Get("path")
			content, ok := contents[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBaseURL, oldUser, oldPass, oldManifest, oldManifestB64 := alistBaseURL, alistUserB64, alistPassB64, manifestJSON, manifestJSONB64
	t.Cleanup(func() {
		alistBaseURL, alistUserB64, alistPassB64, manifestJSON, manifestJSONB64 = oldBaseURL, oldUser, oldPass, oldManifest, oldManifestB64
	})
	alistBaseURL = server.URL
	alistUserB64 = base64.StdEncoding.EncodeToString([]byte("release-reader"))
	alistPassB64 = base64.StdEncoding.EncodeToString([]byte("secret"))
	manifestJSON = string(rawManifest)

	outputDir := t.TempDir()
	var output strings.Builder
	if err := run(runOptions{
		OutputDir: outputDir,
		Profile:   "kylin10sp3-x86_64",
		Stdout:    &output,
	}); err != nil {
		t.Fatalf("run manifest mode: %v", err)
	}
	assertFileContent(t, filepath.Join(outputDir, "env_init"), "base binary")
	assertFileContent(t, filepath.Join(outputDir, "README.md"), "base readme")
	assertFileContent(t, filepath.Join(outputDir, "data", "rpm-repo", "repodata", "repomd.xml"), "kylin repo")
	assertFileContent(t, filepath.Join(outputDir, "planning", "bundle.json"), `{"platform":{"os_family":"kylin"}}`)
	if _, err := os.Stat(filepath.Join(outputDir, "data", "apt-repo", "Packages")); !os.IsNotExist(err) {
		t.Fatalf("ubuntu profile asset should not be downloaded, stat err=%v", err)
	}
	for _, archive := range []string{"env_tool-base.tar", filepath.Join("data", "kylin-data.tar")} {
		if _, err := os.Stat(filepath.Join(outputDir, archive)); !os.IsNotExist(err) {
			t.Fatalf("assembled archive %s should be removed, stat err=%v", archive, err)
		}
	}
	if !strings.Contains(output.String(), "profile kylin10sp3-x86_64") {
		t.Fatalf("expected selected profile in output, got:\n%s", output.String())
	}
}

func TestExtractDeliveryTarRejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "escape.tar.gz")
	data := deliveryTarGzip(t, map[string]string{"../outside": "bad"})
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractDeliveryTar(archive, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), "escapes the delivery directory") {
		t.Fatalf("extract traversal error = %v", err)
	}
}

func deliveryTarGzip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	raw := deliveryTar(t, files)
	var out bytes.Buffer
	gzipWriter := gzip.NewWriter(&out)
	if _, err := gzipWriter.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func deliveryTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	tarWriter := tar.NewWriter(&out)
	for name, content := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(content)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestEmbeddedManifestSupportsBase64(t *testing.T) {
	oldManifest, oldManifestB64 := manifestJSON, manifestJSONB64
	t.Cleanup(func() {
		manifestJSON, manifestJSONB64 = oldManifest, oldManifestB64
	})
	manifestJSON = ""
	manifestJSONB64 = base64.StdEncoding.EncodeToString([]byte(`{"version":"v1","profiles":[{"id":"ubuntu22.04-x86_64"}]}`))

	raw, err := embeddedManifest()
	if err != nil {
		t.Fatalf("embedded manifest: %v", err)
	}
	if !strings.Contains(raw, "ubuntu22.04-x86_64") {
		t.Fatalf("decoded manifest = %q", raw)
	}
}

func TestAssetOutputPathRejectsEscapes(t *testing.T) {
	for _, name := range []string{"/tmp/bundle.json", "../bundle.json", "."} {
		if _, err := assetOutputPath(t.TempDir(), name); err == nil {
			t.Fatalf("expected asset name %q to be rejected", name)
		}
	}
}

func testSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func serverRawURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/raw?path=" + url.QueryEscape(path)
}
