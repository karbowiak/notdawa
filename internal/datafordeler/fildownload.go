// Package datafordeler is a client for Datafordeler's Fildownload service — the
// durable (post-2026) interface for bulk register extracts. It authenticates
// with an apiKey (no service-user needed) and serves JSON/GPKG/GML/CSV.
//
// Docs: https://confluence.sdfi.dk/pages/viewpage.action?pageId=158367919
package datafordeler

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const defaultBaseURL = "https://api.datafordeler.dk/FileDownloads"

// downloadIdleTimeout aborts a download when the body delivers no bytes for this
// long. Datafordeler's GetFile is known to stall mid-stream and ignores Range
// (no resume), so without this a hung read blocks the import Job until the k8s
// activeDeadlineSeconds backstop — silently losing the run's retry budget.
const downloadIdleTimeout = 2 * time.Minute

// redactErr rewrites any *url.Error in err's chain so the URL no longer carries
// the apikey query parameter. Transport failures (timeouts, resets, DNS) wrap
// the FULL request URL — apikey included — and those messages flow into pod
// logs and the ingest_runs.error ledger column, so they must be scrubbed at the
// source. The url.Error wrapper is preserved (Op + underlying Err) so callers'
// errors.Is/As behavior is unchanged.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) && strings.Contains(ue.URL, "apikey=") {
		return &url.Error{Op: ue.Op, URL: redactURL(ue.URL), Err: ue.Err}
	}
	return err
}

// redactURL replaces the apikey query value in a raw URL string with "***".
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// Unparseable: blunt fallback — drop everything after "apikey=".
		if i := strings.Index(raw, "apikey="); i >= 0 {
			return raw[:i] + "apikey=***"
		}
		return raw
	}
	q := u.Query()
	if q.Has("apikey") {
		q.Set("apikey", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// FileDownload is one available extract from GetAvailableFileDownloads.
type FileDownload struct {
	FileName            string  `json:"fileName"`
	Register            string  `json:"register"`
	EntityName          string  `json:"entityName"`
	TypeOfDownload      string  `json:"typeOfDownload"` // TotalDownload | DeltaDownload
	TypeOfData          string  `json:"typeOfData"`     // Current | Temporal | Bitemporal
	Version             string  `json:"version"`
	GenerationNumber    int     `json:"generationNumber"`
	GenerationTime      string  `json:"generationTime"`
	ExpirationDate      string  `json:"expirationDate"`
	ContainedFileFormat string  `json:"containedFileFormat"` // json | gpkg | gml | csv
	OutputFileFormat    string  `json:"outputFileFormat"`    // zip
	MunicipalityCode    *string `json:"municipalityCode"`
	Md5Hash             *string `json:"md5Hash"`
	FileSizeInBytes     int64   `json:"fileSizeInBytes"`
}

type availableResponse struct {
	Pagination struct {
		CurrentPage int `json:"currentPage"`
		PageSize    int `json:"pageSize"`
		TotalCount  int `json:"totalCount"`
		TotalPages  int `json:"totalPages"`
	} `json:"paginationMetadata"`
	Files []FileDownload `json:"availableFileDownloads"`
}

// Client talks to the Fildownload API.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	// OnProgress, when set, is invoked while Download streams bytes to disk:
	// (downloaded, total) in bytes. total is f.FileSizeInBytes and may be 0 when
	// the server omits it. Calls are throttled (a few per file), so it is safe to
	// drive a terminal progress bar from it. Cleared/ignored when nil.
	OnProgress func(downloaded, total int64)
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: 90 * time.Second},
	}
}

// ListAvailable returns every available file download for a register
// (e.g. "DAR", "DAGI", "MAT", "DS"). Large registers are paginated — the
// server caps the page at 10,000 entries regardless of the requested pagesize
// (MAT alone has >22,000 files), so every page is fetched and concatenated.
// Without this, national extracts that sort onto page 2+ (e.g. the MAT Lodflade
// json total) are silently invisible.
func (c *Client) ListAvailable(ctx context.Context, register string) ([]FileDownload, error) {
	const pageSize = 10000
	var all []FileDownload
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/GetAvailableFileDownloads?Register=%s&pagesize=%d&page=%d&apikey=%s",
			c.BaseURL, url.QueryEscape(register), pageSize, page, url.QueryEscape(c.APIKey))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, redactErr(err)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, redactErr(err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("fildownload list %s page %d: HTTP %d", register, page, resp.StatusCode)
		}
		var out availableResponse
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode fildownload list %s page %d: %w", register, page, err)
		}
		all = append(all, out.Files...)
		// Stop when this page held no rows, or we've reached the last page the
		// server reports (TotalPages may be 0 when omitted — then the empty-page
		// guard ends the loop).
		if len(out.Files) == 0 || (out.Pagination.TotalPages > 0 && page >= out.Pagination.TotalPages) {
			break
		}
	}
	return all, nil
}

// FileURL builds the direct download URL for a given file name.
func (c *Client) FileURL(fileName string) string {
	return fmt.Sprintf("%s/GetFile?Filename=%s&apikey=%s",
		c.BaseURL, url.QueryEscape(fileName), url.QueryEscape(c.APIKey))
}

// Download streams a file to a temp file on disk and returns its path. The
// caller owns the file and must remove it. Extracts can be multi-GB (MAT), so
// this uses an untimed HTTP client and relies on ctx for cancellation. When the
// FileDownload carries an Md5Hash, the bytes are verified and a mismatch is an
// error (the temp file is removed).
func (c *Client) Download(ctx context.Context, f FileDownload) (path string, err error) {
	// Watchdog context: aborts the in-flight body read when the stream stalls
	// (see downloadIdleTimeout). cancel is also the normal cleanup.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FileURL(f.FileName), nil)
	if err != nil {
		return "", redactErr(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", f.FileName, redactErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", f.FileName, resp.StatusCode)
	}

	var stalled atomic.Bool
	watchdog := time.AfterFunc(downloadIdleTimeout, func() {
		stalled.Store(true)
		cancel()
	})
	defer watchdog.Stop()
	body := &idleWatchdogReader{r: resp.Body, timer: watchdog, stalled: &stalled}

	// Sanitise the temp pattern: f.FileName is server-supplied, and os.CreateTemp
	// rejects a pattern containing a path separator. Keep only the extension.
	tmp, err := os.CreateTemp("", "notdawa-*"+filepath.Ext(f.FileName))
	if err != nil {
		return "", err
	}
	// On any error after this point, close and drop the partial file. On the
	// success path tmp is closed explicitly below (err nil), so this is a no-op.
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	writers := []io.Writer{tmp}
	var h hash.Hash
	if f.Md5Hash != nil && *f.Md5Hash != "" {
		h = md5.New()
		writers = append(writers, h)
	}
	if c.OnProgress != nil {
		writers = append(writers, &progressWriter{total: f.FileSizeInBytes, fn: c.OnProgress})
	}
	n, err := io.Copy(io.MultiWriter(writers...), body)
	if err != nil {
		if stalled.Load() {
			return "", fmt.Errorf("download %s: stalled (no bytes for %s after %d bytes): %w",
				f.FileName, downloadIdleTimeout, n, redactErr(err))
		}
		return "", fmt.Errorf("download %s: %w", f.FileName, redactErr(err))
	}
	// Catch a truncated body even when no md5 is supplied (DAGI often omits it).
	if f.FileSizeInBytes > 0 && n != f.FileSizeInBytes {
		err = fmt.Errorf("download %s: short read (%d of %d bytes)", f.FileName, n, f.FileSizeInBytes)
		return "", err
	}
	if h != nil {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, *f.Md5Hash) {
			err = fmt.Errorf("download %s: md5 mismatch (got %s, want %s)", f.FileName, got, *f.Md5Hash)
			return "", err
		}
	}
	// Close explicitly so a final-flush error (disk full, I/O) is surfaced
	// rather than swallowed by a deferred close on the success path.
	if err = tmp.Close(); err != nil {
		return "", fmt.Errorf("download %s: close: %w", f.FileName, err)
	}
	return tmp.Name(), nil
}

// idleWatchdogReader resets the stall watchdog on every successful read. When
// the timer fires (no bytes for downloadIdleTimeout) the request context is
// cancelled, which unblocks the pending Read with an error — turning an
// indefinite hang into a prompt, retryable failure.
type idleWatchdogReader struct {
	r       io.Reader
	timer   *time.Timer
	stalled *atomic.Bool
}

func (w *idleWatchdogReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 && !w.stalled.Load() {
		w.timer.Reset(downloadIdleTimeout)
	}
	return n, err
}

// progressWriter is a no-op io.Writer that counts bytes and forwards the running
// total to a callback, throttled to at most one call per 256 KiB (plus a final
// call at completion) so a progress UI redraws a bounded number of times.
type progressWriter struct {
	total      int64
	written    int64
	lastReport int64
	fn         func(downloaded, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	if p.written-p.lastReport >= 256<<10 || (p.total > 0 && p.written >= p.total) {
		p.lastReport = p.written
		p.fn(p.written, p.total)
	}
	return n, nil
}
