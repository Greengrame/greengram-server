package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
)

func (a *API) registerPhotos() {
	handle(a, tlprofile.SemanticMethodPhotosUploadProfilePhoto, a.uploadProfilePhoto)
	handle(a, tlprofile.SemanticMethodPhotosUpdateProfilePhoto, a.updateProfilePhoto)
	handle(a, tlprofile.SemanticMethodPhotosGetUserPhotos, a.getUserPhotos)
	handle(a, tlprofile.SemanticMethodPhotosDeletePhotos, a.deletePhotos)
}

// photoVisible applies owner's profilePhoto privacy rule to viewer.
func (a *API) photoVisible(ctx context.Context, owner, viewer int64) bool {
	return a.canSee(ctx, owner, viewer, "profilePhoto")
}

// setUserPhoto puts u's photo on the user object the audience's viewer gets.
func (a *API) setUserPhoto(t *tg.User, u account.User, au *audience) {
	if u.PhotoID != 0 && au.can(u.ID, "profilePhoto") {
		t.Photo = &tg.UserProfilePhoto{PhotoID: u.PhotoID, DCID: a.in.DC}
	}
}

// photoChanged answers a profile photo change and tells the user's other
// apps; contacts see it the next time they load the user.
func (a *API) photoChanged(ctx context.Context, me account.User, photo tg.PhotoClass) (*tg.PhotosPhoto, error) {
	users, err := a.usersFor(ctx, me.ID, []int64{me.ID})
	if err != nil {
		return nil, err
	}
	// the reply and the push each get their own user object: td sets
	// flags while encoding
	pushed, err := a.usersFor(ctx, me.ID, []int64{me.ID})
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateUser{UserID: tgUserID(me)}}, pushed)
	go a.tellWatchers(me.ID, func(context.Context, int64) (tg.UpdateClass, bool) {
		return &tg.UpdateUser{UserID: tgUserID(me)}, true
	})
	return &tg.PhotosPhoto{Photo: photo, Users: users}, nil
}

func (a *API) uploadProfilePhoto(ctx context.Context, r *tg.PhotosUploadProfilePhotoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.Bot != nil || r.Fallback {
		return nil, tgerr.New(400, "PHOTO_FALLBACK_UNSUPPORTED")
	}
	f, ok := r.GetFile()
	if !ok {
		// video avatars need a transcoder we don't have
		return nil, tgerr.New(400, "PHOTO_FILE_MISSING")
	}
	p, err := a.media.UploadPhoto(ctx, me.ID, f)
	if err != nil {
		return nil, err
	}
	if err := a.media.SetUserPhoto(ctx, me.ID, p.ID); err != nil {
		return nil, err
	}
	me.PhotoID = p.ID
	return a.photoChanged(ctx, me, a.media.PhotoTL(p))
}

// updateProfilePhoto picks an earlier photo, any photo the user can see
// (a chat picture they "set as profile photo"), or none.
func (a *API) updateProfilePhoto(ctx context.Context, r *tg.PhotosUpdateProfilePhotoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.Bot != nil || r.Fallback {
		return nil, tgerr.New(400, "PHOTO_FALLBACK_UNSUPPORTED")
	}
	if _, empty := r.ID.(*tg.InputPhotoEmpty); empty {
		if err := a.media.ClearUserPhoto(ctx, me.ID); err != nil {
			return nil, err
		}
		me.PhotoID = 0
		return a.photoChanged(ctx, me, &tg.PhotoEmpty{})
	}
	p, err := a.media.InputPhoto(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if err := a.media.SetUserPhoto(ctx, me.ID, p.ID); err != nil {
		return nil, err
	}
	me.PhotoID = p.ID
	return a.photoChanged(ctx, me, a.media.PhotoTL(p))
}

func (a *API) getUserPhotos(ctx context.Context, r *tg.PhotosGetUserPhotosRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.inputUser(ctx, me, r.UserID)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, []int64{id})
	if err != nil {
		return nil, err
	}
	if !a.photoVisible(ctx, id, me.ID) {
		return &tg.PhotosPhotos{Photos: []tg.PhotoClass{}, Users: users}, nil
	}
	list, total, err := a.media.UserPhotos(ctx, id, r.Offset, r.Limit, r.MaxID)
	if err != nil {
		return nil, err
	}
	photos := make([]tg.PhotoClass, 0, len(list))
	for _, p := range list {
		photos = append(photos, a.media.PhotoTL(p))
	}
	if r.Offset <= 0 && r.MaxID == 0 && len(photos) == total {
		return &tg.PhotosPhotos{Photos: photos, Users: users}, nil
	}
	return &tg.PhotosPhotosSlice{Count: total, Photos: photos, Users: users}, nil
}

func (a *API) deletePhotos(ctx context.Context, r *tg.PhotosDeletePhotosRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var list []int64
	for _, in := range r.ID {
		if v, ok := in.(*tg.InputPhoto); ok {
			list = append(list, v.ID)
		}
	}
	gone, err := a.media.DeleteUserPhotos(ctx, me.ID, list)
	if err != nil {
		return nil, err
	}
	if len(gone) > 0 {
		users, err := a.usersFor(ctx, me.ID, []int64{me.ID})
		if err != nil {
			return nil, err
		}
		a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateUser{UserID: tgUserID(me)}}, users)
	}
	return gone, nil
}
