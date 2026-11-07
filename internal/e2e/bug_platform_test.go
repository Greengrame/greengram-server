package e2e

import (
	"context"
	"errors"
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

func TestBugsPlatform(t *testing.T) {
	var store = db(t)
	var ctx = context.Background()
	var service = bots.New(store.Pool)
	var author int64
	var voter int64
	var card int64
	var private int64
	var err error
	var cards []bots.BugCard
	var comments []bots.BugComment
	var refusal *account.Error
	var index int
	if err = store.Pool.QueryRow(ctx, "INSERT INTO users(username,name,password) VALUES('reporter','Reporter','!test') RETURNING id").Scan(&author); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, "INSERT INTO users(username,name,password) VALUES('voter','Voter','!test') RETURNING id").Scan(&voter); err != nil {
		t.Fatal(err)
	}
	private, err = service.FileReport(ctx, author, bots.ReportBug, "A private report from the old bot", 1)
	if err != nil || private == 0 {
		t.Fatalf("legacy report: %d %v", private, err)
	}
	card, err = service.CreateBugCard(ctx, author, bots.ReportBug, "Search preserves %_", "Steps to reproduce")
	if err != nil {
		t.Fatal(err)
	}
	cards, err = service.BugCards(ctx, 0, bots.BugFilter{Sort: "rate"}, 0)
	if err != nil || len(cards) != 1 || cards[0].ID != card {
		t.Fatalf("private reports leaked into the platform: %+v %v", cards, err)
	}
	if err = service.VoteBugCard(ctx, voter, card, 1); err != nil {
		t.Fatal(err)
	}
	if err = service.VoteBugCard(ctx, voter, card, 1); err != nil {
		t.Fatal(err)
	}
	cards, err = service.BugCards(ctx, voter, bots.BugFilter{}, card)
	if err != nil || len(cards) != 1 || cards[0].Likes != 1 || cards[0].Vote != 1 {
		t.Fatalf("duplicate vote: %+v %v", cards, err)
	}
	if err = service.VoteBugCard(ctx, voter, card, -1); err != nil {
		t.Fatal(err)
	}
	cards, err = service.BugCards(ctx, voter, bots.BugFilter{}, card)
	if err != nil || len(cards) != 1 || cards[0].Likes != 0 || cards[0].Dislikes != 1 {
		t.Fatalf("changed vote: %+v %v", cards, err)
	}
	if err = service.CommentBugCard(ctx, voter, private, "This should not be public"); !errors.As(err, &refusal) || refusal.Status != 404 {
		t.Fatalf("comment on private report: %v", err)
	}
	if err = service.CommentBugCard(ctx, voter, card, "  Additional information  "); err != nil {
		t.Fatal(err)
	}
	comments, err = service.BugComments(ctx, card)
	if err != nil || len(comments) != 1 || comments[0].Text != "Additional information" {
		t.Fatalf("comments: %+v %v", comments, err)
	}
	if err = service.SetBugCardStatus(ctx, account.User{ID: voter}, card, "fixed"); !errors.As(err, &refusal) || refusal.Status != 403 {
		t.Fatalf("non-admin changed status: %v", err)
	}
	if err = service.SetBugCardStatus(ctx, account.User{ID: author}, card, "closed"); !errors.As(err, &refusal) || refusal.Status != 403 {
		t.Fatalf("the author changed the status: %v", err)
	}
	if err = service.SetBugCardStatus(ctx, account.User{ID: author, Role: "admin"}, card, "duplicate"); !errors.As(err, &refusal) || refusal.Status != 400 {
		t.Fatalf("unknown status accepted: %v", err)
	}
	for _, status := range bots.BugStatuses {
		if err = service.SetBugCardStatus(ctx, account.User{ID: author, Role: "admin"}, card, status); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}
	if err = service.SetBugCardStatus(ctx, account.User{ID: author, Role: "admin"}, card, "fixed"); err != nil {
		t.Fatal(err)
	}
	cards, err = service.BugCards(ctx, voter, bots.BugFilter{Query: "%_", Status: "fixed"}, 0)
	if err != nil || len(cards) != 1 {
		t.Fatalf("literal search or status filter failed: %+v %v", cards, err)
	}
	cards, err = service.BugCards(ctx, voter, bots.BugFilter{Kind: bots.ReportSuggestion}, 0)
	if err != nil || len(cards) != 0 {
		t.Fatalf("suggestion filter: %+v %v", cards, err)
	}

	if err = service.EditBugCard(ctx, account.User{ID: voter}, card, "Unauthorized title", "An unauthorized edit"); !errors.As(err, &refusal) || refusal.Status != 404 {
		t.Fatalf("unauthorized edit: %v", err)
	}
	if err = service.EditBugCard(ctx, account.User{ID: author}, card, "Updated title %_", "Updated steps"); err != nil {
		t.Fatal(err)
	}
	if err = service.DeleteBugCard(ctx, account.User{ID: voter}, card); !errors.As(err, &refusal) || refusal.Status != 404 {
		t.Fatalf("unauthorized delete: %v", err)
	}
	for index = 0; index < 3; index++ {
		if _, err = service.CreateBugCard(ctx, author, bots.ReportSuggestion, "A suggestion", "An idea"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.CreateBugCard(ctx, author, bots.ReportBug, "Too many reports", "Should be limited"); !errors.As(err, &refusal) || refusal.Status != 429 {
		t.Fatalf("hourly limit: %v", err)
	}
	if err = service.DeleteBugCard(ctx, account.User{ID: author}, card); err != nil {
		t.Fatal(err)
	}
	cards, err = service.BugCards(ctx, author, bots.BugFilter{}, card)
	if err != nil || len(cards) != 0 {
		t.Fatalf("deleted card is still public: %+v %v", cards, err)
	}
}

func TestBugsPlatformAttachments(t *testing.T) {
	var store = db(t)
	var ctx = context.Background()
	var service = bots.New(store.Pool)
	var blobs, err = testBlobs(t)
	var mediaService = media.New(store.Pool, media.Config{Blobs: blobs, Parts: t.TempDir(), DC: dc})
	var author int64
	var other int64
	var document media.Document
	var card int64
	var files []bots.BugFile
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, "INSERT INTO users(username,name,password) VALUES('reporter','Reporter','!test') RETURNING id").Scan(&author); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, "INSERT INTO users(username,name,password) VALUES('other','Other','!test') RETURNING id").Scan(&other); err != nil {
		t.Fatal(err)
	}
	document, err = mediaService.NewDocument(ctx, author, "screenshot.png", testPNG(64, 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateBugCardWithFiles(ctx, other, bots.ReportBug, "Cannot borrow attachments", "Description", []bots.BugUpload{{ID: document.ID, Name: "screenshot.png"}}); err == nil {
		t.Fatal("another user's document was published")
	}
	card, err = service.CreateBugCardWithFiles(ctx, author, bots.ReportBug, "A screenshot", "Description", []bots.BugUpload{{ID: document.ID, Name: "screenshot.png"}})
	if err != nil {
		t.Fatal(err)
	}
	files, err = service.BugFiles(ctx, card, document.ID)
	if err != nil || len(files) != 1 || files[0].Name != "screenshot.png" {
		t.Fatalf("attachment: %+v %v", files, err)
	}
	if err = service.DeleteBugCard(ctx, account.User{ID: author}, card); err != nil {
		t.Fatal(err)
	}
	files, err = service.BugFiles(ctx, 0, document.ID)
	if err != nil || len(files) != 0 {
		t.Fatalf("withdrawn attachment remains public: %+v %v", files, err)
	}
}
