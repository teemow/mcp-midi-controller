package drive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// UploadRequest is a file to put into the Drive.
type UploadRequest struct {
	// Path is the local file; Name what it is called in the Drive.
	Path string
	Name string
	// MIMEType is the file's media type.
	MIMEType string
	// FolderID is the folder it goes into ("" or "root": the top level).
	FolderID string
	// Description is shown in the file's details.
	Description string
	// Progress, when set, is called as the upload advances.
	Progress func(sent, total int64)
}

// File is a file in the Drive.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Link opens the file in the browser, for whoever may see it. A file the
	// daemon uploads is private to its owner until they share it.
	Link string `json:"webViewLink"`
	MD5  string `json:"md5Checksum"`
	Size int64  `json:"size,string"`
}

const (
	// maxFailures is how many requests in a row may fail before an upload
	// gives up.
	maxFailures = 8
	// maxSessions is how often an upload starts over because Drive dropped
	// its session.
	maxSessions = 3
)

// Upload puts a file into the Drive with a resumable upload: the file goes up
// in chunks, and after a failed request the client asks how much arrived and
// carries on from there. The uploaded file's checksum is compared with the
// local one.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (File, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return File{}, err
	}
	size := info.Size()
	if size == 0 {
		return File{}, fmt.Errorf("%s is empty", req.Path)
	}
	hash := md5.New()
	if _, err := io.Copy(hash, f); err != nil {
		return File{}, err
	}
	sum := hex.EncodeToString(hash.Sum(nil))

	var lastErr error
	for range maxSessions {
		session, err := c.startUpload(ctx, req, size)
		if err != nil {
			return File{}, err
		}
		file, err := c.sendFile(ctx, session, f, size, req.Progress)
		if errors.Is(err, errSessionGone) {
			lastErr = err
			continue
		}
		if err != nil {
			return File{}, err
		}
		if file.ID == "" {
			return File{}, errors.New("the upload completed, but the answer names no file")
		}
		if file.MD5 != "" && file.MD5 != sum {
			return file, fmt.Errorf("the file arrived damaged (its checksum in the Drive is not the local one): delete %q there and upload again", file.Name)
		}
		return file, nil
	}
	return File{}, fmt.Errorf("the upload was started %d times and never completed: %w", maxSessions, lastErr)
}

// errSessionGone: Drive no longer knows the upload session, so the upload
// starts over.
var errSessionGone = errors.New("the upload session is gone at Google Drive")

// fileFields is what an upload asks Drive to say about the new file.
const fileFields = "id,name,webViewLink,md5Checksum,size"

// startUpload opens an upload session and returns its URI.
func (c *Client) startUpload(ctx context.Context, req UploadRequest, size int64) (string, error) {
	meta := map[string]any{"name": req.Name}
	if req.MIMEType != "" {
		meta["mimeType"] = req.MIMEType
	}
	if req.Description != "" {
		meta["description"] = req.Description
	}
	if req.FolderID != "" && req.FolderID != "root" {
		meta["parents"] = []string{req.FolderID}
	}
	body, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	target := c.endpoints.Upload + "/files?" + url.Values{"uploadType": {"resumable"}, "fields": {fileFields}}.Encode()
	resp, err := c.do(ctx, c.callTimeout, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json; charset=UTF-8")
		r.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
		if req.MIMEType != "" {
			r.Header.Set("X-Upload-Content-Type", req.MIMEType)
		}
		return r, nil
	})
	if err != nil {
		return "", fmt.Errorf("start the upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("start the upload: %w", apiError(resp))
	}
	session := resp.Header.Get("Location")
	// The session URI is where the file and the access token go next: it must
	// be on the host the upload was started at.
	su, err := url.Parse(session)
	eu, _ := url.Parse(c.endpoints.Upload)
	if err != nil || session == "" || eu == nil || su.Scheme != eu.Scheme || su.Host != eu.Host {
		return "", errors.New("start the upload: Google Drive named no usable upload session")
	}
	return session, nil
}

// sendFile sends the file to an upload session, chunk by chunk, and returns
// the file Drive made of it.
func (c *Client) sendFile(ctx context.Context, session string, f *os.File, size int64, progress func(sent, total int64)) (File, error) {
	var (
		offset   int64
		failures int
		lastErr  error
	)
	for {
		if failures > 0 {
			if failures > maxFailures {
				return File{}, fmt.Errorf("the upload failed %d times in a row at byte %d of %d: %w", failures, offset, size, lastErr)
			}
			select {
			case <-ctx.Done():
				return File{}, ctx.Err()
			case <-time.After(c.backoff(failures)):
			}
			// Ask how much arrived; the failed request may have delivered
			// some, all or none of its chunk.
			st, err := c.sendChunk(ctx, session, nil, 0, 0, size)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return File{}, ctx.Err()
				}
				if !retryable(err) {
					return File{}, err
				}
				failures, lastErr = failures+1, err
				continue
			case st.done:
				return st.file, nil
			}
			offset = st.next
		}

		n := min(c.chunkSize, size-offset)
		st, err := c.sendChunk(ctx, session, f, offset, n, size)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return File{}, ctx.Err()
			}
			if !retryable(err) {
				return File{}, err
			}
			failures, lastErr = failures+1, err
			continue
		case st.done:
			if progress != nil {
				progress(size, size)
			}
			return st.file, nil
		}
		if st.next <= offset {
			// Accepted, yet nothing arrived: count it, or a server that never
			// advances would be asked forever.
			failures, lastErr = failures+1, errors.New("none of the chunk was taken by Google Drive")
			continue
		}
		offset, failures = st.next, 0
		if progress != nil {
			progress(offset, size)
		}
	}
}

// chunkState is what Drive said after a chunk (or a status query): the file
// when the upload is complete, else the offset of the next byte it wants.
type chunkState struct {
	done bool
	file File
	next int64
}

// errTransient wraps a failure worth another try: the network, a server
// error, a rate limit.
type errTransient struct{ err error }

func (e *errTransient) Error() string { return e.err.Error() }
func (e *errTransient) Unwrap() error { return e.err }

func retryable(err error) bool {
	var te *errTransient
	return errors.As(err, &te)
}

// sendChunk PUTs the n bytes at offset to the session — or, with f nil, asks
// the session how much it holds.
func (c *Client) sendChunk(ctx context.Context, session string, f *os.File, offset, n, size int64) (chunkState, error) {
	timeout := c.callTimeout
	if f != nil {
		timeout = c.chunkTimeout
	}
	resp, err := c.do(ctx, timeout, func(ctx context.Context) (*http.Request, error) {
		var body io.Reader = http.NoBody
		contentRange := fmt.Sprintf("bytes */%d", size)
		if f != nil {
			body = io.NewSectionReader(f, offset, n)
			contentRange = fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, size)
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodPut, session, body)
		if err != nil {
			return nil, err
		}
		r.ContentLength = n
		r.Header.Set("Content-Range", contentRange)
		return r, nil
	})
	if err != nil {
		if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrNoClient) {
			return chunkState{}, err
		}
		return chunkState{}, &errTransient{err}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var file File
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBytes)).Decode(&file); err != nil {
			return chunkState{}, errors.New("the upload completed, but Google Drive's answer does not read")
		}
		return chunkState{done: true, file: file}, nil
	case http.StatusPermanentRedirect: // 308 Resume Incomplete
		next, err := nextOffset(resp.Header.Get("Range"))
		if err != nil || next > size {
			return chunkState{}, fmt.Errorf("the range Google Drive reports does not read: %q", resp.Header.Get("Range"))
		}
		return chunkState{next: next}, nil
	case http.StatusNotFound, http.StatusGone:
		return chunkState{}, errSessionGone
	}
	ae := apiError(resp)
	if ae.Status >= 500 || ae.Status == http.StatusTooManyRequests ||
		(ae.Status == http.StatusForbidden && strings.Contains(strings.ToLower(ae.Reason), "ratelimit")) {
		return chunkState{}, &errTransient{ae}
	}
	return chunkState{}, ae
}

// nextOffset reads the Range header of a 308 ("bytes=0-42"): the offset of
// the first byte Drive does not have yet. No header means it has none.
func nextOffset(header string) (int64, error) {
	if header == "" {
		return 0, nil
	}
	rest, ok := strings.CutPrefix(header, "bytes=0-")
	if !ok {
		return 0, errors.New("unexpected range")
	}
	last, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || last < 0 {
		return 0, errors.New("unexpected range")
	}
	return last + 1, nil
}
