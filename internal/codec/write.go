package codec

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeBody(w, status, "application/json; charset=utf-8", body)
}

func writeBody(w http.ResponseWriter, status int, contentType string, body []byte) error {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}
