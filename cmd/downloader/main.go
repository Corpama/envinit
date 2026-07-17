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
	"strings"
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
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Assets      []manifestAsset `json:"assets"`
	Bundle      manifestAsset   `json:"bundle"`
	Inventory   manifestAsset   `json:"inventory"`
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
	flag.Parse()

	opts := runOptions{
		Output:       *output,
		OutputDir:    *outputDir,
		Profile:      *profile,
		ListProfiles: *listProfiles,
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

	var fileResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RawURL string `json:"raw_url"`
		} `json:"data"`
	}
	if err := postJSON("/api/fs/get", loginResponse.Data.Token, map[string]any{
		"path":     filePath,
		"password": "",
		"refresh":  true,
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
	assets := profileDownloadAssets(manifest, profile)
	for _, asset := range assets {
		if err := downloadManifestAsset(outputDir, asset); err != nil {
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

func downloadManifestAsset(outputDir string, asset manifestAsset) error {
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
	downloadURL, err := resolveDownloadURLForPath(asset.Path)
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
		fmt.Printf("Resuming at byte %d\n", offset)
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
	fmt.Printf("Downloaded %d bytes in %s\n", written, time.Since(start).Round(time.Second))
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
