package proxy

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
)

// Rewrite only the model form field. Copy part headers and bytes, including
// repeated provider extensions, filenames, and audio, without transcoding.
func transcriptionBody(body []byte, contentType string, backend Backend) ([]byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", fmt.Errorf("transcriptions require multipart/form-data")
	}
	if backend.TargetModel == "" {
		return body, contentType, nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var out bytes.Buffer
	writer := multipart.NewWriter(&out)
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		dst, err := writer.CreatePart(part.Header)
		if err != nil {
			return nil, "", err
		}
		if part.FormName() == "model" && part.FileName() == "" {
			_, err = io.WriteString(dst, backend.TargetModel)
		} else {
			_, err = io.Copy(dst, part)
		}
		_ = part.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), writer.FormDataContentType(), nil
}
