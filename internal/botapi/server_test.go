package botapi

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func TestMultipartBodyLimit(t *testing.T) {
	var body bytes.Buffer
	var mw *multipart.Writer
	var file io.Writer
	var r = httptest.NewRequest("POST", "/botx/sendDocument", nil)
	var p params
	var err error
	var header *multipart.FileHeader
	mw = multipart.NewWriter(&body)
	mw.WriteField("chat_id", "1")
	file, _ = mw.CreateFormFile("document", "big.bin")
	file.Write(make([]byte, 2*maxBody))
	mw.Close()
	r = httptest.NewRequest("POST", "/botx/sendDocument", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	p, err = readParams(r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		t.Fatal(err)
	}
	header, _ = p["document"].(*multipart.FileHeader)
	if header == nil || header.Size != 2*maxBody || p.str("chat_id") != "1" {
		t.Fatalf("multipart file: %v", p)
	}
	body.Reset()
	mw = multipart.NewWriter(&body)
	file, _ = mw.CreateFormFile("document", "too-big.bin")
	io.CopyN(file, &repeatReader{}, maxUpload+1)
	mw.Close()
	r = httptest.NewRequest("POST", "/botx/sendDocument", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	_, err = readParams(r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err == nil {
		t.Fatal("oversized upload accepted")
	}
}

type repeatReader struct{}

func (*repeatReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
