package botapi

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

// files aren't taken yet, so a multipart body is held to maxBody like the
// others instead of spilling to temp files
func TestMultipartBodyLimit(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("chat_id", "1")
	f, _ := mw.CreateFormFile("document", "big.bin")
	f.Write(make([]byte, 2*maxBody))
	mw.Close()
	r := httptest.NewRequest("POST", "/botx/sendDocument", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if _, err := readParams(r); err == nil {
		t.Fatal("an oversized multipart body was read")
	}

	body.Reset()
	mw = multipart.NewWriter(&body)
	mw.WriteField("chat_id", "1")
	mw.Close()
	r = httptest.NewRequest("POST", "/botx/sendMessage", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	p, err := readParams(r)
	if err != nil || p.str("chat_id") != "1" {
		t.Fatalf("small multipart: %v %v", p, err)
	}
}
