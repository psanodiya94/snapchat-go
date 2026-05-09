// Package media handles file uploads for use in messages and stories.
//
// Uploaded files are stored on the local filesystem under the uploads/
// directory configured at startup.  Each file is saved with a randomly
// generated UUID filename (plus the correct extension) to prevent path
// traversal and filename collisions.
//
// # Security
//
// The MIME type is detected from the first 512 bytes of the file content
// using net/http.DetectContentType rather than trusting the Content-Type
// header sent by the client.  Only the types listed in allowedMIME are
// accepted; everything else is rejected with 415 Unsupported Media Type.
//
// # Serving uploaded files
//
// Files are served as static content at /uploads/<filename> by the router
// in main.go.  Store the returned URL in the media_url field when creating
// a message or story.
package media

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// maxUploadSize is the maximum size of a single upload (50 MB).
// Requests larger than this are rejected before the body is read.
const maxUploadSize = 50 << 20 // 50 MB

// allowedMIME maps each permitted content-type to the file extension that
// should be used when saving the file.  The extension is derived from the
// detected MIME type, not from the original filename.
var allowedMIME = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"video/mp4":  ".mp4",
	"video/webm": ".webm",
}

// Upload handles multipart file uploads.
//
// The file must be sent as the "file" field of a multipart/form-data body.
// The original filename is ignored; a UUID-based name is generated instead.
//
// POST /media/upload
// Request: multipart/form-data with a "file" field (max 50 MB).
// Response (201 Created):
//
//	{ "url": "/uploads/<uuid>.<ext>" }
//
// Use the returned URL as the media_url when sending a message or posting a story.
//
// Returns:
//   - 400 if the "file" field is missing.
//   - 413 if the file exceeds 50 MB.
//   - 415 if the file type is not one of: JPEG, PNG, GIF, WebP, MP4, WebM.
func Upload(uploadDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Enforce the size limit before parsing to avoid reading a huge body.
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
		if err := r.ParseMultipartForm(maxUploadSize); err != nil {
			http.Error(w, "file too large (max 50 MB)", http.StatusRequestEntityTooLarge)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file field required", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Read the first 512 bytes to detect the real content type.
		// http.DetectContentType uses the magic bytes, not the Content-Type header.
		buf := make([]byte, 512)
		n, _ := file.Read(buf)
		mime := http.DetectContentType(buf[:n])
		// Strip any parameters after the MIME type (e.g. "; charset=utf-8").
		mime = strings.SplitN(mime, ";", 2)[0]

		ext, ok := allowedMIME[mime]
		if !ok {
			http.Error(w, fmt.Sprintf("unsupported file type: %s", mime), http.StatusUnsupportedMediaType)
			return
		}

		// Discard the original filename and use a UUID to prevent path traversal.
		_ = header
		filename := uuid.New().String() + ext
		dst := filepath.Join(uploadDir, filename)

		out, err := os.Create(dst)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer out.Close()

		// Write the sniffed header bytes first, then stream the remainder.
		if _, err := out.Write(buf[:n]); err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if _, err := io.Copy(out, file); err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"url":"/uploads/%s"}`, filename)
	}
}
