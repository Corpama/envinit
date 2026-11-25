package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"envinit/internal/cosstore"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

type deliveryStore struct {
	token string // Only used by legacy AList manifests.
	cos   *cos.Client
}

func openDeliveryStore(config *cosstore.Config) (deliveryStore, error) {
	if config == nil {
		token, err := loginAList()
		return deliveryStore{token: token}, err
	}
	client := downloadHTTPClient()
	client.Transport.(*http.Transport).DisableCompression = true
	c, err := config.Client(client)
	return deliveryStore{cos: c}, err
}

func (s deliveryStore) materialFiles(profile manifestProfile, cache string) ([]materialFile, error) {
	root := profile.MaterialRoot
	if s.cos == nil {
		root = path.Clean(strings.ReplaceAll(strings.TrimSpace(root), "\\", "/"))
		if !path.IsAbs(root) || root == "/" {
			return nil, fmt.Errorf("invalid AList material_root %q", root)
		}
		return collectMaterialFiles(s.token, root)
	}
	if !cosstore.ValidKey(root) {
		return nil, fmt.Errorf("invalid COS material_root %q", root)
	}
	index := profile.MaterialIndex
	index.Name = "materials.json"
	if err := downloadManifestAsset(cache, index, s); err != nil {
		return nil, fmt.Errorf("material index: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(cache, index.Name))
	if err != nil {
		return nil, err
	}
	var entries []cosstore.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	files := make([]materialFile, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !cosstore.ValidKey(e.Path) || !filepath.IsLocal(filepath.FromSlash(e.Path)) || e.ETag == "" || e.Size < 0 || seen[e.Path] {
			return nil, fmt.Errorf("invalid or duplicate COS material index entry %q", e.Path)
		}
		seen[e.Path] = true
		files = append(files, materialFile{RemotePath: root + "/" + e.Path, RelativePath: e.Path, Size: e.Size, ETag: e.ETag})
	}
	return files, nil
}

func (s deliveryStore) fetch(output, key, etag string, asset bool, progress *materialProgress) error {
	if s.cos == nil {
		u, err := resolveDownloadURLForPathWithTokenAndRefresh(key, s.token, asset)
		if err != nil {
			return err
		}
		return downloadFileWithProgress(output, u, asset, progress.addExisting, progress.addDownloaded)
	}
	if !cosstore.ValidKey(key) {
		return fmt.Errorf("invalid COS object key %q", key)
	}
	var digest, crc string
	err := downloadStream(output, asset, false, progress.addExisting, progress.addDownloaded, func(byteRange string) (*http.Response, error) {
		headers := http.Header{}
		if etag != "" {
			headers.Set("If-Match", `"`+strings.Trim(etag, `"`)+`"`)
		}
		opts := &cos.ObjectGetOptions{Range: byteRange, XOptionHeader: &headers}
		resp, err := s.cos.Object.Get(context.Background(), key, opts)
		if byteRange != "" && resp != nil && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			opts.Range = ""
			resp, err = s.cos.Object.Get(context.Background(), key, opts)
		}
		if err != nil {
			return nil, fmt.Errorf("COS GetObject %q: %w", key, err)
		}
		if etag != "" && strings.Trim(resp.Header.Get("ETag"), `"`) != strings.Trim(etag, `"`) {
			resp.Body.Close()
			return nil, fmt.Errorf("COS object %q changed since release", key)
		}
		digest = resp.Header.Get("x-cos-meta-sha256")
		crc = resp.Header.Get("x-cos-hash-crc64ecma")
		return resp.Response, nil
	})
	if err != nil {
		return err
	}
	// COS returns the checksum of the whole object even on a ranged GET. Check
	// the assembled file so corruption in an existing .part is detected too.
	if digest != "" {
		err = verifySHA256(output, digest)
	} else if crc != "" {
		err = verifyCOSCRC(output, crc)
	}
	if err != nil {
		_ = os.Remove(output)
		return fmt.Errorf("verify COS object %q: %w", key, err)
	}
	return nil
}

func verifyCOSCRC(filename, want string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	h := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if strconv.FormatUint(h.Sum64(), 10) != want {
		return fmt.Errorf("CRC64 mismatch")
	}
	return nil
}
