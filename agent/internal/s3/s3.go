// Package s3 is a small client for S3-compatible storage (BAK-03), with the Go standard library only: put a file
// (multipart above one part), list and delete objects under a prefix. Requests are signed with AWS Signature
// Version 4 and use path-style addresses (https://endpoint/bucket/key), which every S3-compatible service accepts.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PartSize is the size of one part of a multipart upload (S3 allows up to 10 000 parts).
var PartSize int64 = 64 << 20

// retryWait is the pause before a failed part is sent again (doubled the second time).
var retryWait = 2 * time.Second

// Client talks to one bucket.
type Client struct {
	Endpoint  string // https://host[:port]
	Region    string // "us-east-1" when the service does not care
	Bucket    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	Now       func() time.Time
}

// Object is one listed object.
type Object struct {
	Key  string
	Size int64
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Hour}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) region() string {
	if c.Region == "" {
		return "us-east-1"
	}
	return c.Region
}

// escape encodes a path or query part the way SigV4 wants it (RFC 3986 unreserved left as is).
func escape(s string, slash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !slash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func hmacSHA(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sign adds the SigV4 headers to req. payload is the hex sha256 of the body.
func (c *Client) sign(req *http.Request, payload string) {
	t := c.now().UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)
	names := []string{"host"}
	vals := map[string]string{"host": req.Host}
	if req.Host == "" {
		vals["host"] = req.URL.Host
	}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "authorization" || lk == "user-agent" || lk == "content-length" {
			continue
		}
		names = append(names, lk)
		vals[lk] = strings.TrimSpace(strings.Join(v, ","))
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	// canonical query: every key and value escaped, sorted by key
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			qs = append(qs, escape(k, true)+"="+escape(v, true))
		}
	}
	path := req.URL.EscapedPath()
	if p, err := url.PathUnescape(path); err == nil {
		path = escape(p, false)
	}
	if path == "" {
		path = "/"
	}
	canon := strings.Join([]string{req.Method, path, strings.Join(qs, "&"), ch.String(), signed, payload}, "\n")
	scope := day + "/" + c.region() + "/s3/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hashHex([]byte(canon))
	k := hmacSHA([]byte("AWS4"+c.SecretKey), day)
	k = hmacSHA(k, c.region())
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func (c *Client) url(key string, q url.Values) string {
	u := strings.TrimRight(c.Endpoint, "/") + "/" + escape(c.Bucket, true)
	if key != "" {
		u += "/" + escape(key, false)
	}
	if len(q) > 0 {
		u += "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	}
	return u
}

// Error is an answer from the service that is not a success.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("S3 %d %s: %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("S3 answered %d", e.Status)
}

func (c *Client) do(ctx context.Context, method, key string, q url.Values, body io.ReadSeeker, size int64, payload string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(key, q), nil)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Body = io.NopCloser(body)
		req.ContentLength = size
		req.GetBody = func() (io.ReadCloser, error) {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			return io.NopCloser(body), nil
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c.sign(req, payload)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var x struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(b, &x)
		return nil, &Error{Status: resp.StatusCode, Code: x.Code, Msg: x.Message}
	}
	return resp, nil
}

func emptyHash() string { return hashHex(nil) }

// hashSection reads a section once for its sha256.
func hashSection(r *io.SectionReader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	_, err := r.Seek(0, io.SeekStart)
	return hex.EncodeToString(h.Sum(nil)), err
}

// PutBytes stores a small object.
func (c *Client) PutBytes(ctx context.Context, key string, b []byte) error {
	resp, err := c.do(ctx, "PUT", key, nil, bytes.NewReader(b), int64(len(b)), hashHex(b), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// PutFile uploads a file under key: one request up to PartSize, a multipart upload above (aborted on failure).
func (c *Client) PutFile(ctx context.Context, key, path string, progress func(done, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	total := fi.Size()
	if total <= PartSize {
		sec := io.NewSectionReader(f, 0, total)
		h, err := hashSection(sec)
		if err != nil {
			return err
		}
		resp, err := c.do(ctx, "PUT", key, nil, sec, total, h, nil)
		if err != nil {
			return err
		}
		if progress != nil {
			progress(total, total)
		}
		return resp.Body.Close()
	}
	resp, err := c.do(ctx, "POST", key, url.Values{"uploads": {""}}, nil, 0, emptyHash(), nil)
	if err != nil {
		return err
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	err = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&init)
	resp.Body.Close()
	if err != nil || init.UploadID == "" {
		return errors.New("S3: no upload id")
	}
	abort := func() {
		actx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if r, err := c.do(actx, "DELETE", key, url.Values{"uploadId": {init.UploadID}}, nil, 0, emptyHash(), nil); err == nil {
			r.Body.Close()
		}
	}
	type part struct {
		XMLName    xml.Name `xml:"Part"`
		PartNumber int      `xml:"PartNumber"`
		ETag       string   `xml:"ETag"`
	}
	var parts []part
	for n, off := 1, int64(0); off < total; n, off = n+1, off+PartSize {
		sz := min(PartSize, total-off)
		sec := io.NewSectionReader(f, off, sz)
		h, err := hashSection(sec)
		if err != nil {
			abort()
			return err
		}
		var r *http.Response
		for try := 0; ; try++ {
			_, _ = sec.Seek(0, io.SeekStart)
			r, err = c.do(ctx, "PUT", key, url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {init.UploadID}}, sec, sz, h, nil)
			if err == nil || try == 2 || ctx.Err() != nil {
				break
			}
			time.Sleep(time.Duration(try+1) * retryWait)
		}
		if err != nil {
			abort()
			return err
		}
		etag := r.Header.Get("ETag")
		r.Body.Close()
		parts = append(parts, part{PartNumber: n, ETag: etag})
		if progress != nil {
			progress(off+sz, total)
		}
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part
	}{Parts: parts})
	resp, err = c.do(ctx, "POST", key, url.Values{"uploadId": {init.UploadID}}, bytes.NewReader(body), int64(len(body)), hashHex(body), map[string]string{"Content-Type": "application/xml"})
	if err != nil {
		abort()
		return err
	}
	// a 200 answer can still carry an error
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if bytes.Contains(b, []byte("<Error>")) {
		abort()
		return errors.New("S3: the upload could not be completed")
	}
	return nil
}

// List lists the objects under prefix.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for range 100 {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, "GET", "", q, nil, 0, emptyHash(), nil)
		if err != nil {
			return nil, err
		}
		var x struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated bool   `xml:"IsTruncated"`
			Next        string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&x)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("S3: list: %v", err)
		}
		for _, o := range x.Contents {
			out = append(out, Object{o.Key, o.Size})
		}
		if !x.IsTruncated || x.Next == "" {
			return out, nil
		}
		token = x.Next
	}
	return out, nil
}

// Delete removes one object.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "DELETE", key, nil, nil, 0, emptyHash(), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
