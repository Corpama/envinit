package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"envinit/internal/configcli"
	"envinit/internal/cosstore"
	"envinit/internal/startup"
)

var (
	releaseVersion  = "development"
	packageName     = ""
	packageSHA256   = ""
	alistBaseURL    = ""
	alistFilePath   = ""
	alistUserB64    = ""
	alistPassB64    = ""
	manifestJSON    = ""
	manifestJSONB64 = ""
)

const (
	defaultOutputDir = "kunlun_env_init"

	// httpDialTimeout / httpTLSTimeout / httpResponseHeaderTimeout bound the
	// connection setup so an unreachable or silent endpoint fails fast instead
	// of hanging forever.
	httpDialTimeout           = 15 * time.Second
	httpTLSTimeout            = 15 * time.Second
	httpResponseHeaderTimeout = 30 * time.Second
	httpControlTimeout        = 60 * time.Second
	downloadStallTimeout      = 60 * time.Second
)

func newHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   httpDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   httpTLSTimeout,
		ResponseHeaderTimeout: httpResponseHeaderTimeout,
		ExpectContinueTimeout: 5 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}

// controlHTTPClient bounds the whole request/response for short AList control
// calls (login, resolve, list). A stalled body cannot hang these.
func controlHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   httpControlTimeout,
		Transport: newHTTPTransport(),
	}
}

// downloadHTTPClient has no overall Timeout because large materials can take a
// long time legitimately; instead the connection-setup timeouts above plus the
// per-read stall watchdog (stallReader) bound any silent hang.
func downloadHTTPClient() *http.Client {
	return &http.Client{Transport: newHTTPTransport()}
}

type releaseManifest struct {
	Store    *cosstore.Config  `json:"store,omitempty"`
	Version  string            `json:"version"`
	Base     manifestAsset     `json:"base"`
	Profiles []manifestProfile `json:"profiles"`
}

type manifestProfile struct {
	MaterialIndex manifestAsset   `json:"material_index,omitempty"`
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	MaterialRoot  string          `json:"material_root"`
	Assets        []manifestAsset `json:"assets"`
	Bundle        manifestAsset   `json:"bundle"`
	Inventory     manifestAsset   `json:"inventory"`
}

type manifestAsset struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func main() {
	name := filepath.Base(os.Args[0])
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if err := configcli.Run(name, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [download options]\n       %s config [--project DIR] [config options]\n\nDownload options:\n", name, name)
		flag.PrintDefaults()
	}
	output := flag.String("output", "", "Output tar path, defaults to the release package name")
	outputDir := flag.String("output-dir", defaultOutputDir, "Delivery directory for all profile files (created if missing)")
	profile := flag.String("profile", "", "Delivery profile ID to download, for example kylin10sp3-x86_64")
	listProfiles := flag.Bool("list-profiles", false, "List delivery profiles in the embedded manifest")
	jobs := flag.Int("jobs", 6, "Concurrent material file downloads")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	opts := runOptions{
		Output:       *output,
		OutputDir:    *outputDir,
		Profile:      *profile,
		ListProfiles: *listProfiles,
		Jobs:         *jobs,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type runOptions struct {
	Output       string
	OutputDir    string
	Profile      string
	ListProfiles bool
	Jobs         int
	Stdin        io.Reader
	Stdout       io.Writer
}

func run(opts runOptions) error {
	var rawManifest string
	var manifest releaseManifest
	var args []string
	if opts.ListProfiles {
		args = []string{"--list-profiles"}
	}
	err := startup.Run("downloader", args,
		startup.Step{Label: "读取发布清单", Load: func() error { var e error; rawManifest, e = embeddedManifest(); return e }},
		startup.Step{Label: "解析交付配置", Load: func() error {
			if rawManifest == "" {
				return validateLegacyMetadata()
			}
			var e error
			manifest, e = parseManifest(rawManifest)
			return e
		}},
	)
	if err != nil {
		return err
	}
	if rawManifest != "" {
		if isInteractiveDownloader() && opts.Profile == "" && !opts.ListProfiles {
			return runManifestTUI(opts, manifest)
		}
		return runManifestMode(opts, manifest)
	}
	return runLegacyMode(opts.Output)
}

func embeddedManifest() (string, error) {
	if strings.TrimSpace(manifestJSON) != "" {
		return manifestJSON, nil
	}
	if strings.TrimSpace(manifestJSONB64) == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(manifestJSONB64)
	if err != nil {
		return "", fmt.Errorf("decode embedded release manifest: %w", err)
	}
	return string(raw), nil
}

func validateLegacyMetadata() error {
	if strings.TrimSpace(alistBaseURL) == "" || strings.TrimSpace(alistFilePath) == "" ||
		strings.TrimSpace(alistUserB64) == "" || strings.TrimSpace(alistPassB64) == "" ||
		strings.TrimSpace(packageName) == "" || strings.TrimSpace(packageSHA256) == "" {
		return errors.New("downloader build is missing release metadata")
	}
	return nil
}

func runLegacyMode(output string) error {
	if output == "" {
		output = packageName
	}

	fmt.Printf("Downloading env_tool %s\n", releaseVersion)
	fmt.Printf("Output: %s\n", output)
	if err := verifySHA256(output, packageSHA256); err == nil {
		fmt.Println("Existing package already matches the expected SHA256")
		fmt.Printf("Extract with: tar -xf %s\n", output)
		return nil
	}
	downloadURL, err := resolveDownloadURL()
	if err != nil {
		return err
	}
	partialOutput := output + ".part"
	if err := download(partialOutput, downloadURL); err != nil {
		return err
	}
	if err := verifySHA256(partialOutput, packageSHA256); err != nil {
		return err
	}
	if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unverified output: %w", err)
	}
	if err := os.Rename(partialOutput, output); err != nil {
		return fmt.Errorf("replace output with verified package: %w", err)
	}
	fmt.Printf("Verified SHA256: %s\n", packageSHA256)
	fmt.Printf("Extract with: tar -xf %s\n", output)
	return nil
}

func resolveDownloadURL() (string, error) {
	return resolveDownloadURLForPath(alistFilePath)
}

func resolveDownloadURLForPath(filePath string) (string, error) {
	token, err := loginAList()
	if err != nil {
		return "", err
	}
	return resolveDownloadURLForPathWithToken(filePath, token)
}

func loginAList() (string, error) {
	username, err := decodeCredential(alistUserB64)
	if err != nil {
		return "", fmt.Errorf("decode AList username: %w", err)
	}
	password, err := decodeCredential(alistPassB64)
	if err != nil {
		return "", fmt.Errorf("decode AList password: %w", err)
	}

	var loginResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := postJSON("/api/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	}, &loginResponse); err != nil {
		return "", err
	}
	if loginResponse.Code != 200 || loginResponse.Data.Token == "" {
		return "", fmt.Errorf("AList login failed: code=%d message=%s", loginResponse.Code, loginResponse.Message)
	}
	return loginResponse.Data.Token, nil
}

func resolveDownloadURLForPathWithToken(filePath, token string) (string, error) {
	return resolveDownloadURLForPathWithTokenAndRefresh(filePath, token, true)
}

func resolveDownloadURLForPathWithTokenAndRefresh(filePath, token string, refresh bool) (string, error) {
	var fileResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RawURL string `json:"raw_url"`
		} `json:"data"`
	}
	if err := postJSON("/api/fs/get", token, map[string]any{
		"path":     filePath,
		"password": "",
		"refresh":  refresh,
	}, &fileResponse); err != nil {
		return "", err
	}
	if fileResponse.Code != 200 || fileResponse.Data.RawURL == "" {
		return "", fmt.Errorf("AList file lookup failed: code=%d message=%s", fileResponse.Code, fileResponse.Message)
	}
	return fileResponse.Data.RawURL, nil
}

func runManifestMode(opts runOptions, manifest releaseManifest) error {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.ListProfiles {
		printProfiles(opts.Stdout, manifest)
		return nil
	}
	if opts.Stdin != nil {
		if _, ok := opts.Stdin.(*bufio.Reader); !ok {
			opts.Stdin = bufio.NewReader(opts.Stdin)
		}
	}
	profile, err := selectProfile(manifest, opts.Profile, opts.Stdin, opts.Stdout)
	if err != nil {
		return err
	}
	if err := downloadSelectedProfile(opts, manifest, profile, nil); err != nil {
		return err
	}
	printConfigHint(opts)
	return nil
}

func downloadSelectedProfile(opts runOptions, manifest releaseManifest, profile manifestProfile, reporter *tuiReporter) error {
	outputDir := normalizedOutputDir(opts.OutputDir)
	unlock, err := lockDeliveryProject(outputDir)
	if err != nil {
		return err
	}
	defer unlock()
	paths, state, err := prepareProject(outputDir, manifest, profile)
	if err != nil {
		return err
	}
	if reporter != nil {
		quietDownloaderOutput = true
		defer func() { quietDownloaderOutput = false }()
		reporter.update("准备下载", "连接物料服务", 0.02, 0, 0, 0, 0)
	}
	if reporter == nil {
		fmt.Fprintf(opts.Stdout, "Downloading env_tool %s profile %s\n", valueOrDefault(manifest.Version, releaseVersion), profile.ID)
	}
	store, err := openDeliveryStore(manifest.Store)
	if err != nil {
		return err
	}
	if reporter != nil {
		reporter.update("下载规划模板", "bundle.json / inventory.csv", 0.08, 0, 0, 0, 0)
	}
	if err := downloadProjectTemplates(paths, profile, store); err != nil {
		return err
	}
	if err := seedPlanningTemplates(paths); err != nil {
		return err
	}
	state.Status = "downloading"
	if err := writeProjectState(paths, state); err != nil {
		return err
	}
	assets := append([]manifestAsset{}, profile.Assets...)
	if strings.TrimSpace(manifest.Base.Path) != "" {
		assets = append([]manifestAsset{manifest.Base}, assets...)
	}
	for i, asset := range assets {
		name := asset.Name
		if name == "" {
			name = filepath.Base(asset.Path)
		}
		if protectedProjectPath(filepath.ToSlash(filepath.Clean(name))) {
			return fmt.Errorf("asset %q targets protected project configuration", name)
		}
		if reporter != nil {
			reporter.update("下载基础交付文件", name, 0.12+0.18*float64(i)/float64(max(1, len(assets))), 0, 0, 0, 0)
		}
		if err := downloadManifestAsset(outputDir, asset, store); err != nil {
			return err
		}
	}
	if store.cos != nil || strings.TrimSpace(profile.MaterialRoot) != "" {
		if err := downloadProfileMaterials(paths, profile, store, opts.Jobs, opts.Stdout, reporter); err != nil {
			return err
		}
	}
	state.Status = "ready"
	if err := writeProjectState(paths, state); err != nil {
		return err
	}
	if reporter != nil {
		reporter.update("交付目录已就绪", outputDir, 1, 0, 0, 0, 0)
	} else {
		fmt.Fprintf(opts.Stdout, "Profile %s is ready under %s\n", profile.ID, outputDir)
	}
	return nil
}

func parseManifest(raw string) (releaseManifest, error) {
	var manifest releaseManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("parse release manifest: %w", err)
	}
	if len(manifest.Profiles) == 0 {
		return releaseManifest{}, errors.New("release manifest does not contain any profiles")
	}
	return manifest, nil
}

func printProfiles(w io.Writer, manifest releaseManifest) {
	fmt.Fprintf(w, "Available delivery profiles for env_tool %s:\n", valueOrDefault(manifest.Version, releaseVersion))
	for idx, profile := range manifest.Profiles {
		line := fmt.Sprintf("  %d. %s", idx+1, profile.ID)
		if strings.TrimSpace(profile.Name) != "" {
			line += " - " + strings.TrimSpace(profile.Name)
		}
		if strings.TrimSpace(profile.Description) != "" {
			line += " (" + strings.TrimSpace(profile.Description) + ")"
		}
		fmt.Fprintln(w, line)
	}
}

func selectProfile(manifest releaseManifest, requested string, stdin io.Reader, stdout io.Writer) (manifestProfile, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, profile := range manifest.Profiles {
			if profile.ID == requested {
				return profile, nil
			}
		}
		return manifestProfile{}, fmt.Errorf("unknown profile %q; run with --list-profiles to see supported profiles", requested)
	}
	if stdin == nil {
		return manifestProfile{}, errors.New("--profile is required when stdin is not available")
	}
	if stdinIsNonInteractiveFile(stdin) {
		return manifestProfile{}, errors.New("--profile is required in non-interactive mode; stdin is not a terminal, so no profile prompt can be answered")
	}
	fmt.Fprintln(stdout, "请选择本次交付系统：")
	for idx, profile := range manifest.Profiles {
		label := profile.ID
		if strings.TrimSpace(profile.Name) != "" {
			label = strings.TrimSpace(profile.Name)
		}
		if strings.TrimSpace(profile.Description) != "" {
			fmt.Fprintf(stdout, "%d. %s - %s\n", idx+1, label, strings.TrimSpace(profile.Description))
		} else {
			fmt.Fprintf(stdout, "%d. %s\n", idx+1, label)
		}
	}
	fmt.Fprint(stdout, "请输入序号或 profile ID: ")

	reader, ok := stdin.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(stdin)
	}
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || strings.TrimSpace(line) == "") {
		return manifestProfile{}, fmt.Errorf("read profile selection: %w", err)
	}
	choice := strings.TrimSpace(line)
	for idx, profile := range manifest.Profiles {
		if choice == profile.ID || choice == fmt.Sprintf("%d", idx+1) {
			return profile, nil
		}
	}
	return manifestProfile{}, fmt.Errorf("invalid profile selection %q", choice)
}

func profileDownloadAssets(manifest releaseManifest, profile manifestProfile) []manifestAsset {
	assets := []manifestAsset{}
	if strings.TrimSpace(manifest.Base.Path) != "" {
		assets = append(assets, manifest.Base)
	}
	assets = append(assets, profile.Assets...)
	if strings.TrimSpace(profile.Bundle.Path) != "" {
		bundle := profile.Bundle
		if strings.TrimSpace(bundle.Name) == "" {
			bundle.Name = "planning/bundle.json"
		}
		assets = append(assets, bundle)
	}
	if strings.TrimSpace(profile.Inventory.Path) != "" {
		inventory := profile.Inventory
		if strings.TrimSpace(inventory.Name) == "" {
			inventory.Name = "planning/inventory.csv"
		}
		assets = append(assets, inventory)
	}
	return assets
}

type alistMaterialEntry struct {
	Name           string          `json:"name"`
	Size           int64           `json:"size"`
	IsDir          bool            `json:"is_dir"`
	Modified       string          `json:"modified"`
	HashInfo       json.RawMessage `json:"hash_info"`
	LegacyHashInfo json.RawMessage `json:"hashinfo"`
}

type materialFile struct {
	ETag         string
	RemotePath   string
	RelativePath string
	Size         int64
	Modified     string
	SHA256       string
}

var errSkipStaleZeroSizeMaterial = errors.New("skip stale zero-size material")

type downloadHTTPStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *downloadHTTPStatusError) Error() string {
	if strings.TrimSpace(e.Body) != "" {
		return fmt.Sprintf("download package: HTTP %s: %s", e.Status, strings.TrimSpace(e.Body))
	}
	return fmt.Sprintf("download package: HTTP %s", e.Status)
}

func downloadProfileMaterials(paths projectPaths, profile manifestProfile, store deliveryStore, jobs int, stdout io.Writer, reporter *tuiReporter) error {
	outputDir, root := paths.OutputDir, profile.MaterialRoot
	files, err := store.materialFiles(profile, filepath.Dir(paths.BundleTemplate))
	if err != nil {
		return fmt.Errorf("list material profile %s: %w", profile.ID, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("material profile %s is empty at %s", profile.ID, root)
	}
	if jobs <= 0 {
		jobs = 6
	}
	if jobs > 32 {
		jobs = 32
	}
	if jobs > len(files) {
		jobs = len(files)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if reporter != nil {
		// The Bubble Tea dashboard owns the terminal while a download is active.
		// Keep the existing text progress implementation for non-interactive and
		// explicit CLI mode, but never let it interleave with the TUI renderer.
		stdout = io.Discard
	} else {
		fmt.Fprintf(stdout, "Material profile %s: %d files from %s using %d workers\n", profile.ID, len(files), root, jobs)
	}
	progress := newMaterialProgressWithReporter(stdout, files, reporter)
	progress.start()

	queue := make(chan materialFile, len(files))
	for _, file := range files {
		queue <- file
	}
	close(queue)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	completed := 0
	skipped := 0
	for worker := 0; worker < jobs; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range queue {
				mu.Lock()
				stopped := firstErr != nil
				mu.Unlock()
				if stopped {
					continue
				}
				if err := downloadMaterialFile(outputDir, profile.ID, store, file, progress); err != nil {
					mu.Lock()
					if errors.Is(err, errSkipStaleZeroSizeMaterial) {
						skipped++
						progress.completeFile()
						progress.warning("material %s is listed as an empty file but its storage object is missing; skipping stale entry", file.RemotePath)
					} else if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	progress.finish()
	if firstErr != nil {
		return firstErr
	}
	if skipped > 0 {
		noun := "entries"
		if skipped == 1 {
			noun = "entry"
		}
		fmt.Fprintf(stdout, "Material profile %s assembled: %d files under %s; skipped %d stale zero-size %s\n", profile.ID, completed, filepath.Join(outputDir, "data"), skipped, noun)
	} else {
		fmt.Fprintf(stdout, "Material profile %s assembled: %d/%d files under %s\n", profile.ID, completed, len(files), filepath.Join(outputDir, "data"))
	}
	return nil
}

func collectMaterialFiles(token, root string) ([]materialFile, error) {
	var files []materialFile
	var walk func(string, string) error
	walk = func(remoteDir, relativeDir string) error {
		entries, err := listAListDirectory(remoteDir, token)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			if shouldSkipMaterialEntry(name) {
				continue
			}
			if err := validateMaterialEntryName(name); err != nil {
				return fmt.Errorf("invalid entry below %s: %w", remoteDir, err)
			}
			remotePath := path.Join(remoteDir, name)
			relativePath := path.Join(relativeDir, name)
			if entry.IsDir {
				if err := walk(remotePath, relativePath); err != nil {
					return err
				}
				continue
			}
			files = append(files, materialFile{
				RemotePath:   remotePath,
				RelativePath: relativePath,
				Size:         entry.Size,
				Modified:     entry.Modified,
				SHA256:       materialSHA256(entry),
			})
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return files, nil
}

func listAListDirectory(directory, token string) ([]alistMaterialEntry, error) {
	const perPage = 500
	var entries []alistMaterialEntry
	for pageNumber := 1; ; pageNumber++ {
		var response struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Content []alistMaterialEntry `json:"content"`
				Total   int                  `json:"total"`
			} `json:"data"`
		}
		if err := postJSON("/api/fs/list", token, map[string]any{
			"path": directory, "password": "", "page": pageNumber, "per_page": perPage, "refresh": pageNumber == 1,
		}, &response); err != nil {
			return nil, err
		}
		if response.Code != 200 {
			return nil, fmt.Errorf("AList directory lookup %s failed: code=%d message=%s", directory, response.Code, response.Message)
		}
		entries = append(entries, response.Data.Content...)
		if len(response.Data.Content) == 0 || (response.Data.Total > 0 && response.Data.Total <= len(entries)) || len(response.Data.Content) < perPage {
			break
		}
	}
	return entries, nil
}

func validateMaterialEntryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("material entry name %q is not a single path component", name)
	}
	return nil
}

func shouldSkipMaterialEntry(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return lower == ".ds_store" || lower == "thumbs.db" || strings.HasPrefix(lower, "._")
}

func materialSHA256(entry alistMaterialEntry) string {
	for _, raw := range []json.RawMessage{entry.HashInfo, entry.LegacyHashInfo} {
		if value := hashInfoValue(raw, "sha256"); value != "" {
			return strings.ToLower(value)
		}
	}
	return ""
}

func hashInfoValue(raw json.RawMessage, algorithm string) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.EqualFold(value, "null") {
		return ""
	}

	if strings.HasPrefix(value, "\"") {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return ""
		}
		encoded = strings.TrimSpace(encoded)
		if encoded == "" || strings.EqualFold(encoded, "null") {
			return ""
		}
		if strings.HasPrefix(encoded, "{") {
			return hashInfoValue(json.RawMessage(encoded), algorithm)
		}
		for _, field := range strings.FieldsFunc(encoded, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
		}) {
			name, digest, ok := strings.Cut(field, ":")
			if !ok {
				name, digest, ok = strings.Cut(field, "=")
			}
			if ok && strings.EqualFold(strings.TrimSpace(name), algorithm) {
				return strings.TrimSpace(digest)
			}
		}
		return ""
	}

	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return ""
	}
	for name, candidate := range values {
		if !strings.EqualFold(strings.TrimSpace(name), algorithm) {
			continue
		}
		if digest, ok := candidate.(string); ok {
			return strings.TrimSpace(digest)
		}
	}
	return ""
}

func downloadMaterialFile(outputDir, profileID string, store deliveryStore, file materialFile, progress *materialProgress) error {
	dataRoot := filepath.Join(outputDir, "data")
	output, err := assetOutputPath(dataRoot, filepath.FromSlash(file.RelativePath))
	if err != nil {
		return err
	}
	complete, err := materialFileAlreadyComplete(outputDir, profileID, output, file)
	if err != nil {
		return err
	}
	if complete {
		progress.addExisting(file.Size)
		progress.completeFile()
		return nil
	}
	partialOutput := output + ".part"
	if err := store.fetch(partialOutput, file.RemotePath, file.ETag, false, progress); err != nil {
		var statusErr *downloadHTTPStatusError
		if store.cos == nil && file.Size == 0 && file.SHA256 == "" && errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			_ = os.Remove(partialOutput)
			return fmt.Errorf("%w: %s", errSkipStaleZeroSizeMaterial, file.RemotePath)
		}
		return fmt.Errorf("download material %s: %w", file.RemotePath, err)
	}
	if err := verifyMaterialFile(partialOutput, file); err != nil {
		return fmt.Errorf("verify material %s: %w", file.RemotePath, err)
	}
	if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove previous material %s: %w", file.RelativePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create material directory for %s: %w", file.RelativePath, err)
	}
	if err := os.Rename(partialOutput, output); err != nil {
		return fmt.Errorf("install material %s: %w", file.RelativePath, err)
	}
	if err := applyArchiveMode(output, materialFileMode(file.RelativePath)); err != nil {
		return err
	}
	if err := writeMaterialMarker(outputDir, profileID, file); err != nil {
		return err
	}
	progress.completeFile()
	return nil
}

func materialFileAlreadyComplete(outputDir, profileID, output string, file materialFile) (bool, error) {
	info, err := os.Stat(output)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect material %s: %w", file.RelativePath, err)
	}
	if !info.Mode().IsRegular() || info.Size() != file.Size {
		return false, nil
	}
	if file.SHA256 != "" {
		if err := verifySHA256(output, file.SHA256); err != nil {
			return false, nil
		}
		if err := applyArchiveMode(output, materialFileMode(file.RelativePath)); err != nil {
			return false, err
		}
		if err := writeMaterialMarker(outputDir, profileID, file); err != nil {
			return false, err
		}
		return true, nil
	}
	marker, err := os.ReadFile(materialMarkerPath(outputDir, file.RemotePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read material marker: %w", err)
	}
	if string(marker) != materialFingerprint(profileID, file) {
		return false, nil
	}
	if err := applyArchiveMode(output, materialFileMode(file.RelativePath)); err != nil {
		return false, err
	}
	return true, nil
}

func verifyMaterialFile(localPath string, file materialFile) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.Size() != file.Size {
		return fmt.Errorf("size mismatch: expected %d, got %d", file.Size, info.Size())
	}
	if file.SHA256 != "" {
		return verifySHA256(localPath, file.SHA256)
	}
	return nil
}

func materialMarkerPath(outputDir, remotePath string) string {
	sum := sha256.Sum256([]byte(remotePath))
	return filepath.Join(outputDir, ".envinit-downloads", "materials", hex.EncodeToString(sum[:])+".complete")
}

func materialFingerprint(profileID string, file materialFile) string {
	fp := fmt.Sprintf("profile=%s\npath=%s\nsize=%d\nmodified=%s\nsha256=%s\n", profileID, file.RemotePath, file.Size, file.Modified, file.SHA256)
	if file.ETag != "" {
		fp += "etag=" + file.ETag + "\n"
	}
	return fp
}

func writeMaterialMarker(outputDir, profileID string, file materialFile) error {
	marker := materialMarkerPath(outputDir, file.RemotePath)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return fmt.Errorf("create material marker directory: %w", err)
	}
	if err := os.WriteFile(marker, []byte(materialFingerprint(profileID, file)), 0o644); err != nil {
		return fmt.Errorf("write material marker: %w", err)
	}
	return nil
}

func materialFileMode(relativePath string) os.FileMode {
	lower := strings.ToLower(filepath.ToSlash(relativePath))
	base := path.Base(lower)
	if strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".run") || base == "xpu_exporter" {
		return 0o755
	}
	return 0o644
}

func downloadManifestAsset(outputDir string, asset manifestAsset, store deliveryStore) error {
	if strings.TrimSpace(asset.Path) == "" {
		return errors.New("manifest asset path is required")
	}
	if strings.TrimSpace(asset.SHA256) == "" {
		return fmt.Errorf("manifest asset %s is missing sha256", asset.Path)
	}
	name := strings.TrimSpace(asset.Name)
	if name == "" {
		name = filepath.Base(asset.Path)
	}
	output, err := assetOutputPath(outputDir, name)
	if err != nil {
		return err
	}
	downloaderPrintf("Asset: %s\n", name)
	if archiveKind(name) != "" {
		assembled, err := manifestAssetAlreadyAssembled(outputDir, asset.SHA256)
		if err != nil {
			return err
		}
		if assembled {
			downloaderPrintln("  archive already assembled")
			return nil
		}
	}
	if err := verifySHA256(output, asset.SHA256); err == nil {
		downloaderPrintln("  existing file already matches SHA256")
		return assembleManifestArchive(outputDir, output, asset.SHA256)
	}
	partialOutput := output + ".part"
	if err := store.fetch(partialOutput, asset.Path, "", true, nil); err != nil {
		return err
	}
	if err := verifySHA256(partialOutput, asset.SHA256); err != nil {
		_ = os.Remove(partialOutput)
		return err
	}
	if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unverified output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create asset output directory: %w", err)
	}
	if err := os.Rename(partialOutput, output); err != nil {
		return fmt.Errorf("replace output with verified asset: %w", err)
	}
	downloaderPrintf("  verified SHA256: %s\n", asset.SHA256)
	return assembleManifestArchive(outputDir, output, asset.SHA256)
}

func archiveKind(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	default:
		return ""
	}
}

func assembleManifestArchive(outputDir, archivePath, sha256 string) error {
	kind := archiveKind(archivePath)
	if kind == "" {
		return nil
	}
	if err := extractDeliveryTar(archivePath, outputDir, kind == "tar.gz"); err != nil {
		return fmt.Errorf("assemble delivery archive %s: %w", filepath.Base(archivePath), err)
	}
	if err := os.Remove(archivePath); err != nil {
		return fmt.Errorf("remove assembled archive %s: %w", archivePath, err)
	}
	if err := writeManifestAssemblyMarker(outputDir, sha256); err != nil {
		return err
	}
	downloaderPrintf("  assembled under %s\n", outputDir)
	return nil
}

func extractDeliveryTar(archivePath, outputDir string, compressed bool) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	var reader io.Reader = file
	if compressed {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("open gzip stream: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	root, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		rel, skip, err := deliveryArchiveRelativePath(header.Name)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		// Planning is user-owned. Templates are downloaded separately; archives
		// must never replace saved configuration or downloader recovery records.
		if protectedProjectPath(rel) {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := ensurePathUnderRoot(root, target); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, archiveDirMode(header.Mode)); err != nil {
				return fmt.Errorf("create archive directory %s: %w", rel, err)
			}
			if err := applyArchiveMode(target, archiveDirMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := extractDeliveryFile(tarReader, target, rel, header.Mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q uses unsupported type %d", header.Name, header.Typeflag)
		}
	}
}

func deliveryArchiveRelativePath(name string) (string, bool, error) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	clean := path.Clean(name)
	if clean == "." || clean == "env_tool" {
		return "", true, nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("archive entry %q escapes the delivery directory", name)
	}
	if strings.HasPrefix(clean, "env_tool/") {
		clean = strings.TrimPrefix(clean, "env_tool/")
	}
	if clean == "" || clean == "." {
		return "", true, nil
	}
	return clean, false, nil
}

func ensurePathUnderRoot(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("validate archive target: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("archive target %q escapes the delivery directory", target)
	}
	return nil
}

func extractDeliveryFile(reader io.Reader, target, rel string, rawMode int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent for %s: %w", rel, err)
	}
	mode := archiveFileMode(rawMode)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create archive file %s: %w", rel, err)
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return fmt.Errorf("extract archive file %s: %w", rel, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close archive file %s: %w", rel, err)
	}
	if err := applyArchiveMode(target, mode); err != nil {
		return err
	}
	return nil
}

func archiveFileMode(raw int64) os.FileMode {
	mode := os.FileMode(raw).Perm()
	if mode == 0 {
		return 0o644
	}
	return mode
}

func archiveDirMode(raw int64) os.FileMode {
	mode := os.FileMode(raw).Perm()
	if mode == 0 {
		return 0o755
	}
	return mode
}

func manifestAssemblyMarker(outputDir, sha256 string) string {
	return filepath.Join(outputDir, ".envinit-downloads", strings.ToLower(strings.TrimSpace(sha256))+".complete")
}

func manifestAssetAlreadyAssembled(outputDir, sha256 string) (bool, error) {
	_, err := os.Stat(manifestAssemblyMarker(outputDir, sha256))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect archive assembly marker: %w", err)
}

func writeManifestAssemblyMarker(outputDir, sha256 string) error {
	marker := manifestAssemblyMarker(outputDir, sha256)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return fmt.Errorf("create archive marker directory: %w", err)
	}
	if err := os.WriteFile(marker, []byte(strings.ToLower(strings.TrimSpace(sha256))+"\n"), 0o644); err != nil {
		return fmt.Errorf("write archive assembly marker: %w", err)
	}
	return nil
}

func assetOutputPath(outputDir string, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("manifest asset name %q must be a relative file path under the output directory", name)
	}
	return filepath.Join(outputDir, clean), nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func decodeCredential(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func postJSON(path, token string, body any, output any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode AList request: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(alistBaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create AList request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", token)
	}

	response, err := controlHTTPClient().Do(request)
	if err != nil {
		return fmt.Errorf("call AList API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("call AList API: HTTP %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode AList response: %w", err)
	}
	return nil
}

func download(output, url string) error {
	return downloadFile(output, url, true)
}

func downloadFile(output, url string, reportProgress bool) error {
	return downloadFileWithProgress(output, url, reportProgress, nil, nil)
}

func downloadFileWithProgress(output, url string, reportProgress bool, onExisting, onDownloaded func(int64)) error {
	return downloadStream(output, reportProgress, true, onExisting, onDownloaded, func(byteRange string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		return downloadHTTPClient().Do(req)
	})
}

func downloadStream(output string, reportProgress, inspectAList bool, onExisting, onDownloaded func(int64), get func(string) (*http.Response, error)) error {
	var offset int64
	if info, err := os.Stat(output); err == nil {
		offset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing output: %w", err)
	}

	var byteRange string
	if offset > 0 {
		if onExisting != nil {
			onExisting(offset)
		}
		byteRange = fmt.Sprintf("bytes=%d-", offset)
		if reportProgress {
			downloaderPrintf("Resuming at byte %d\n", offset)
		}
	}

	resp, err := get(byteRange)
	if err != nil {
		return fmt.Errorf("download package: %w", err)
	}
	defer resp.Body.Close()

	body := bufio.NewReader(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, readErr := io.ReadAll(io.LimitReader(body, 64<<10))
		if readErr != nil {
			return fmt.Errorf("read HTTP error response: %w", readErr)
		}
		return &downloadHTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(message)}
	}
	var downloadBody io.Reader = body
	if inspectAList {
		downloadBody, err = inspectJSONDownloadResponse(resp.Header.Get("Content-Type"), body, onDownloaded)
		if err != nil {
			return err
		}
	} else if onDownloaded != nil {
		downloadBody = io.TeeReader(body, byteProgressWriter{add: onDownloaded})
	}

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
			return fmt.Errorf("unexpected Content-Range %q at offset %d", resp.Header.Get("Content-Range"), offset)
		}
		flags |= os.O_APPEND
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if offset > 0 && onExisting != nil {
			onExisting(-offset)
		}
		flags |= os.O_TRUNC
	default:
		return &downloadHTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}

	if err := os.MkdirAll(filepath.Dir(filepath.Clean(output)), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.OpenFile(output, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer file.Close()

	start := time.Now()
	reader := &stallReader{
		inner:   downloadBody,
		timeout: downloadStallTimeout,
		cancel:  func() { resp.Body.Close() },
	}
	written, err := io.Copy(file, reader)
	if err != nil {
		if reader.stalled() {
			return fmt.Errorf("download stalled: no data received for %s (received %d bytes); check network path to the storage endpoint", downloadStallTimeout, written)
		}
		return fmt.Errorf("write output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if reportProgress {
		downloaderPrintf("Downloaded %d bytes in %s\n", written, time.Since(start).Round(time.Second))
	}
	return nil
}

func inspectJSONDownloadResponse(contentType string, body io.Reader, onDownloaded func(int64)) (io.Reader, error) {
	if !strings.Contains(strings.ToLower(contentType), "json") {
		if onDownloaded != nil {
			return io.TeeReader(body, byteProgressWriter{add: onDownloaded}), nil
		}
		return body, nil
	}

	const maxErrorEnvelopeSize = 64 << 10
	prefix, err := io.ReadAll(io.LimitReader(body, maxErrorEnvelopeSize+1))
	if err != nil {
		return nil, fmt.Errorf("inspect JSON download response: %w", err)
	}
	if len(prefix) > maxErrorEnvelopeSize {
		return acceptedJSONDownloadBody(prefix, body, onDownloaded), nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(prefix, &envelope); err != nil {
		return acceptedJSONDownloadBody(prefix, body, onDownloaded), nil
	}
	codeRaw, hasCode := envelope["code"]
	messageRaw, hasMessage := envelope["message"]
	_, hasData := envelope["data"]
	if !hasCode || !hasMessage || !hasData {
		return acceptedJSONDownloadBody(prefix, body, onDownloaded), nil
	}
	var code int
	var message string
	if json.Unmarshal(codeRaw, &code) != nil || json.Unmarshal(messageRaw, &message) != nil || code == http.StatusOK {
		return acceptedJSONDownloadBody(prefix, body, onDownloaded), nil
	}
	return nil, fmt.Errorf("download package: AList code=%d message=%s", code, strings.TrimSpace(message))
}

func acceptedJSONDownloadBody(prefix []byte, remaining io.Reader, onDownloaded func(int64)) io.Reader {
	if onDownloaded != nil {
		onDownloaded(int64(len(prefix)))
		remaining = io.TeeReader(remaining, byteProgressWriter{add: onDownloaded})
	}
	return io.MultiReader(bytes.NewReader(prefix), remaining)
}

func verifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open output for verification: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("calculate SHA256: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// stallReader aborts a download when no bytes arrive within timeout. Each Read
// runs in a goroutine watched by a timer, so a connection that opens but then
// goes silent fails fast instead of hanging inside io.Copy forever.
type stallReader struct {
	inner      io.Reader
	timeout    time.Duration
	cancel     func()
	timedOut   bool
	cancelOnce sync.Once
}

type readResult struct {
	n   int
	err error
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.timeout <= 0 {
		return r.inner.Read(p)
	}
	done := make(chan readResult, 1)
	go func() {
		n, err := r.inner.Read(p)
		done <- readResult{n: n, err: err}
	}()
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.n, res.err
	case <-timer.C:
		r.timedOut = true
		if r.cancel != nil {
			r.cancelOnce.Do(r.cancel)
		}
		// Reap the blocked Read so the goroutine and buffer are not leaked.
		<-done
		return 0, fmt.Errorf("download stalled after %s with no data", r.timeout)
	}
}

func (r *stallReader) stalled() bool { return r.timedOut }
