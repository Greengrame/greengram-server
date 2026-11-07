package stars

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// The collectible market (core.telegram.org/api/gifts#reselling-collectible-gifts).
//
// Two kinds of stock show on a collection's resale screen. Owners list their
// collectibles for a price (payments.updateStarGiftPrice); a buyer pays it,
// the seller gets it less the instance's commission and the commission goes
// to the gifts account. And a gift sold "as variants" has the instance's own
// offers: every combination of model, symbol and backdrop no collectible of
// the collection has yet. Offers aren't stored. A page works them out from
// the collection's attributes and what's minted, in an order fixed by the
// page's offset, so an offer's slug and id hold for as long as the buyer
// browses. Buying one mints it with the collection's next number; buying the
// same combination twice is the second buyer's SLUG_INVALID.
//
// An offer's price runs from the base (the most common combination) to base
// times the rarity multiplier (the rarest), by how unlikely the combination
// is to come out of an upgrade: -log of the three rarities multiplied.

var (
	// what Web A shows as "invoice not found": sold, taken off sale, minted
	errResaleGone     = tgerr.New(400, "SLUG_INVALID")
	errResalePrice    = tgerr.New(400, "STARGIFT_RESELL_PRICE_CHANGED")
	errResaleOwn      = tgerr.New(400, "STARGIFT_OWNER_INVALID")
	errResaleAmount   = tgerr.New(400, "STARGIFT_RESELL_AMOUNT_INVALID")
	errResaleCurrency = tgerr.New(400, "STARGIFT_RESELL_CURRENCY_NOT_ALLOWED")
	errResaleChannel  = tgerr.New(400, "STARGIFT_PEER_INVALID")
	// listed gifts stay put: no transfer, no emoji status
	errListed = tgerr.New(400, "STARGIFT_RESELL_LISTED")
)

// ErrResaleCurrency is a TON price: Stars only here.
var ErrResaleCurrency = errResaleCurrency

// ErrListed is wearing a listed collectible.
var ErrListed = errListed

const (
	// a listing's price; the floor leaves the seller at least a Star after
	// the largest commission
	ResaleMinStars = 10
	ResaleMaxStars = 1000000
	// what the panel offers when it hasn't set one
	DefaultRarityX100 = 300
	// MaxCommission is the most the instance may take, in permille
	MaxCommission = 900
)

// Market is how a catalog gift sells as variants.
type Market struct {
	Enabled bool
	// Base 0 is the gift's price plus its upgrade
	Base       int64
	RarityX100 int
}

func (s *Service) market(ctx context.Context, q querier, gift int64) (Market, error) {
	m := Market{RarityX100: DefaultRarityX100}
	var base *int64
	err := q.QueryRow(ctx, "SELECT enabled, base_stars, rarity_x100 FROM gift_market WHERE gift_id=$1", gift).Scan(&m.Enabled, &base, &m.RarityX100)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, nil
	}
	if base != nil {
		m.Base = *base
	}
	return m, err
}

// MarketOf is a catalog gift's market settings; a gift never set has the defaults.
func (s *Service) MarketOf(ctx context.Context, gift int64) (Market, error) {
	return s.market(ctx, s.db, gift)
}

var errMarketNoSet = &account.Error{Status: 400, Message: "Only a gift that upgrades into a collection sells as variants."}

// SetMarket saves a gift's market settings from the admin panel.
func (s *Service) SetMarket(ctx context.Context, gift int64, m Market) error {
	if m.Base < 0 || m.Base > ResaleMaxStars || m.RarityX100 < 100 || m.RarityX100 > 100000 {
		return &account.Error{Status: 400, Message: "A base price up to 1,000,000 Stars and a rarity multiplier from 1 to 1000."}
	}
	var set int64
	err := s.db.QueryRow(ctx, "SELECT coalesce(upgrade_set_id, 0) FROM gift_catalog WHERE id=$1", gift).Scan(&set)
	if errors.Is(err, pgx.ErrNoRows) {
		return errGiftNotFound
	}
	if err != nil {
		return err
	}
	if m.Enabled && set == 0 {
		return errMarketNoSet
	}
	var base *int64
	if m.Base > 0 {
		base = &m.Base
	}
	_, err = s.db.Exec(ctx, `INSERT INTO gift_market(gift_id, enabled, base_stars, rarity_x100) VALUES ($1, $2, $3, $4)
		ON CONFLICT (gift_id) DO UPDATE SET enabled=$2, base_stars=$3, rarity_x100=$4`, gift, m.Enabled, base, m.RarityX100)
	return err
}

// Commission is the instance's cut of a sale between users, in permille.
func (s *Service) Commission(ctx context.Context) (int, error) {
	return commission(ctx, s.db)
}

func commission(ctx context.Context, q querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT commission_permille FROM gift_market_config").Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func (s *Service) SetCommission(ctx context.Context, permille int) error {
	if permille < 0 || permille > MaxCommission {
		return &account.Error{Status: 400, Message: "The commission is 0 to 90%."}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO gift_market_config(id, commission_permille) VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET commission_permille=$1`, permille)
	return err
}

// split is a sale's price as the seller's part and the commission. Web A
// shows the seller the price times their share, rounded; the remainder is
// the fee.
func split(price int64, permille int) (net, fee int64) {
	net = (price*int64(1000-permille) + 500) / 1000
	return net, price - net
}

// offerSet is a collection as the market sees it at one moment.
type offerSet struct {
	gift        Gift
	set         int64
	slug, title string
	supply      int
	issued      int
	left        int // offers that may still be minted; -1 for no limit
	on          bool
	base        int64
	rarity      int

	models, patterns []Attribute
	backdrops        []Backdrop
	lm, lp, lb       []float64 // -ln(rarity) of each
	smin, smax       float64

	minted map[int]bool // combinations some collectible already has
	// how many minted combinations have each attribute
	mintedM, mintedP, mintedB []int
}

func neglog(permille int) float64 { return -math.Log(float64(max(permille, 1)) / 1000) }

func minmax(v []float64) (float64, float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range v {
		lo, hi = min(lo, x), max(hi, x)
	}
	return lo, hi
}

// loadOffers reads g's collection. Under the collection's lock (lockSet) it
// is what a payment may rely on.
func (s *Service) loadOffers(ctx context.Context, q interface {
	querier
	rowsQuerier
}, g Gift) (*offerSet, error) {
	o := &offerSet{gift: g, set: g.UpgradeSet}
	var waiting int
	err := q.QueryRow(ctx, `SELECT slug, title, supply, issued,
		(SELECT count(*) FROM gift_receipts WHERE gift_id=$2 AND upgrade_prepaid > 0 AND unique_id IS NULL AND NOT converted)
		FROM collectible_sets WHERE id=$1`, g.UpgradeSet, g.ID).Scan(&o.slug, &o.title, &o.supply, &o.issued, &waiting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errGiftInvalid
	}
	if err != nil {
		return nil, err
	}
	o.left = -1
	if o.supply > 0 {
		o.left = max(o.supply-o.issued-waiting, 0)
	}
	m, err := s.market(ctx, q, g.ID)
	if err != nil {
		return nil, err
	}
	o.on, o.rarity, o.base = m.Enabled && g.Enabled, m.RarityX100, m.Base
	if o.base == 0 {
		o.base = g.Stars + g.UpgradeStars
	}
	if o.models, o.patterns, o.backdrops, err = attributes(ctx, q, g.UpgradeSet); err != nil {
		return nil, err
	}
	if len(o.models) == 0 || len(o.patterns) == 0 || len(o.backdrops) == 0 {
		o.on = false
	}
	for _, a := range o.models {
		o.lm = append(o.lm, neglog(a.Permille))
	}
	for _, a := range o.patterns {
		o.lp = append(o.lp, neglog(a.Permille))
	}
	for _, b := range o.backdrops {
		o.lb = append(o.lb, neglog(b.Permille))
	}
	a1, b1 := minmax(o.lm)
	a2, b2 := minmax(o.lp)
	a3, b3 := minmax(o.lb)
	o.smin, o.smax = a1+a2+a3, b1+b2+b3

	index := func(names []string) map[string]int {
		out := make(map[string]int, len(names))
		for i, n := range names {
			out[n] = i
		}
		return out
	}
	var mn, pn, bn []string
	for _, a := range o.models {
		mn = append(mn, a.Name)
	}
	for _, a := range o.patterns {
		pn = append(pn, a.Name)
	}
	for _, b := range o.backdrops {
		bn = append(bn, b.Name)
	}
	mi, pi, bi := index(mn), index(pn), index(bn)
	o.minted = map[int]bool{}
	o.mintedM, o.mintedP, o.mintedB = make([]int, len(mn)), make([]int, len(pn)), make([]int, len(bn))
	rows, err := q.Query(ctx, "SELECT DISTINCT model, pattern, backdrop FROM unique_gifts WHERE set_id=$1", g.UpgradeSet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			return nil, err
		}
		x, ok1 := mi[a]
		y, ok2 := pi[b]
		z, ok3 := bi[c]
		if ok1 && ok2 && ok3 {
			o.minted[o.combo(x, y, z)] = true
			o.mintedM[x]++
			o.mintedP[y]++
			o.mintedB[z]++
		}
	}
	return o, rows.Err()
}

func (o *offerSet) combo(m, p, b int) int { return (m*len(o.patterns)+p)*len(o.backdrops) + b }

func (o *offerSet) split(idx int) (m, p, b int) {
	b = idx % len(o.backdrops)
	idx /= len(o.backdrops)
	return idx / len(o.patterns), idx % len(o.patterns), b
}

func (o *offerSet) total() int { return len(o.models) * len(o.patterns) * len(o.backdrops) }

// price of a combination: the base for the most common, base times the
// multiplier for the rarest, in between by log-rarity.
func (o *offerSet) price(idx int) int64 {
	m, p, b := o.split(idx)
	return o.priceOf(o.lm[m] + o.lp[p] + o.lb[b])
}

func (o *offerSet) priceOf(s float64) int64 {
	t := 0.0
	if o.smax > o.smin {
		t = (s - o.smin) / (o.smax - o.smin)
	}
	return o.base + int64(math.Round(float64(o.base)*float64(o.rarity-100)/100*t))
}

func (o *offerSet) check(m, p, b int) uint32 {
	return crc32.ChecksumIEEE([]byte(o.models[m].Name+"\x00"+o.patterns[p].Name+"\x00"+o.backdrops[b].Name)) & 0xffff
}

// An offer's slug is the collection's, "-x", then the combination and a
// check of its names in base 36: a collection imported again with other
// attributes doesn't sell a different gift under an old slug. Numbered
// collectibles are "-<digits>", so the two never meet.
func (o *offerSet) offerSlug(idx int) string {
	m, p, b := o.split(idx)
	return o.slug + "-x" + strconv.FormatUint(uint64(idx)<<16|uint64(o.check(m, p, b)), 36)
}

func parseOfferSlug(slug string) (set string, code uint64, ok bool) {
	i := strings.LastIndex(slug, "-x")
	if i <= 0 {
		return "", 0, false
	}
	code, err := strconv.ParseUint(slug[i+2:], 36, 64)
	return slug[:i], code, err == nil
}

func (o *offerSet) decode(code uint64) (int, bool) {
	idx := code >> 16
	if idx >= uint64(o.total()) {
		return 0, false
	}
	m, p, b := o.split(int(idx))
	return int(idx), uint64(o.check(m, p, b)) == code&0xffff
}

// offerID: high bits no random collectible id has together with the set and
// the combination, so a page's ids are the same on every load.
func (o *offerSet) offerID(idx int) int64 { return 1<<62 | (o.set&0xffffff)<<36 | int64(idx) }

// offer is the combination as a collectible nobody holds, priced.
func (o *offerSet) offer(idx int) Unique {
	m, p, b := o.split(idx)
	return Unique{ID: o.offerID(idx), SetID: o.set, GiftID: o.gift.ID, Title: o.title, Slug: o.offerSlug(idx), Supply: o.supply, Issued: o.issued,
		Model: o.models[m], Pattern: o.patterns[p], Backdrop: o.backdrops[b], PremiumOnly: o.gift.PremiumOnly, Resale: o.price(idx), Offer: true}
}

// available is how many offers there are among n unminted combinations.
func (o *offerSet) available(n int) int {
	if !o.on || n <= 0 {
		return 0
	}
	if o.left >= 0 {
		return min(n, o.left)
	}
	return n
}

// ResaleQuery is getResaleStarGifts: filters by model and symbol document
// and backdrop id, any of each kind.
type ResaleQuery struct {
	GiftID      int64
	SortByPrice bool
	SortByNum   bool
	Models      []int64
	Patterns    []int64
	Backdrops   []int
	Offset      string
	Limit       int
}

// ResalePage is one page of a collection's market and what its filters show.
type ResalePage struct {
	Gifts []Unique
	Count int
	Next  string

	Models    []Attribute
	Patterns  []Attribute
	Backdrops []Backdrop
	// for sale with each attribute, the whole collection's, by index into
	// the lists above
	ModelCount, PatternCount, BackdropCount []int
}

// keep is the indices a filter keeps: every one when it names none.
func keep[T any](list []T, want map[int64]bool, key func(T) int64) []int {
	var out []int
	for i, x := range list {
		if len(want) == 0 || want[key(x)] {
			out = append(out, i)
		}
	}
	return out
}

func setOf[T int64 | int](v []T) map[int64]bool {
	out := map[int64]bool{}
	for _, x := range v {
		out[int64(x)] = true
	}
	return out
}

// listings are a collection's collectibles on sale by people.
func (s *Service) listings(ctx context.Context, q rowsQuerier, set int64) ([]Unique, error) {
	return s.uniques(ctx, q, "u.set_id=$1 AND u.resale_stars IS NOT NULL AND r.owner_id IS NOT NULL AND NOT r.converted", set)
}

// ResaleGifts is a page of a collection's market: listings and offers
// together, newest listings first then offers in an order of the page's own
// (by date), by number with offers after (by num), or cheapest first.
func (s *Service) ResaleGifts(ctx context.Context, q ResaleQuery) (ResalePage, error) {
	var out ResalePage
	g, err := s.gift(ctx, s.db, q.GiftID)
	if err != nil {
		return out, err
	}
	if g.UpgradeSet == 0 {
		return out, errGiftInvalid
	}
	o, err := s.loadOffers(ctx, s.db, g)
	if err != nil {
		return out, err
	}
	all, err := s.listings(ctx, s.db, o.set)
	if err != nil {
		return out, err
	}
	out.Models, out.Patterns, out.Backdrops = o.models, o.patterns, o.backdrops

	// counters: the whole collection, so a menu keeps every choice
	out.ModelCount, out.PatternCount, out.BackdropCount = make([]int, len(o.models)), make([]int, len(o.patterns)), make([]int, len(o.backdrops))
	np, nb := len(o.patterns), len(o.backdrops)
	for i := range o.models {
		out.ModelCount[i] = o.available(np*nb - o.mintedM[i])
	}
	for i := range o.patterns {
		out.PatternCount[i] = o.available(len(o.models)*nb - o.mintedP[i])
	}
	for i := range o.backdrops {
		out.BackdropCount[i] = o.available(len(o.models)*np - o.mintedB[i])
	}
	pos := func(names []string, n string) int {
		for i, x := range names {
			if x == n {
				return i
			}
		}
		return -1
	}
	names := func(n int, name func(int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = name(i)
		}
		return out
	}
	mn := names(len(o.models), func(i int) string { return o.models[i].Name })
	pn := names(len(o.patterns), func(i int) string { return o.patterns[i].Name })
	bn := names(len(o.backdrops), func(i int) string { return o.backdrops[i].Name })
	for _, u := range all {
		if i := pos(mn, u.Model.Name); i >= 0 {
			out.ModelCount[i]++
		}
		if i := pos(pn, u.Pattern.Name); i >= 0 {
			out.PatternCount[i]++
		}
		if i := pos(bn, u.Backdrop.Name); i >= 0 {
			out.BackdropCount[i]++
		}
	}

	wm, wp, wb := setOf(q.Models), setOf(q.Patterns), setOf(q.Backdrops)
	fm := keep(o.models, wm, func(a Attribute) int64 { return a.Document })
	fp := keep(o.patterns, wp, func(a Attribute) int64 { return a.Document })
	fb := keep(o.backdrops, wb, func(b Backdrop) int64 { return int64(b.ID) })
	var list []Unique
	for _, u := range all {
		if (len(wm) == 0 || wm[u.Model.Document]) && (len(wp) == 0 || wp[u.Pattern.Document]) && (len(wb) == 0 || wb[int64(u.Backdrop.ID)]) {
			list = append(list, u)
		}
	}
	// offers within the filter: all its combinations less the minted ones
	n := len(fm) * len(fp) * len(fb)
	if n > 0 && len(o.minted) > 0 {
		in := func(f []int) map[int]bool {
			out := make(map[int]bool, len(f))
			for _, i := range f {
				out[i] = true
			}
			return out
		}
		im, ip, ib := in(fm), in(fp), in(fb)
		for idx := range o.minted {
			m, p, b := o.split(idx)
			if im[m] && ip[p] && ib[b] {
				n--
			}
		}
	}
	offers := o.available(n)
	out.Count = len(list) + offers

	mode := "d"
	switch {
	case q.SortByPrice:
		mode = "p"
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Resale != list[j].Resale {
				return list[i].Resale < list[j].Resale
			}
			return list[i].Num < list[j].Num
		})
	case q.SortByNum:
		mode = "n"
		sort.SliceStable(list, func(i, j int) bool { return list[i].Num < list[j].Num })
	default:
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].ResaleSince != list[j].ResaleSince {
				return list[i].ResaleSince > list[j].ResaleSince
			}
			return list[i].ID > list[j].ID
		})
	}
	cur, err := parseCursor(q.Offset, mode)
	if err != nil {
		return out, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var it comboIter
	if mode == "p" {
		it = newPriceIter(o, fm, fp, fb, cur.pos)
	} else {
		it = newShuffleIter(o, fm, fp, fb, cur.seed, cur.pos)
	}
	// the next offer, skipping minted combinations, until there are no more
	var peeked *Unique
	nextOffer := func() *Unique {
		if peeked != nil {
			return peeked
		}
		for cur.rank < offers {
			idx, ok := it.next()
			if !ok {
				return nil
			}
			cur.pos++
			if o.minted[idx] {
				continue
			}
			u := o.offer(idx)
			peeked = &u
			return peeked
		}
		return nil
	}
	for len(out.Gifts) < limit {
		var l *Unique
		if cur.li < len(list) {
			l = &list[cur.li]
		}
		p := nextOffer()
		switch {
		case l == nil && p == nil:
		case p == nil || l != nil && (mode != "p" || l.Resale <= p.Resale):
			out.Gifts = append(out.Gifts, *l)
			cur.li++
			continue
		default:
			out.Gifts = append(out.Gifts, *p)
			peeked = nil
			cur.rank++
			continue
		}
		break
	}
	if cur.li < len(list) || nextOffer() != nil {
		// a peeked offer is read again on the next page
		if peeked != nil {
			cur.pos--
		}
		out.Next = cur.String()
	}
	return out, nil
}

// cursor is where a page stopped: the listings shown, the position in the
// offers' order and how many offers were shown, under the page's seed.
type cursor struct {
	mode          string
	seed          uint64
	li, pos, rank int
}

func (c cursor) String() string {
	return fmt.Sprintf("%s.%x.%d.%d.%d", c.mode, c.seed, c.li, c.pos, c.rank)
}

var errOffset = tgerr.New(400, "OFFSET_INVALID")

func parseCursor(s, mode string) (cursor, error) {
	if s == "" {
		var b [8]byte
		rand.Read(b[:])
		return cursor{mode: mode, seed: binary.LittleEndian.Uint64(b[:])}, nil
	}
	f := strings.Split(s, ".")
	if len(f) != 5 || f[0] != mode {
		return cursor{}, errOffset
	}
	seed, err1 := strconv.ParseUint(f[1], 16, 64)
	li, err2 := strconv.Atoi(f[2])
	pos, err3 := strconv.Atoi(f[3])
	rank, err4 := strconv.Atoi(f[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || li < 0 || pos < 0 || rank < 0 {
		return cursor{}, errOffset
	}
	return cursor{mode: mode, seed: seed, li: li, pos: pos, rank: rank}, nil
}

// comboIter walks a filter's combinations, minted ones too, in some order.
type comboIter interface {
	next() (int, bool)
}

// shuffleIter is the filter's combinations in an order the seed picks: a
// Feistel permutation over the next power of four, walking past what's out
// of range. Position n is reached without walking the ones before it.
type shuffleIter struct {
	o          *offerSet
	fm, fp, fb []int
	n, pos     int
	half       uint
	seed       uint64
}

func newShuffleIter(o *offerSet, fm, fp, fb []int, seed uint64, pos int) *shuffleIter {
	n := len(fm) * len(fp) * len(fb)
	k := uint(bits.Len(uint(max(n-1, 1))))
	k += k & 1
	return &shuffleIter{o: o, fm: fm, fp: fp, fb: fb, n: n, pos: pos, half: k / 2, seed: seed}
}

func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

func (s *shuffleIter) permute(x uint64) uint64 {
	mask := uint64(1)<<s.half - 1
	for {
		l, r := x>>s.half, x&mask
		for round := uint64(0); round < 4; round++ {
			l, r = r, l^(mix(s.seed^round<<56^r)&mask)
		}
		x = l<<s.half | r
		if x < uint64(s.n) {
			return x
		}
	}
}

func (s *shuffleIter) next() (int, bool) {
	if s.pos >= s.n {
		return 0, false
	}
	x := int(s.permute(uint64(s.pos)))
	s.pos++
	b := x % len(s.fb)
	x /= len(s.fb)
	return s.o.combo(s.fm[x/len(s.fp)], s.fp[x%len(s.fp)], s.fb[b]), true
}

// priceIter is the filter's combinations cheapest first: the attribute lists
// sorted by rarity and a best-first walk over sums of the three, which only
// ever holds the frontier. Ties go by position in the sorted lists, so the
// order is the same every time and a later page skips what earlier ones
// showed.
type priceIter struct {
	o          *offerSet
	fm, fp, fb []int // sorted by -log rarity
	h          comboHeap
	seen       map[[3]int]bool
}

func newPriceIter(o *offerSet, fm, fp, fb []int, skip int) *priceIter {
	byCost := func(f []int, l []float64) []int {
		out := append([]int(nil), f...)
		sort.SliceStable(out, func(i, j int) bool { return l[out[i]] < l[out[j]] })
		return out
	}
	p := &priceIter{o: o, fm: byCost(fm, o.lm), fp: byCost(fp, o.lp), fb: byCost(fb, o.lb), seen: map[[3]int]bool{}}
	if len(fm) > 0 && len(fp) > 0 && len(fb) > 0 {
		p.push([3]int{})
	}
	for i := 0; i < skip; i++ {
		if _, ok := p.next(); !ok {
			break
		}
	}
	return p
}

func (p *priceIter) push(at [3]int) {
	if at[0] >= len(p.fm) || at[1] >= len(p.fp) || at[2] >= len(p.fb) || p.seen[at] {
		return
	}
	p.seen[at] = true
	heap.Push(&p.h, comboAt{at: at, s: p.o.lm[p.fm[at[0]]] + p.o.lp[p.fp[at[1]]] + p.o.lb[p.fb[at[2]]]})
}

func (p *priceIter) next() (int, bool) {
	if p.h.Len() == 0 {
		return 0, false
	}
	c := heap.Pop(&p.h).(comboAt)
	delete(p.seen, c.at) // what's popped can't come back: its successors only grow
	for d := 0; d < 3; d++ {
		at := c.at
		at[d]++
		p.push(at)
	}
	return p.o.combo(p.fm[c.at[0]], p.fp[c.at[1]], p.fb[c.at[2]]), true
}

type comboAt struct {
	at [3]int
	s  float64
}

type comboHeap []comboAt

func (h comboHeap) Len() int { return len(h) }
func (h comboHeap) Less(i, j int) bool {
	if h[i].s != h[j].s {
		return h[i].s < h[j].s
	}
	a, b := h[i].at, h[j].at
	for d := 0; d < 3; d++ {
		if a[d] != b[d] {
			return a[d] < b[d]
		}
	}
	return false
}
func (h comboHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *comboHeap) Push(x any)   { *h = append(*h, x.(comboAt)) }
func (h *comboHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// cheapest is the lowest offer price, 0 when there's nothing to offer.
func (o *offerSet) cheapest() int64 {
	if o.available(o.total()-len(o.minted)) == 0 {
		return 0
	}
	all := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	it := newPriceIter(o, all(len(o.models)), all(len(o.patterns)), all(len(o.backdrops)), 0)
	for {
		idx, ok := it.next()
		if !ok {
			return 0
		}
		if !o.minted[idx] {
			return o.price(idx)
		}
	}
}

// Tile is what the shop shows of a gift's market: how many are for sale
// and from what price. Variants says the gift sells only that way.
type Tile struct {
	Count    int64
	MinStars int64
	Variants bool
}

// Tiles are the market of every catalog gift that upgrades into a
// collection with something on sale.
func (s *Service) Tiles(ctx context.Context, list []Gift) (map[int64]Tile, error) {
	out := map[int64]Tile{}
	type listed struct {
		n   int64
		min int64
	}
	rows, err := s.db.Query(ctx, `SELECT u.set_id, count(*), min(u.resale_stars) FROM unique_gifts u
		JOIN gift_receipts r ON r.unique_id = u.id AND r.owner_id IS NOT NULL AND NOT r.converted
		WHERE u.resale_stars IS NOT NULL GROUP BY u.set_id`)
	if err != nil {
		return nil, err
	}
	bySet := map[int64]listed{}
	for rows.Next() {
		var set int64
		var l listed
		if err := rows.Scan(&set, &l.n, &l.min); err != nil {
			rows.Close()
			return nil, err
		}
		bySet[set] = l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	on := map[int64]bool{}
	rows, err = s.db.Query(ctx, "SELECT gift_id FROM gift_market WHERE enabled")
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		on[id] = true
	}
	for _, g := range list {
		if g.UpgradeSet == 0 {
			continue
		}
		t := Tile{Count: bySet[g.UpgradeSet].n, MinStars: bySet[g.UpgradeSet].min}
		if on[g.ID] {
			t.Variants = true
			o, err := s.loadOffers(ctx, s.db, g)
			if err != nil {
				return nil, err
			}
			if n := o.available(o.total() - len(o.minted)); n > 0 {
				t.Count += int64(n)
				if c := o.cheapest(); t.MinStars == 0 || c < t.MinStars {
					t.MinStars = c
				}
			}
		}
		if t.Count > 0 || t.Variants {
			out[g.ID] = t
		}
	}
	return out, nil
}

// SellsAsVariants says a catalog gift is only sold through its market.
func (s *Service) SellsAsVariants(ctx context.Context, gift int64) (bool, error) {
	return sellsAsVariants(ctx, s.db, gift)
}

func sellsAsVariants(ctx context.Context, q querier, gift int64) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gift_market m JOIN gift_catalog g ON g.id = m.gift_id
		WHERE m.gift_id=$1 AND m.enabled AND g.upgrade_set_id IS NOT NULL)`, gift).Scan(&on)
	return on, err
}

// ResaleItem finds a collectible on sale by slug: a listing, or one of the
// instance's offers. Anything else is SLUG_INVALID.
func (s *Service) ResaleItem(ctx context.Context, slug string) (Unique, error) {
	u, err := s.UniqueBySlug(ctx, slug)
	if err == nil {
		if u.Resale == 0 || u.Holder.User == 0 {
			return u, errResaleGone
		}
		return u, nil
	}
	if !errors.Is(err, errUniqueNotFound) {
		return u, err
	}
	o, idx, err := s.findOffer(ctx, s.db, slug)
	if err != nil {
		return Unique{}, err
	}
	return o.offer(idx), nil
}

// findOffer resolves an offer's slug to its combination, still on offer.
func (s *Service) findOffer(ctx context.Context, q interface {
	querier
	rowsQuerier
}, slug string) (*offerSet, int, error) {
	setSlug, code, ok := parseOfferSlug(slug)
	if !ok {
		return nil, 0, errResaleGone
	}
	var gift int64
	err := q.QueryRow(ctx, "SELECT g.id FROM collectible_sets s JOIN gift_catalog g ON g.upgrade_set_id = s.id WHERE lower(s.slug)=lower($1)", setSlug).Scan(&gift)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, errResaleGone
	}
	if err != nil {
		return nil, 0, err
	}
	g, err := s.gift(ctx, q, gift)
	if err != nil {
		return nil, 0, err
	}
	o, err := s.loadOffers(ctx, q, g)
	if err != nil {
		return nil, 0, err
	}
	idx, ok := o.decode(code)
	if !ok || o.minted[idx] || o.available(1) == 0 {
		return nil, 0, errResaleGone
	}
	return o, idx, nil
}

// ResaleForm prices buying a collectible off the market for to: a person
// (the buyer too) or a broadcast channel; the caller checked to takes it.
func (s *Service) ResaleForm(ctx context.Context, buyer int64, to Holder, slug, message string, hideName bool) (Form, Unique, error) {
	if utf8.RuneCountInString(message) > MaxGiftMessage {
		return Form{}, Unique{}, errGiftMessage
	}
	u, err := s.ResaleItem(ctx, slug)
	if err != nil {
		return Form{}, u, err
	}
	if u.Holder.User == buyer {
		return Form{}, u, errResaleOwn
	}
	// bought for yourself there's nobody to hide from: the profile says you
	if to == (Holder{User: buyer}) {
		message, hideName = "", false
	}
	f := Form{Kind: KindGiftResale, User: buyer, Recipient: to.User, Channel: to.Channel, GiftID: u.GiftID, Stars: u.Resale, Slug: u.Slug,
		UniqueID: u.ID, Seller: u.Holder.User, Message: message, HideName: hideName}
	if u.Offer {
		f.UniqueID = 0
	}
	if to.Channel != 0 {
		if err := giftChannel(ctx, s.db, to.Channel); err != nil {
			return Form{}, u, err
		}
		f.Recipient = buyer
	} else if _, err := person(ctx, s.db, to.User); err != nil {
		return Form{}, u, err
	}
	f, err = s.issue(ctx, f)
	return f, u, err
}

// ResaleResult is a collectible bought off the market.
type ResaleResult struct {
	Form    Form
	To      Holder
	Receipt Receipt
	Unique  Unique
	Balance int64
	// a listing: who sold it, what they got and what the instance kept; an
	// offer has no seller and all of the price is the instance's
	Seller   int64
	Net, Fee int64
	// the seller wore it, which is over now
	StatusCleared bool
}

// PayResale is sendStarsForm for a resale form: one transaction that moves
// the Stars and the gift, or nothing.
func (s *Service) PayResale(ctx context.Context, buyer, formID int64) (ResaleResult, error) {
	var out ResaleResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindGiftResale)
		if err != nil {
			return err
		}
		out.Form = f
		out.To = Holder{User: f.Recipient}
		if f.Channel != 0 {
			out.To = Holder{Channel: f.Channel}
			if err := giftChannel(ctx, tx, f.Channel); err != nil {
				return err
			}
		} else if _, err := person(ctx, tx, f.Recipient); err != nil {
			return err
		}
		if f.UniqueID != 0 {
			return s.buyListing(ctx, tx, f, &out)
		}
		return s.buyOffer(ctx, tx, f, &out)
	})
	return out, err
}

func (s *Service) buyListing(ctx context.Context, tx pgx.Tx, f Form, out *ResaleResult) error {
	var receipt int64
	err := tx.QueryRow(ctx, "SELECT id FROM gift_receipts WHERE unique_id=$1 AND NOT converted", f.UniqueID).Scan(&receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errResaleGone
	}
	if err != nil {
		return err
	}
	r, err := heldForUpdate(ctx, tx, receipt)
	if err != nil {
		return errResaleGone
	}
	var price *int64
	if err := tx.QueryRow(ctx, "SELECT resale_stars FROM unique_gifts WHERE id=$1 FOR UPDATE", f.UniqueID).Scan(&price); err != nil {
		return err
	}
	switch {
	case r.UniqueID != f.UniqueID || r.Owner == 0 || r.Owner != f.Seller || price == nil:
		return errResaleGone
	case r.Owner == f.User:
		return errResaleOwn
	case *price != f.Stars:
		return errResalePrice
	}
	permille, err := commission(ctx, tx)
	if err != nil {
		return err
	}
	net, fee := split(f.Stars, permille)
	sys, err := Account(ctx, tx, GiftsAccount)
	if err != nil {
		return err
	}
	if err := Lock(ctx, tx, f.User, r.Owner, sys); err != nil {
		return err
	}
	now := s.unix()
	ref := fmt.Sprintf("gift-resale:%d", f.ID)
	title := r.Gift.Title
	if _, err := Post(ctx, tx, Entry{User: f.User, Amount: -f.Stars, Reason: "gift_resale", Reference: ref, CreatedBy: f.User, Peer: r.Owner, Title: title, GiftID: r.Gift.ID}, now); err != nil {
		return err
	}
	if _, err := Post(ctx, tx, Entry{User: r.Owner, Amount: net, Reason: "gift_resale", Reference: ref + ":seller", CreatedBy: f.User, Peer: f.User, Title: title, GiftID: r.Gift.ID}, now); err != nil {
		return err
	}
	if fee > 0 {
		if _, err := Post(ctx, tx, Entry{User: sys, Amount: fee, Reason: "gift_resale_fee", Reference: ref + ":fee", CreatedBy: f.User, Peer: f.User, Title: title, GiftID: r.Gift.ID}, now); err != nil {
			return err
		}
	}
	var owner, channel *int64
	if out.To.Channel != 0 {
		channel = &out.To.Channel
	} else {
		owner = &out.To.User
	}
	if out.To == (Holder{User: f.User}) {
		// bought for myself: nobody gave it to me, so it still names who
		// first gave it (its original details) or who handed it on last
		_, err = tx.Exec(ctx, `UPDATE gift_receipts r SET owner_id=$2, owner_channel_id=NULL, displayed=true, message_id=NULL, created_at=$3, pinned=NULL, body='',
			sender_id=coalesce(u.original_sender_id, r.sender_id), hide_name=u.original_sender_id IS NULL AND r.hide_name
			FROM unique_gifts u WHERE r.id=$1 AND u.id=r.unique_id`, r.ID, f.User, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE gift_receipts SET owner_id=$2, owner_channel_id=$3, sender_id=$4, hide_name=$5, body=$6, displayed=true,
			message_id=NULL, created_at=$7, pinned=NULL WHERE id=$1`, r.ID, owner, channel, f.User, f.HideName, f.Message, now)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE unique_gifts SET resale_stars=NULL, resale_since=0 WHERE id=$1", f.UniqueID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE users SET emoji_status_collectible=NULL WHERE id=$1 AND emoji_status_collectible=$2", r.Owner, f.UniqueID)
	if err != nil {
		return err
	}
	out.StatusCleared = tag.RowsAffected() > 0
	out.Seller, out.Net, out.Fee = r.Owner, net, fee
	return s.resold(ctx, tx, f, r.ID, out)
}

func (s *Service) buyOffer(ctx context.Context, tx pgx.Tx, f Form, out *ResaleResult) error {
	g, err := s.gift(ctx, tx, f.GiftID)
	if err != nil || g.UpgradeSet == 0 {
		return errResaleGone
	}
	// under the collection's lock: numbers in order, one buyer per combination
	if g, err = s.lockSet(ctx, tx, g); err != nil {
		return err
	}
	o, idx, err := s.findOffer(ctx, tx, f.Slug)
	if err != nil {
		return err
	}
	if o.gift.ID != g.ID {
		return errResaleGone
	}
	if o.price(idx) != f.Stars {
		return errResalePrice
	}
	sys, err := Account(ctx, tx, GiftsAccount)
	if err != nil {
		return err
	}
	if err := Lock(ctx, tx, f.User, sys); err != nil {
		return err
	}
	now := s.unix()
	if err := move(ctx, tx, f.User, sys, f.Stars, "gift_market", fmt.Sprintf("gift-market:%d", f.ID), g, now); err != nil {
		return err
	}
	var num int
	var slug string
	err = tx.QueryRow(ctx, "UPDATE collectible_sets SET issued = issued + 1 WHERE id=$1 AND (supply = 0 OR issued < supply) RETURNING issued, slug", g.UpgradeSet).Scan(&num, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return errResaleGone
	}
	if err != nil {
		return err
	}
	m, p, b := o.split(idx)
	// bought for someone else, it keeps who gave it and the note, like a gift
	var sender, recipient, channel *int64
	original := out.To != Holder{User: f.User}
	if original {
		if !f.HideName {
			sender = &f.User
		}
		if out.To.Channel != 0 {
			channel = &out.To.Channel
		} else {
			recipient = &out.To.User
		}
	}
	id := randomID()
	msg := ""
	if original {
		msg = f.Message
	}
	if _, err := tx.Exec(ctx, `INSERT INTO unique_gifts(id, set_id, gift_id, num, slug, model, pattern, backdrop, original,
			original_sender_id, original_recipient_id, original_recipient_channel_id, original_message, original_date, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)`,
		id, g.UpgradeSet, g.ID, num, slug+"-"+strconv.Itoa(num), o.models[m].Name, o.patterns[p].Name, o.backdrops[b].Name, original,
		sender, recipient, channel, msg, now); err != nil {
		return err
	}
	var owner *int64
	if out.To.Channel == 0 {
		owner = &out.To.User
	}
	// hiding my name from myself hides nothing: the sheet says it's from me
	hide := f.HideName && original
	var receipt int64
	if err := tx.QueryRow(ctx, `INSERT INTO gift_receipts(gift_id, sender_id, owner_id, owner_channel_id, cost, convert_stars, body, hide_name, created_at, unique_id)
		VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8, $9) RETURNING id`, g.ID, f.User, owner, channel, f.Stars, f.Message, hide, now, id).Scan(&receipt); err != nil {
		return err
	}
	f.UniqueID = id
	out.Fee = f.Stars
	return s.resold(ctx, tx, f, receipt, out)
}

// resold reads back what a purchase left: the receipt, the collectible and
// the buyer's balance.
func (s *Service) resold(ctx context.Context, tx pgx.Tx, f Form, receipt int64, out *ResaleResult) error {
	r, err := scanReceipt(tx.QueryRow(ctx, "SELECT "+receiptCols+receiptFrom+" WHERE r.id=$1", receipt))
	if err != nil {
		return err
	}
	list, err := s.uniques(ctx, tx, "u.id = $1", f.UniqueID)
	if err != nil {
		return err
	}
	if len(list) != 1 {
		return errors.New("stars: resold gift vanished")
	}
	out.Unique = list[0]
	r.Unique = &out.Unique
	out.Receipt = r
	out.Balance, err = Balance(ctx, tx, f.User)
	return err
}

// Listed is a collectible put on sale or taken off.
type Listed struct {
	Receipt Receipt
	Unique  Unique
	// its owner wore it; listing took it off
	StatusCleared bool
}

// SetResalePrice is updateStarGiftPrice: a price puts owner's collectible
// on sale or changes it, 0 takes it off. Channels have no Stars balance to
// sell into.
func (s *Service) SetResalePrice(ctx context.Context, owner Holder, receipt, price int64) (Listed, error) {
	var out Listed
	if price != 0 && (price < ResaleMinStars || price > ResaleMaxStars) {
		return out, errResaleAmount
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := heldForUpdate(ctx, tx, receipt)
		switch {
		case err != nil:
			return err
		case r.Holder() != owner:
			return errSavedGift
		case r.UniqueID == 0:
			return errNotUnique
		case owner.Channel != 0:
			return errResaleChannel
		}
		var converted bool
		if err := tx.QueryRow(ctx, "SELECT converted FROM gift_receipts WHERE id=$1", r.ID).Scan(&converted); err != nil || converted {
			return errSavedGift
		}
		var stars *int64
		var since *int
		if price > 0 {
			now := s.unix()
			stars, since = &price, &now
		}
		if _, err := tx.Exec(ctx, "UPDATE unique_gifts SET resale_stars=$2, resale_since=coalesce($3, 0) WHERE id=$1", r.UniqueID, stars, since); err != nil {
			return err
		}
		if price > 0 {
			tag, err := tx.Exec(ctx, "UPDATE users SET emoji_status_collectible=NULL WHERE id=$1 AND emoji_status_collectible=$2", owner.User, r.UniqueID)
			if err != nil {
				return err
			}
			out.StatusCleared = tag.RowsAffected() > 0
		}
		list, err := s.uniques(ctx, tx, "u.id = $1", r.UniqueID)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			return errSavedGift
		}
		out.Unique = list[0]
		r.Unique = &out.Unique
		out.Receipt = r
		return nil
	})
	return out, err
}

// MarketCollection is a collection's market numbers for the admin panel.
type MarketCollection struct {
	Listed   int
	Offers   int // -1: the market is off
	MinStars int64
	MaxStars int64 // the rarest combination's price
}

// MarketStats is what the panel shows next to a gift's market settings.
func (s *Service) MarketStats(ctx context.Context, gift int64) (MarketCollection, error) {
	out := MarketCollection{Offers: -1}
	g, err := s.gift(ctx, s.db, gift)
	if err != nil {
		return out, err
	}
	if g.UpgradeSet == 0 {
		return out, nil
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM unique_gifts u JOIN gift_receipts r ON r.unique_id = u.id AND r.owner_id IS NOT NULL AND NOT r.converted
		WHERE u.set_id=$1 AND u.resale_stars IS NOT NULL`, g.UpgradeSet).Scan(&out.Listed); err != nil {
		return out, err
	}
	o, err := s.loadOffers(ctx, s.db, g)
	if err != nil {
		return out, err
	}
	if o.total() > 0 {
		out.MinStars, out.MaxStars = o.priceOf(o.smin), o.priceOf(o.smax)
	}
	if o.on {
		out.Offers = o.available(o.total() - len(o.minted))
	}
	return out, nil
}
