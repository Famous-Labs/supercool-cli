package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"time"
)

// PostFile uploads a file to a presigned S3 POST, streaming it from disk.
// S3 needs an exact Content-Length (no chunked encoding), so the multipart
// envelope is built around the file rather than buffering it.
func PostFile(ctx context.Context, up *Upload, path, contentType string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	// Policy fields first; S3 ignores anything after the file part.
	for k, v := range up.Fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filepath.Base(path)))
	h.Set("Content-Type", contentType)
	if _, err := mw.CreatePart(h); err != nil {
		return err
	}
	tail := "\r\n--" + mw.Boundary() + "--\r\n"
	body := io.MultiReader(bytes.NewReader(head.Bytes()), f, bytes.NewReader([]byte(tail)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, up.URL, body)
	if err != nil {
		return err
	}
	req.ContentLength = int64(head.Len()) + st.Size() + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 2 * time.Hour}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("upload of %s refused (%d): %s", filepath.Base(path), resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
