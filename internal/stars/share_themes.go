package stars

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

func (s *Service) ShareThemeBackdrops(ctx context.Context) ([]tg.ShareThemeBackdropOption, error) {
	var rows pgx.Rows
	var err error
	var out = []tg.ShareThemeBackdropOption{}
	var backdrop tg.ShareThemeBackdropOption
	rows, err = s.db.Query(ctx, `SELECT DISTINCT ON (backdrop_id) backdrop_id,name,center_color,edge_color,pattern_color,text_color FROM collectible_backdrops ORDER BY backdrop_id,set_id,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		err = rows.Scan(&backdrop.BackdropID, &backdrop.Name, &backdrop.CenterColor, &backdrop.EdgeColor, &backdrop.PatternColor, &backdrop.TextColor)
		if err != nil {
			return nil, err
		}
		out = append(out, backdrop)
	}
	return out, rows.Err()
}
