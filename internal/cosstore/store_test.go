package cosstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func TestListPagedProfile(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("prefix") != "p/" || q.Get("delimiter") != "" {
			t.Errorf("unexpected list query: %s", r.URL)
		}
		switch q.Get("marker") {
		case "":
			io.WriteString(w, `<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>true</IsTruncated><NextMarker>p%2Fa%2Bb</NextMarker><Contents><Key>p%2F</Key></Contents><Contents><Key>p%2Fa%2Bb</Key><Size>1</Size><ETag>aaa</ETag></Contents></ListBucketResult>`)
		case "p/a+b":
			io.WriteString(w, `<ListBucketResult><Contents><Key>p/nested/.DS_Store</Key><ETag>ignore</ETag></Contents><Contents><Key>p/nested/file</Key><ETag>bbb</ETag></Contents></ListBucketResult>`)
		default:
			t.Errorf("unexpected marker: %s", q.Get("marker"))
			http.Error(w, "bad marker", 400)
		}
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	c := cos.NewClient(&cos.BaseURL{BucketURL: u}, s.Client())
	entries, err := List(context.Background(), c, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "a+b" || entries[1].Path != "nested/file" {
		t.Fatalf("entries: %+v", entries)
	}
}
