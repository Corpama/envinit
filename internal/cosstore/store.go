// Package cosstore defines the release-time catalog used for anonymous COS downloads.
package cosstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

type Config struct {
	Provider string `json:"provider"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint"`
}

func (c Config) Client(httpClient *http.Client) (*cos.Client, error) {
	if c.Provider != "cos" || c.Endpoint != "cos."+c.Region+".myqcloud.com" || strings.ContainsAny(c.Bucket, "./:@?#\\%") {
		return nil, fmt.Errorf("invalid COS configuration: %+v", c)
	}
	u, err := cos.NewBucketURL(c.Bucket, c.Region, true)
	if err != nil {
		return nil, err
	}
	return cos.NewClient(&cos.BaseURL{BucketURL: u}, httpClient), nil
}

type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	ETag string `json:"etag"`
}

func ValidKey(key string) bool {
	return key != "" && key != "." && key != ".." && !strings.HasPrefix(key, "../") && !path.IsAbs(key) && path.Clean(key) == key && !strings.ContainsAny(key, "\\\x00\r\n")
}

// List reads only metadata. The published index lets downloaders use GetObject
// without granting anonymous callers permission to enumerate the bucket.
func List(ctx context.Context, client *cos.Client, root string) ([]Entry, error) {
	if !ValidKey(root) {
		return nil, fmt.Errorf("invalid COS material prefix %q", root)
	}
	opts := &cos.BucketGetOptions{Prefix: root + "/", MaxKeys: 1000, EncodingType: "url"}
	var files []Entry
	for {
		pageCtx, cancel := context.WithTimeout(ctx, time.Minute)
		page, _, err := client.Bucket.Get(pageCtx, opts)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", root, err)
		}
		decode := func(s string) (string, error) {
			if page.EncodingType == "url" {
				return url.PathUnescape(s)
			}
			return s, nil
		}
		for _, obj := range page.Contents {
			key, err := decode(obj.Key)
			if err != nil {
				return nil, err
			}
			rel, ok := strings.CutPrefix(key, opts.Prefix)
			if !ok {
				return nil, fmt.Errorf("unexpected object %q outside %q", key, opts.Prefix)
			}
			if strings.HasSuffix(key, "/") {
				continue
			}
			if !ValidKey(rel) || obj.Size < 0 || obj.ETag == "" {
				return nil, fmt.Errorf("invalid COS listing entry %q", key)
			}
			skip := false
			for _, component := range strings.Split(rel, "/") {
				s := strings.ToLower(strings.TrimSpace(component))
				if s == ".ds_store" || s == "thumbs.db" || strings.HasPrefix(s, "._") {
					skip = true
					break
				}
			}
			if !skip {
				files = append(files, Entry{rel, obj.Size, obj.ETag})
			}
		}
		if !page.IsTruncated {
			break
		}
		next := page.NextMarker
		if next == "" && len(page.Contents) > 0 {
			next = page.Contents[len(page.Contents)-1].Key
		}
		next, err = decode(next)
		if err != nil {
			return nil, err
		}
		if next <= opts.Marker {
			return nil, fmt.Errorf("COS listing for %s did not advance", root)
		}
		opts.Marker = next
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("empty COS material prefix %q", root)
	}
	return files, nil
}
