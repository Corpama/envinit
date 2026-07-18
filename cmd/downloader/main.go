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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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

type releaseManifest struct {
	Version  string            `json:"version"`
	Base     manifestAsset     `json:"base"`
	Profiles []manifestProfile `json:"profiles"`
}

type manifestProfile struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	MaterialRoot string          `json:"material_root"`
	Assets       []manifestAsset `json:"assets"`
	Bundle       manifestAsset   `json:"bundle"`
	Inventory    manifestAsset   `json:"inventory"`
}

type manifestAsset struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func main() {
	output := flag.String("output", "", "Output tar path, defaults to the release package name")
	outputDir := flag.String("output-dir", ".", "Output directory for manifest/profile downloads")
	profile := flag.String("profile", "", "Delivery profile ID to download, for example kylin10sp3-x86_64")
	listProfiles := flag.Bool("list-profiles", false, "List delivery profiles in the embedded manifest")
	jobs := flag.Int("jobs", 6, "Concurrent material file downloads")
	flag.Parse()

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
	rawManifest, err := embeddedManifest()
	if err != nil {
		return err
	}
	if rawManifest != "" {
		return runManifestMode(opts, rawManifest)
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

func runLegacyMode(output string) error {
	if strings.TrimSpace(alistBaseURL) == "" || strings.TrimSpace(alistFilePath) == "" ||
		strings.TrimSpace(alistUserB64) == "" || strings.TrimSpace(alistPassB64) == "" ||
		strings.TrimSpace(packageName) == "" || strings.TrimSpace(packageSHA256) == "" {
		return errors.New("downloader build is missing release metadata")
	}
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

func runManifestMode(opts runOptions, rawManifest string) error {
	manifest, err := parseManifest(rawManifest)
	if err != nil {
		return err
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.ListProfiles {
		printProfiles(opts.Stdout, manifest)
		return nil
	}
	profile, err := selectProfile(manifest, opts.Profile, opts.Stdin, opts.Stdout)
	if err != nil {
		return err
	}
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = "."
	}
	fmt.Fprintf(opts.Stdout, "Downloading env_tool %s profile %s\n", valueOrDefault(manifest.Version, releaseVersion), profile.ID)
	token, err := loginAList()
	if err != nil {
		return err
	}
	assets := profileDownloadAssets(manifest, profile)
	for _, asset := range assets {
		if err := downloadManifestAsset(outputDir, asset, token); err != nil {
			return err
		}
	}
	if strings.TrimSpace(profile.MaterialRoot) != "" {
		if err := downloadProfileMaterials(outputDir, profile, token, opts.Jobs, opts.Stdout); err != nil {
			return err
		}
	}
	fmt.Fprintf(opts.Stdout, "Profile %s is ready under %s\n", profile.ID, outputDir)
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

	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return manifestProfile{}, fmt.Errorf("read profile selection: %w", err)
		}
		return manifestProfile{}, errors.New("profile selection is required")
	}
	choice := strings.TrimSpace(scanner.Text())
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
			inventory.Name = "planning/inventory.sample.csv"
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
	RemotePath   string
	RelativePath string
	Size         int64
	Modified     string
	SHA256       string
}

func downloadProfileMaterials(outputDir string, profile manifestProfile, token string, jobs int, stdout io.Writer) error {
	root := path.Clean(strings.ReplaceAll(strings.TrimSpace(profile.MaterialRoot), "\\", "/"))
	if !path.IsAbs(root) || root == "/" {
		return fmt.Errorf("profile %s material_root must be an absolute AList directory below /, got %q", profile.ID, profile.MaterialRoot)
	}
	files, err := collectMaterialFiles(token, root)
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
	fmt.Fprintf(stdout, "Material profile %s: %d files from %s using %d workers\n", profile.ID, len(files), root, jobs)

	queue := make(chan materialFile, len(files))
	for _, file := range files {
		queue <- file
	}
	close(queue)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	completed := 0
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
				if err := downloadMaterialFile(outputDir, profile.ID, token, file); err != nil {
					mu.Lock()
					if firstErr == nil {
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
	if firstErr != nil {
		return firstErr
	}
	fmt.Fprintf(stdout, "Material profile %s assembled: %d/%d files under %s\n", profile.ID, completed, len(files), filepath.Join(outputDir, "data"))
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

func downloadMaterialFile(outputDir, profileID, token string, file materialFile) error {
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
		return nil
	}
	// Directory enumeration already refreshed the material tree. Avoid forcing
	// another storage refresh for every file in a large offline repository.
	downloadURL, err := resolveDownloadURLForPathWithTokenAndRefresh(file.RemotePath, token, false)
	if err != nil {
		return fmt.Errorf("resolve material %s: %w", file.RemotePath, err)
	}
	partialOutput := output + ".part"
	if err := downloadFile(partialOutput, downloadURL, false); err != nil {
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
	return fmt.Sprintf("profile=%s\npath=%s\nsize=%d\nmodified=%s\nsha256=%s\n", profileID, file.RemotePath, file.Size, file.Modified, file.SHA256)
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

func downloadManifestAsset(outputDir string, asset manifestAsset, token string) error {
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
	fmt.Printf("Asset: %s\n", name)
	if archiveKind(name) != "" {
		assembled, err := manifestAssetAlreadyAssembled(outputDir, asset.SHA256)
		if err != nil {
			return err
		}
		if assembled {
			fmt.Println("  archive already assembled")
			return nil
		}
	}
	if err := verifySHA256(output, asset.SHA256); err == nil {
		fmt.Println("  existing file already matches SHA256")
		return assembleManifestArchive(outputDir, output, asset.SHA256)
	}
	downloadURL, err := resolveDownloadURLForPathWithToken(asset.Path, token)
	if err != nil {
		return err
	}
	partialOutput := output + ".part"
	if err := download(partialOutput, downloadURL); err != nil {
		return err
	}
	if err := verifySHA256(partialOutput, asset.SHA256); err != nil {
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
	fmt.Printf("  verified SHA256: %s\n", asset.SHA256)
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
	fmt.Printf("  assembled under %s\n", outputDir)
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

	response, err := http.DefaultClient.Do(request)
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
	var offset int64
	if info, err := os.Stat(output); err == nil {
		offset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing output: %w", err)
	}

	client := &http.Client{Timeout: 0}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if reportProgress {
			fmt.Printf("Resuming at byte %d\n", offset)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download package: %w", err)
	}
	defer resp.Body.Close()

	body := bufio.NewReader(resp.Body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		message, err := io.ReadAll(io.LimitReader(body, 64<<10))
		if err != nil {
			return fmt.Errorf("read JSON error response: %w", err)
		}
		return fmt.Errorf("download package: %s", strings.TrimSpace(string(message)))
	}

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		flags |= os.O_APPEND
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		flags |= os.O_TRUNC
	default:
		return fmt.Errorf("download package: HTTP %s", resp.Status)
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
	written, err := io.Copy(file, body)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if reportProgress {
		fmt.Printf("Downloaded %d bytes in %s\n", written, time.Since(start).Round(time.Second))
	}
	return nil
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
