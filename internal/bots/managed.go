package bots

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

func queueManagedBot(ctx context.Context, tx pgx.Tx, manager, owner, bot int64) error {
	var raw []byte
	var users = map[int64]map[string]any{}
	var id int64
	var name, username, first, last string
	var isBot bool
	var err error
	for _, id = range []int64{owner, bot} {
		err = tx.QueryRow(ctx, "SELECT name,username,is_bot FROM users WHERE id=$1", id).Scan(&name, &username, &isBot)
		if err != nil {
			return err
		}
		first, last, _ = strings.Cut(name, " ")
		users[id] = map[string]any{"id": ids.Client(id), "first_name": first, "last_name": last, "username": username, "is_bot": isBot}
	}
	raw, err = json.Marshal(map[string]any{"user": users[owner], "bot": users[bot]})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bot_updates(bot_id,kind,chat_id,payload) VALUES($1,'managed_bot',$2,$3)", manager, owner, raw)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "SELECT pg_notify($1,$2)", channel, strconv.FormatInt(manager, 10))
	return err
}
