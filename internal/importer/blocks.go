package importer

import "context"

// importBlocks carries block lists over. The web kept no date, so every
// block is dated the import.
func importBlocks(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	if !r.src.has("blocked_users") {
		return c, nil
	}
	rows, err := r.src.db.QueryContext(ctx, "SELECT blocker_id, blocked_id FROM blocked_users ORDER BY blocker_id, blocked_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := newBatch(w, "blocked_users", "blocker_id", "blocked_id", "date")
	date := int32(r.now.Unix())
	for rows.Next() {
		var blocker, blocked int64
		if err := rows.Scan(&blocker, &blocked); err != nil {
			return nil, err
		}
		if blocker == blocked || !r.users[blocker] || !r.users[blocked] {
			c["blocks_skipped"]++
			continue
		}
		if err := out.add(ctx, blocker, blocked, date); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c["blocked_users"] = out.n
	return c, out.flush(ctx)
}
