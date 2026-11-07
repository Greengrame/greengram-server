package stars

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// boostCooldown is how long a moved boost stays put (boosts.js).
const boostCooldown = 86400

var (
	errSlotsEmpty   = tgerr.New(400, "SLOTS_EMPTY")
	errSlotsInvalid = tgerr.New(400, "SLOTS_INVALID")
)

const boostLevelMax = 100 // boosts_channel_level_max

// boostSlotsSQL is how many slots account u has at $1: four with Premium, and
// a Premium gift sent adds three for as long as that gift lasts. Telegram
// gives a subscription that came as a gift only one, but here every account
// starts on welcome Premium, so the ledger can't tell those apart.
// Numbers: core.telegram.org/api/config boosts_per_sent_gift, Premium FAQ.
const boostSlotsSQL = `(4 + 3 * (SELECT count(*) FROM star_ledger s WHERE s.user_id = u.id AND s.reason = 'premium_gift' AND s.created_at + s.months * 2592000 > $1))`

// MyBoost is one of a Premium account's slots.
type MyBoost struct {
	Slot     int
	Channel  int64 // 0 = unused
	Date     int
	Expires  int64
	Cooldown int
}

func (s *Service) boostSlots(ctx context.Context, q querier, user int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT "+boostSlotsSQL+" FROM users u WHERE u.id=$2", s.unix(), user).Scan(&n)
	return n, err
}

// MyBoosts is myBoosts(): nothing without Premium, else every slot.
func (s *Service) MyBoosts(ctx context.Context, user int64, premiumUntil int64, premium bool) ([]MyBoost, error) {
	if !premium {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, "SELECT slot, channel_id, assigned_at, cooldown_until FROM channel_boosts WHERE user_id=$1", user)
	if err != nil {
		return nil, err
	}
	have := map[int]MyBoost{}
	for rows.Next() {
		var b MyBoost
		if err := rows.Scan(&b.Slot, &b.Channel, &b.Date, &b.Cooldown); err != nil {
			rows.Close()
			return nil, err
		}
		have[b.Slot] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	n, err := s.boostSlots(ctx, s.db, user)
	if err != nil {
		return nil, err
	}
	out := make([]MyBoost, 0, n)
	for slot := 1; slot <= n; slot++ {
		b := have[slot]
		b.Slot, b.Expires = slot, premiumUntil
		out = append(out, b)
	}
	return out, nil
}

// ApplyBoosts is applyBoosts(). The caller has checked Premium, membership
// and that the channel is a broadcast one. A slot already on this channel
// is left alone; moving one needs its cooldown to be over.
func (s *Service) ApplyBoosts(ctx context.Context, user, channel int64, slots []int) error {
	if len(slots) == 0 {
		return errSlotsEmpty
	}
	n, err := s.boostSlots(ctx, s.db, user)
	if err != nil {
		return err
	}
	if len(slots) > n {
		return errSlotsInvalid
	}
	seen := map[int]bool{}
	for _, slot := range slots {
		if slot < 1 || slot > n || seen[slot] {
			return errSlotsInvalid
		}
		seen[slot] = true
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		now := s.unix()
		for _, slot := range slots {
			var on int64
			var cooldown int
			err := tx.QueryRow(ctx, "SELECT channel_id, cooldown_until FROM channel_boosts WHERE user_id=$1 AND slot=$2 FOR UPDATE", user, slot).Scan(&on, &cooldown)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if on == channel {
				continue
			}
			if cooldown > now {
				return tgerr.New(420, fmt.Sprintf("FLOOD_WAIT_%d", cooldown-now))
			}
			_, err = tx.Exec(ctx, `INSERT INTO channel_boosts(user_id, slot, channel_id, assigned_at, cooldown_until) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (user_id, slot) DO UPDATE SET channel_id=excluded.channel_id, assigned_at=excluded.assigned_at, cooldown_until=excluded.cooldown_until`,
				user, slot, channel, now, now+boostCooldown)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

type Booster struct {
	User    int64
	Slot    int
	Date    int
	Expires int64
}

type BoostStatus struct {
	Level   int
	Current int // boosts the level started at
	Next    int // boosts for the next level; 0 at the top
	Boosts  int
	Mine    []int // viewer's slots on this channel
	List    []Booster
	Gifts   []GiftBoost // what the channel gave away, one row per winner

	GiftBoosts int // what Gifts add up to
}

// A slot past what the account has now (its gift ran out) stops counting.
const liveBoostsSQL = `channel_boosts b JOIN users u ON u.id = b.user_id
	WHERE NOT u.disabled AND u.premium_until > $1 AND u.premium_starts_at <= $1 AND b.slot <= ` + boostSlotsSQL

// chatMembersSQL is the size the curve is measured against, for chat id.
const chatMembersSQL = `(SELECT count(*) FROM channel_members m WHERE m.channel_id = %s AND m.status IN ('creator', 'admin', 'member', 'restricted'))`

// ChannelBoosts is channelBoosts(): only boosts of accounts whose Premium
// is running count.
func (s *Service) ChannelBoosts(ctx context.Context, viewer, channel int64) (BoostStatus, error) {
	var st BoostStatus
	now := s.unix()
	rows, err := s.db.Query(ctx, `SELECT b.user_id, b.slot, b.assigned_at, u.premium_until FROM `+liveBoostsSQL+`
		AND b.channel_id=$2 ORDER BY b.assigned_at DESC, b.user_id, b.slot`, now, channel)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var b Booster
		if err := rows.Scan(&b.User, &b.Slot, &b.Date, &b.Expires); err != nil {
			return st, err
		}
		st.List = append(st.List, b)
		if b.User == viewer {
			st.Mine = append(st.Mine, b.Slot)
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	var members int
	if err := s.db.QueryRow(ctx, "SELECT "+fmt.Sprintf(chatMembersSQL, "$1"), channel).Scan(&members); err != nil {
		return st, err
	}
	if st.Gifts, err = s.channelGiftBoosts(ctx, channel); err != nil {
		return st, err
	}
	st.Boosts = len(st.List)
	for _, g := range st.Gifts {
		st.GiftBoosts += g.Multiplier
	}
	st.Boosts += st.GiftBoosts
	st.Level = boostLevel(st.Boosts, members)
	if st.Level > 0 {
		st.Current = boostsForLevel(st.Level, members)
	}
	if st.Level < boostLevelMax {
		st.Next = boostsForLevel(st.Level+1, members)
	}
	return st, nil
}

// boostsForLevel is the total a chat needs for a level. Telegram doesn't
// publish this; tginfo.me/new-levels-ranking-en measured it after the May 2025
// change: k = members/250 + 1, levels 1-8 cost k*level/2 rounded up, level 9
// k*7, the rest k*level. That has small chats (k=1) reach levels 1 and 2 on
// the same boost, so every level is held to at least one boost more.
func boostsForLevel(level, members int) int {
	k := members/250 + 1
	need := k * level
	switch {
	case level <= 8:
		need = (k*level + 1) / 2
	case level == 9:
		need = k * 7
	}
	return max(need, level)
}

func boostLevel(boosts, members int) int {
	level := 0
	for level < boostLevelMax && boostsForLevel(level+1, members) <= boosts {
		level++
	}
	return level
}

// ChannelLevels is the boost level of those chats that have one, for the
// level field on the chat itself.
func (s *Service) ChannelLevels(ctx context.Context, list []int64) (map[int64]int, error) {
	rows, err := s.db.Query(ctx, `SELECT id, sum(n)::int, `+fmt.Sprintf(chatMembersSQL, "id")+` FROM (
			SELECT b.channel_id AS id, count(*) AS n FROM `+liveBoostsSQL+` AND b.channel_id = ANY($2) GROUP BY b.channel_id
			UNION ALL SELECT channel_id, sum(multiplier) FROM channel_gift_boosts WHERE expires_at > $1 AND channel_id = ANY($2) GROUP BY channel_id
		) x GROUP BY id`, s.unix(), list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n, members int
		if err := rows.Scan(&id, &n, &members); err != nil {
			return nil, err
		}
		if l := boostLevel(n, members); l > 0 {
			out[id] = l
		}
	}
	return out, rows.Err()
}
