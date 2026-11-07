package media

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

func (s *Service) NewDocument(ctx context.Context, owner int64, name string, raw []byte) (Document, error) {
	var d = Document{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Kind: KindFile}
	var err error
	var thumb Size
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	d.Mime = strings.Split(http.DetectContentType(raw), ";")[0]
	d.Key, d.Size, err = s.blobs.Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return d, err
	}
	d.Attrs = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}}
	if strings.HasPrefix(d.Mime, "image/") {
		thumb, err = s.thumb(ctx, raw)
		if err == nil {
			d.Thumb = &thumb
		}
	}
	err = s.tx(ctx, func(tx pgx.Tx) error { return s.insertDocument(ctx, tx, d) })
	return d, err
}

func (s *Service) DocumentFile(ctx context.Context, id int64, thumbnail bool) (Document, []byte, error) {
	var documents []Document
	var err error
	var d Document
	var key string
	var size int64
	var raw []byte
	documents, err = s.Documents(ctx, []int64{id})
	if err != nil {
		return d, nil, err
	}
	if len(documents) != 1 {
		return d, nil, errDocInvalid
	}
	d = documents[0]
	key, size = d.Key, d.Size
	if thumbnail && d.Thumb != nil {
		key, size = d.Thumb.Key, int64(d.Thumb.Bytes)
	}
	if size > 10<<20 {
		return d, nil, errDocInvalid
	}
	raw, err = s.blobs.Read(ctx, key, 0, int(size))
	return d, raw, err
}
