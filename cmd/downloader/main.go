package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	releaseVersion = "development"
	packageName    = ""
	packageSHA256  = ""
	downloadURL    = ""
)

func main() {
	output := flag.String("output", "", "Output tar path, defaults to the release package name")
	flag.Parse()

	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(output string) error {
	if strings.TrimSpace(downloadURL) == "" || strings.TrimSpace(packageName) == "" || strings.TrimSpace(packageSHA256) == "" {
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
	partialOutput := output + ".part"
	if err := download(partialOutput, downloadURL); err != nil {
		return err
	}
	if err := verifySHA256(partialOutput, packageSHA256); err != nil {
		return err
	}
	if err := os.Rename(partialOutput, output); err != nil {
		return fmt.Errorf("replace output with verified package: %w", err)
	}
	fmt.Printf("Verified SHA256: %s\n", packageSHA256)
	fmt.Printf("Extract with: tar -xf %s\n", output)
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
