package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The "GET object" example of the AWS Signature Version 4 documentation.
func TestSignAWSExample(t *testing.T) {
	c := &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, emptyHash())
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// fake is a tiny S3: it checks that every request is signed and keeps objects in memory.
type fake struct {
	mu      sync.Mutex
	objs    map[string][]byte
	uploads map[string]map[int][]byte
	n       int
	failPut int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("x-amz-content-sha256") == "" {
		w.WriteHeader(403)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>no</Message></Error>")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bk/")
	q := r.URL.Query()
	switch {
	case r.Method == "GET" && q.Get("list-type") == "2":
		fmt.Fprint(w, "<ListBucketResult>")
		for k, v := range f.objs {
			if strings.HasPrefix(k, q.Get("prefix")) {
				fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(v))
			}
		}
		fmt.Fprint(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")
	case r.Method == "POST" && q.Has("uploads"):
		f.n++
		id := fmt.Sprint("u", f.n)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == "PUT" && q.Get("uploadId") != "":
		if f.failPut > 0 {
			f.failPut--
			w.WriteHeader(500)
			return
		}
		var n int
		fmt.Sscan(q.Get("partNumber"), &n)
		b, _ := io.ReadAll(r.Body)
		f.uploads[q.Get("uploadId")][n] = b
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, n))
	case r.Method == "POST" && q.Get("uploadId") != "":
		var x struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		_ = xml.NewDecoder(r.Body).Decode(&x)
		var all []byte
		for i, p := range x.Parts {
			if p.PartNumber != i+1 || p.ETag != fmt.Sprintf(`"e%d"`, i+1) {
				w.WriteHeader(400)
				return
			}
			all = append(all, f.uploads[q.Get("uploadId")][p.PartNumber]...)
		}
		f.objs[key] = all
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprint(w, "<CompleteMultipartUploadResult/>")
	case r.Method == "DELETE" && q.Get("uploadId") != "":
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		f.objs[key] = b
	case r.Method == "DELETE":
		delete(f.objs, key)
		w.WriteHeader(204)
	default:
		w.WriteHeader(400)
	}
}

func TestClient(t *testing.T) {
	f := &fake{objs: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Bucket: "bk", AccessKey: "AK", SecretKey: "SK"}
	ctx := context.Background()
	old := PartSize
	PartSize = 1000
	defer func() { PartSize, retryWait = old, 2*time.Second }()
	retryWait = time.Millisecond
	dir := t.TempDir()
	small, big := filepath.Join(dir, "s"), filepath.Join(dir, "b")
	_ = os.WriteFile(small, []byte("hello"), 0o600)
	data := []byte(strings.Repeat("0123456789", 350))
	_ = os.WriteFile(big, data, 0o600)
	if err := c.PutFile(ctx, "p/s.npbk", small, nil); err != nil {
		t.Fatal(err)
	}
	f.failPut = 1 // one part fails once and is sent again
	var last int64
	if err := c.PutFile(ctx, "p/b.npbk", big, func(d, _ int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	if string(f.objs["p/b.npbk"]) != string(data) || last != int64(len(data)) || len(f.uploads) != 0 {
		t.Fatal("multipart", len(f.objs["p/b.npbk"]), last)
	}
	l, err := c.List(ctx, "p/")
	if err != nil || len(l) != 2 {
		t.Fatal(l, err)
	}
	if err := c.Delete(ctx, "p/s.npbk"); err != nil || len(f.objs) != 1 {
		t.Fatal(err)
	}
	f.failPut = 5 // a part that keeps failing aborts the upload
	if err := c.PutFile(ctx, "p/c.npbk", big, nil); err == nil || len(f.uploads) != 0 {
		t.Fatal("abort", err)
	}
	bad := &Client{Endpoint: srv.URL, Bucket: "bk", AccessKey: "XX", SecretKey: "SK"}
	err = bad.PutBytes(ctx, "x", []byte("x"))
	if e, ok := err.(*Error); !ok || e.Code != "AccessDenied" || e.Status != 403 {
		t.Fatal(err)
	}
}

func TestEscape(t *testing.T) {
	if got := escape("a b/c+d~", false); got != "a%20b/c%2Bd~" {
		t.Fatal(got)
	}
	if got := escape("a/b", true); got != "a%2Fb" {
		t.Fatal(got)
	}
}
