package agent

// File manager and database calls (SITE-02). Transfers stream through the panel; nothing is kept on its disk.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Entry is one row of a folder listing.
type Entry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified int64  `json:"modified"`
	Target   string `json:"target"`
}

// Listing is one folder.
type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// FileTrash is a deleted file or folder.
type FileTrash struct {
	Entry   string `json:"entry"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Deleted int64  `json:"deleted"`
	Purge   int64  `json:"purge"`
}

// Table is one database table.
type Table struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Rows      int64  `json:"rows"`
	Data      int64  `json:"data"`
	Index     int64  `json:"index"`
	Collation string `json:"collation"`
}

// Dump is a kept database copy.
type Dump struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// DB is the overview of a site database.
type DB struct {
	Name    string  `json:"name"`
	User    string  `json:"user"`
	Host    string  `json:"host"`
	Version string  `json:"version"`
	Size    int64   `json:"size"`
	Tables  []Table `json:"tables"`
	Backups []Dump  `json:"backups"`
}

// Result is one result set of the SQL console.
type Result struct {
	Statement string      `json:"statement"`
	Columns   []string    `json:"columns"`
	Rows      [][]*string `json:"rows"`
	Limited   bool        `json:"limited"`
}

// QueryAnswer is one console run.
type QueryAnswer struct {
	Results []Result `json:"results"`
	Millis  int64    `json:"ms"`
}

func site(id string) string { return "/v1/sites/" + url.PathEscape(id) }

// stream sends a raw request and returns the open answer; an agent error is read and returned instead.
func (c *Client) stream(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if c.hc == nil {
		return nil, ErrNoAgent
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	hc := &http.Client{Transport: c.hc.Transport} // no overall timeout: the context decides
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNoAgent
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("agent answered %d", resp.StatusCode)
		}
		return nil, &Error{Code: resp.StatusCode, Msg: e.Error}
	}
	return resp, nil
}

// Files lists one folder of a website.
func (c *Client) Files(ctx context.Context, id, p string) (Listing, error) {
	var v Listing
	return v, c.call(ctx, "GET", site(id)+"/files?"+url.Values{"path": {p}}.Encode(), nil, &v)
}

// Download opens a file of a website. The caller closes the answer body.
func (c *Client) Download(ctx context.Context, id, p string) (*http.Response, error) {
	return c.stream(ctx, "GET", site(id)+"/files/content?"+url.Values{"path": {p}}.Encode(), nil)
}

// Zip streams a folder as a zip. The caller closes the answer body.
func (c *Client) Zip(ctx context.Context, id, p string) (*http.Response, error) {
	return c.stream(ctx, "GET", site(id)+"/files/zip?"+url.Values{"path": {p}}.Encode(), nil)
}

// PutFile writes a file from body (overwrite replaces an existing file).
func (c *Client) PutFile(ctx context.Context, id, p string, overwrite bool, body io.Reader) (Entry, error) {
	q := url.Values{"path": {p}}
	if overwrite {
		q.Set("overwrite", "1")
	}
	resp, err := c.stream(ctx, "PUT", site(id)+"/files/content?"+q.Encode(), body)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()
	var e Entry
	return e, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
}

// FileOp runs mkdir, rename, chmod, delete or unzip.
func (c *Client) FileOp(ctx context.Context, id, op, p, to, mode string) (map[string]any, error) {
	body := map[string]string{"path": p}
	if to != "" {
		body["to"] = to
	}
	if mode != "" {
		body["mode"] = mode
	}
	var v map[string]any
	return v, c.call(ctx, "POST", site(id)+"/files/"+url.PathEscape(op), body, &v)
}

// FilesTrash lists a website's deleted files.
func (c *Client) FilesTrash(ctx context.Context, id string) ([]FileTrash, error) {
	var v struct {
		Trash []FileTrash `json:"trash"`
	}
	return v.Trash, c.call(ctx, "GET", site(id)+"/files/trash", nil, &v)
}

// RestoreFile puts a deleted file back.
func (c *Client) RestoreFile(ctx context.Context, id, entry string) (string, error) {
	var v struct {
		Path string `json:"path"`
	}
	return v.Path, c.call(ctx, "POST", site(id)+"/files/trash/"+url.PathEscape(entry)+"/restore", nil, &v)
}

// Database reads the tables of a website database.
func (c *Client) Database(ctx context.Context, id string) (DB, error) {
	var v DB
	return v, c.call(ctx, "GET", site(id)+"/db", nil, &v)
}

// Query runs console SQL.
func (c *Client) Query(ctx context.Context, id, sql string) (QueryAnswer, error) {
	var v QueryAnswer
	return v, c.call(ctx, "POST", site(id)+"/db/query", map[string]string{"sql": sql}, &v)
}

// Export streams a fresh .sql.gz of the database. The caller closes the answer body.
func (c *Client) Export(ctx context.Context, id string) (*http.Response, error) {
	return c.stream(ctx, "GET", site(id)+"/db/export", nil)
}

// Backup streams a kept database copy. The caller closes the answer body.
func (c *Client) Backup(ctx context.Context, id, name string) (*http.Response, error) {
	return c.stream(ctx, "GET", site(id)+"/db/backups/"+url.PathEscape(name), nil)
}

// Import feeds a .sql or .sql.gz to the database; the agent keeps a copy first and names it.
func (c *Client) Import(ctx context.Context, id string, body io.Reader) (string, error) {
	resp, err := c.stream(ctx, "PUT", site(id)+"/db/import", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		Backup string `json:"backup"`
	}
	return v.Backup, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v)
}
