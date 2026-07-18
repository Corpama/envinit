package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestMaterialSHA256AcceptsAListHashInfoShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "new object field", raw: `{"hash_info":{"sha256":"ABCDEF"}}`, want: "abcdef"},
		{name: "legacy object field", raw: `{"hashinfo":{"SHA256":"ABCDEF"}}`, want: "abcdef"},
		{name: "legacy JSON string", raw: `{"hashinfo":"{\"sha256\":\"ABCDEF\"}"}`, want: "abcdef"},
		{name: "legacy key value string", raw: `{"hashinfo":"sha256:ABCDEF"}`, want: "abcdef"},
		{name: "legacy null string", raw: `{"hashinfo":"null"}`},
		{name: "null", raw: `{"hashinfo":null}`},
		{name: "unsupported value is ignored", raw: `{"hashinfo":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var entry alistMaterialEntry
			if err := json.Unmarshal([]byte(tt.raw), &entry); err != nil {
				t.Fatal(err)
			}
			if got := materialSHA256(entry); got != tt.want {
				t.Fatalf("materialSHA256() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadMaterialFileDoesNotSkipMissingNonEmptyFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"message":"success","data":{"raw_url":"` + serverRawURL(r, "/raw") + `"}}`))
		case "/raw":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldBaseURL := alistBaseURL
	t.Cleanup(func() { alistBaseURL = oldBaseURL })
	alistBaseURL = server.URL
	err := downloadMaterialFile(t.TempDir(), "test-profile", "token", materialFile{
		RemotePath:   "/data/profiles/test/missing.rpm",
		RelativePath: "missing.rpm",
		Size:         1024,
	}, nil)
	if err == nil {
		t.Fatal("downloadMaterialFile() succeeded for a missing non-empty file")
	}
	if errors.Is(err, errSkipStaleZeroSizeMaterial) {
		t.Fatalf("downloadMaterialFile() skipped non-empty file: %v", err)
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

func TestRunManifestModeSupportsLegacyProfileArchives(t *testing.T) {
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

func TestRunManifestModeDownloadsAndAssemblesMaterialDirectory(t *testing.T) {
	contents := map[string][]byte{
		"/releases/v-test/env_tool-base.tar": deliveryTar(t, map[string]string{
			"env_tool/env_init":  "base binary",
			"env_tool/README.md": "base readme",
		}),
		"/releases/v-test/kylin/bundle.json":                []byte(`{"platform":{"os_family":"kylin"}}`),
		"/data/profiles/kylin/rpm-repo/repodata/repomd.xml": []byte("kylin repo"),
		"/data/profiles/kylin/misc/install.sh":              []byte("#!/bin/sh\necho install\n"),
	}
	const staleZeroSizePath = "/data/profiles/kylin/misc/stale-link"
	manifest := releaseManifest{
		Version: "v-test",
		Base: manifestAsset{
			Name:   "env_tool-base.tar",
			Path:   "/releases/v-test/env_tool-base.tar",
			SHA256: testSHA256(contents["/releases/v-test/env_tool-base.tar"]),
		},
		Profiles: []manifestProfile{{
			ID:           "kylin10sp3-x86_64",
			Name:         "Kylin V10 SP3 x86_64",
			MaterialRoot: "/data/profiles/kylin",
			Bundle: manifestAsset{
				Path:   "/releases/v-test/kylin/bundle.json",
				SHA256: testSHA256(contents["/releases/v-test/kylin/bundle.json"]),
			},
		}},
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var materialRawDownloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"message":"success","data":{"token":"jwt-token"}}`))
		case "/api/fs/list":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			directory, _ := body["path"].(string)
			var content []map[string]any
			switch directory {
			case "/data/profiles/kylin":
				content = []map[string]any{
					{"name": "rpm-repo", "is_dir": true, "hashinfo": "null"},
					{"name": "misc", "is_dir": true},
					{"name": ".DS_Store", "size": 9, "is_dir": false},
				}
			case "/data/profiles/kylin/rpm-repo":
				content = []map[string]any{{"name": "repodata", "is_dir": true}}
			case "/data/profiles/kylin/rpm-repo/repodata":
				file := contents["/data/profiles/kylin/rpm-repo/repodata/repomd.xml"]
				content = []map[string]any{{"name": "repomd.xml", "size": len(file), "modified": "2026-07-18T01:02:03Z", "hash_info": map[string]string{"sha256": testSHA256(file)}}}
			case "/data/profiles/kylin/misc":
				file := contents["/data/profiles/kylin/misc/install.sh"]
				content = []map[string]any{
					{"name": "install.sh", "size": len(file), "modified": "2026-07-18T01:02:04Z"},
					{"name": "stale-link", "size": 0, "modified": "2026-07-18T01:02:05Z", "hashinfo": "null"},
				}
			default:
				t.Fatalf("unexpected AList directory: %s", directory)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success", "data": map[string]any{"content": content, "total": len(content)}})
		case "/api/fs/get":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			filePath, _ := body["path"].(string)
			if _, ok := contents[filePath]; !ok && filePath != staleZeroSizePath {
				t.Fatalf("unexpected AList path lookup: %s", filePath)
			}
			if strings.HasPrefix(filePath, "/data/profiles/") {
				if refresh, _ := body["refresh"].(bool); refresh {
					http.Error(w, "material file lookup should reuse the refreshed directory metadata", http.StatusBadRequest)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"message":"success","data":{"raw_url":"` + serverRawURL(r, filePath) + `"}}`))
		case "/raw":
			filePath := r.URL.Query().Get("path")
			content, ok := contents[filePath]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(filePath, "/data/profiles/") {
				materialRawDownloads.Add(1)
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
	opts := runOptions{OutputDir: outputDir, Profile: "kylin10sp3-x86_64", Jobs: 2, Stdout: &output}
	if err := run(opts); err != nil {
		t.Fatalf("run material profile mode: %v", err)
	}
	assertFileContent(t, filepath.Join(outputDir, "env_init"), "base binary")
	assertFileContent(t, filepath.Join(outputDir, "planning", "bundle.json"), `{"platform":{"os_family":"kylin"}}`)
	assertFileContent(t, filepath.Join(outputDir, "data", "rpm-repo", "repodata", "repomd.xml"), "kylin repo")
	assertFileContent(t, filepath.Join(outputDir, "data", "misc", "install.sh"), "#!/bin/sh\necho install\n")
	if info, err := os.Stat(filepath.Join(outputDir, "data", "misc", "install.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("material shell script should be executable, info=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "data", ".DS_Store")); !os.IsNotExist(err) {
		t.Fatalf(".DS_Store should be skipped, stat err=%v", err)
	}
	if got := materialRawDownloads.Load(); got != 2 {
		t.Fatalf("material raw downloads = %d, want 2", got)
	}
	if err := run(opts); err != nil {
		t.Fatalf("rerun material profile mode: %v", err)
	}
	if got := materialRawDownloads.Load(); got != 2 {
		t.Fatalf("completed materials were downloaded again, raw downloads=%d", got)
	}
	for _, want := range []string{"3 files from /data/profiles/kylin", "WARNING material " + staleZeroSizePath, "100.00%", "3/3 files", "assembled: 2 files", "skipped 1 stale zero-size entry"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing material output %q:\n%s", want, output.String())
		}
	}
}

func TestCollectMaterialFilesRejectsTraversalEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"name":"../outside","size":3,"is_dir":false}],"total":1}}`))
	}))
	defer server.Close()
	oldBaseURL := alistBaseURL
	t.Cleanup(func() { alistBaseURL = oldBaseURL })
	alistBaseURL = server.URL
	_, err := collectMaterialFiles("jwt-token", "/data/profiles/test")
	if err == nil || !strings.Contains(err.Error(), "not a single path component") {
		t.Fatalf("expected traversal entry rejection, got %v", err)
	}
}

func TestListAListDirectoryPaginatesWhenTotalIsMissing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		page, _ := body["page"].(float64)
		content := make([]map[string]any, 0, 500)
		switch int(page) {
		case 1:
			for idx := 0; idx < 500; idx++ {
				content = append(content, map[string]any{"name": fmt.Sprintf("file-%03d", idx), "size": 1})
			}
		case 2:
			content = append(content, map[string]any{"name": "file-500", "size": 1})
		default:
			t.Fatalf("unexpected page %v", page)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"content": content},
		})
	}))
	defer server.Close()

	oldBaseURL := alistBaseURL
	t.Cleanup(func() { alistBaseURL = oldBaseURL })
	alistBaseURL = server.URL
	entries, err := listAListDirectory("/data/profiles/test", "jwt-token")
	if err != nil {
		t.Fatalf("list directory: %v", err)
	}
	if len(entries) != 501 || calls.Load() != 2 {
		t.Fatalf("entries=%d calls=%d, want 501 entries from 2 calls", len(entries), calls.Load())
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
