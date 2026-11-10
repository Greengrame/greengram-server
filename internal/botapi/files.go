package botapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

const maxUpload = 50 << 20

type fileID struct {
	Kind string `json:"k"`
	ID   int64  `json:"i"`
	Hash int64  `json:"h"`
	Size string `json:"s,omitempty"`
}

func encodeFileID(bot bots.Bot, file fileID) string {
	var raw []byte
	var mac = hmac.New(sha256.New, bot.WebAppKey)
	raw, _ = json.Marshal(file)
	mac.Write([]byte(strconv.FormatInt(bot.UserID, 10)))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(raw, mac.Sum(nil)...))
}

func decodeFileID(bot bots.Bot, value string) (fileID, error) {
	var file fileID
	var raw []byte
	var err error
	var mac = hmac.New(sha256.New, bot.WebAppKey)
	raw, err = base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) <= sha256.Size || len(raw) > 512 {
		return file, bad("wrong file identifier specified")
	}
	mac.Write([]byte(strconv.FormatInt(bot.UserID, 10)))
	mac.Write(raw[:len(raw)-sha256.Size])
	if !hmac.Equal(mac.Sum(nil), raw[len(raw)-sha256.Size:]) {
		return file, bad("wrong file identifier specified")
	}
	err = json.Unmarshal(raw[:len(raw)-sha256.Size], &file)
	if err != nil || file.ID <= 0 || (file.Kind != "photo" && file.Kind != "document") {
		return file, bad("wrong file identifier specified")
	}
	return file, nil
}

func fileUniqueID(file fileID) string {
	var sum [32]byte
	sum = sha256.Sum256([]byte(file.Kind + ":" + strconv.FormatInt(file.ID, 10) + ":" + file.Size))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func (s *Server) locateFile(c *call, value string) (fileID, media.File, error) {
	var id fileID
	var file media.File
	var err error
	var location tg.InputFileLocationClass
	if s.Media == nil {
		return id, file, bad("media service is unavailable")
	}
	id, err = decodeFileID(c.bot, value)
	if err != nil {
		return id, file, err
	}
	if id.Kind == "photo" {
		location = &tg.InputPhotoFileLocation{ID: id.ID, AccessHash: id.Hash, ThumbSize: id.Size}
	} else {
		location = &tg.InputDocumentFileLocation{ID: id.ID, AccessHash: id.Hash, ThumbSize: id.Size}
	}
	file, err = s.Media.Locate(c.ctx, location)
	return id, file, err
}

func (s *Server) getFile(c *call) (any, error) {
	var id fileID
	var file media.File
	var err error
	var value = c.p.str("file_id")
	id, file, err = s.locateFile(c, value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"file_id": value, "file_unique_id": fileUniqueID(id), "file_size": file.Size, "file_path": value}, nil
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	var token, value string
	var found bool
	var bot bots.Bot
	var err error
	var file media.File
	var chunk *tg.UploadFile
	var offset int64
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		write(w, 405, reply{ErrorCode: 405, Description: "Method Not Allowed"})
		return
	}
	token, value, found = strings.Cut(strings.TrimPrefix(r.URL.Path, "/file/bot"), "/")
	if !found || !pathRe.MatchString("/bot"+token+"/getFile") {
		write(w, 404, reply{ErrorCode: 404, Description: "Not Found"})
		return
	}
	bot, found, err = s.Bots.ByToken(r.Context(), token)
	if err != nil {
		write(w, 500, reply{ErrorCode: 500, Description: "Internal Server Error"})
		return
	}
	if !found {
		write(w, 401, reply{ErrorCode: 401, Description: "Unauthorized"})
		return
	}
	_, file, err = s.locateFile(&call{ctx: r.Context(), bot: bot}, value)
	if err != nil {
		write(w, 400, reply{ErrorCode: 400, Description: "Bad Request: file not found"})
		return
	}
	w.Header().Set("Content-Type", file.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment")
	if r.Method == http.MethodHead {
		return
	}
	for offset < file.Size {
		chunk, err = s.Media.Read(r.Context(), file, offset, 1<<20, true)
		if err != nil || len(chunk.Bytes) == 0 {
			return
		}
		_, err = w.Write(chunk.Bytes)
		if err != nil {
			return
		}
		offset += int64(len(chunk.Bytes))
	}
}

func (s *Server) uploadFile(c *call, header *multipart.FileHeader) (*tg.InputFile, error) {
	var source multipart.File
	var err error
	var random [8]byte
	var id int64
	var parts, part, count int
	var buffer = make([]byte, 512<<10)
	if header.Size <= 0 || header.Size > maxUpload {
		return nil, bad("file must contain 1-52428800 bytes")
	}
	source, err = header.Open()
	if err != nil {
		return nil, bad("can't open uploaded file")
	}
	defer source.Close()
	_, err = rand.Read(random[:])
	if err != nil {
		return nil, err
	}
	id = int64(binary.LittleEndian.Uint64(random[:]) >> 1)
	parts = int((header.Size + int64(len(buffer)) - 1) / int64(len(buffer)))
	for part = 0; part < parts; part++ {
		count, err = io.ReadFull(source, buffer)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, bad("can't read uploaded file")
		}
		err = s.Media.SavePart(c.ctx, c.bot.UserID, id, part, 0, buffer[:count])
		if err != nil {
			return nil, err
		}
	}
	return &tg.InputFile{ID: id, Parts: parts, Name: filepath.Base(header.Filename)}, nil
}

func (s *Server) inputFile(c *call, field string) (media.Ref, error) {
	var header *multipart.FileHeader
	var uploaded *tg.InputFile
	var input tg.InputMediaClass
	var id fileID
	var value string
	var err error
	var attrs []tg.DocumentAttributeClass
	var duration, width, height int
	var mimeType string
	if s.Media == nil {
		return media.Ref{}, bad("media service is unavailable")
	}
	header, _ = c.p[field].(*multipart.FileHeader)
	value = c.p.str(field)
	if strings.HasPrefix(value, "attach://") {
		header, _ = c.p[strings.TrimPrefix(value, "attach://")].(*multipart.FileHeader)
	}
	if header == nil {
		id, err = decodeFileID(c.bot, value)
		if err != nil {
			return media.Ref{}, bad("provide a multipart file or a valid file_id")
		}
		if id.Kind == "photo" && field == "photo" {
			input = &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: id.ID, AccessHash: id.Hash}, Spoiler: c.p.bool("has_spoiler")}
		} else if id.Kind == "document" && field != "photo" && id.Size == "" {
			input = &tg.InputMediaDocument{ID: &tg.InputDocument{ID: id.ID, AccessHash: id.Hash}, Spoiler: c.p.bool("has_spoiler")}
		} else {
			return media.Ref{}, bad("file type does not match the requested media")
		}
		return s.Media.FromInput(c.ctx, c.bot.UserID, input)
	}
	duration, err = c.p.int("duration")
	if err != nil || duration < 0 {
		return media.Ref{}, bad("invalid duration")
	}
	width, err = c.p.int("width")
	if err != nil || width < 0 {
		return media.Ref{}, bad("invalid width")
	}
	height, err = c.p.int("height")
	if err != nil || height < 0 {
		return media.Ref{}, bad("invalid height")
	}
	mimeType = header.Header.Get("Content-Type")
	switch field {
	case "video", "video_note", "animation":
		attrs = append(attrs, &tg.DocumentAttributeVideo{Duration: float64(duration), W: width, H: height, RoundMessage: field == "video_note", SupportsStreaming: c.p.bool("supports_streaming")})
		if field == "animation" {
			attrs = append(attrs, &tg.DocumentAttributeAnimated{})
		}
	case "audio", "voice":
		attrs = append(attrs, &tg.DocumentAttributeAudio{Duration: duration, Voice: field == "voice", Title: c.p.str("title"), Performer: c.p.str("performer")})
	case "sticker":
		attrs = append(attrs, &tg.DocumentAttributeSticker{Alt: c.p.str("emoji"), Stickerset: &tg.InputStickerSetEmpty{}})
	}
	if field == "photo" && header.Size > media.MaxPhotoBytes {
		return media.Ref{}, bad("photo is too big")
	}
	uploaded, err = s.uploadFile(c, header)
	if err != nil {
		return media.Ref{}, err
	}
	if field == "photo" {
		input = &tg.InputMediaUploadedPhoto{File: uploaded, Spoiler: c.p.bool("has_spoiler")}
	} else {
		input = &tg.InputMediaUploadedDocument{File: uploaded, MimeType: mimeType, Attributes: attrs, ForceFile: field == "document", Spoiler: c.p.bool("has_spoiler")}
	}
	return s.Media.FromInput(c.ctx, c.bot.UserID, input)
}

func (s *Server) sendFile(c *call, field string) (any, error) {
	var to chat
	var err error
	var out Out
	to, err = s.messageChat(c)
	if err != nil {
		return nil, err
	}
	err = s.messageWritable(c, to)
	if err != nil {
		return nil, err
	}
	out.Text, out.Entities, err = botCaption(c.p)
	if err != nil {
		return nil, err
	}
	out.Markup, _, err = markup(c.p)
	if err != nil {
		return nil, err
	}
	out.ReplyTo, err = replyTo(c.p)
	if err != nil {
		return nil, err
	}
	out.Media, err = s.inputFile(c, field)
	if err != nil {
		return nil, err
	}
	return s.sendCall(c, to, out)
}

func botCaption(p params) (string, []tg.MessageEntityClass, error) {
	var value string
	var entities []tg.MessageEntityClass
	var err error
	if !p.has("caption") {
		return "", nil, nil
	}
	value, entities, err = text(p, "caption", "caption_entities")
	if err == nil && utf8.RuneCountInString(value) > 1024 {
		return "", nil, bad("caption is too long")
	}
	return value, entities, err
}

func fileMethod(field string) method {
	return func(s *Server, c *call) (any, error) { return s.sendFile(c, field) }
}
