package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"time"
)

func (a *App) handleTranscription(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	limit := a.Config.ProxySettings.ASR.UploadLimit()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "audio upload is too large", "payload_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "failed to read audio upload", "invalid_request_error")
		}
		return
	}
	payload, err := transcriptionMetadata(body, r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	a.handleParsedProxy(w, r, start, body, payload, func(map[string]any) error { return nil })
}

func transcriptionMetadata(body []byte, contentType string) (map[string]any, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, fmt.Errorf("transcriptions require multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	payload := map[string]any{}
	files, models, streams, parts := 0, 0, 0, 0
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid multipart audio upload")
		}
		parts++
		if parts > 1024 {
			return nil, fmt.Errorf("too many multipart fields")
		}
		name := part.FormName()
		if name == "file" {
			files++
			n, err := io.Copy(io.Discard, part)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("file must contain audio")
			}
		} else if name == "model" || name == "stream" {
			if part.FileName() != "" {
				return nil, fmt.Errorf("%s must be a form field", name)
			}
			value, err := io.ReadAll(io.LimitReader(part, 65537))
			if err != nil || len(value) > 65536 {
				return nil, fmt.Errorf("%s field is too large", name)
			}
			if name == "model" {
				models++
				payload["model"] = string(value)
			} else {
				streams++
				if string(value) != "true" && string(value) != "false" {
					return nil, fmt.Errorf("stream must be true or false")
				}
				payload["stream"] = string(value) == "true"
			}
		}
		if err := part.Close(); err != nil {
			return nil, fmt.Errorf("invalid multipart audio upload")
		}
	}
	if files != 1 || models != 1 || streams > 1 {
		return nil, fmt.Errorf("provide exactly one file, one model, and at most one stream field")
	}
	return payload, nil
}
