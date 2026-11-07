import { useEffect, useRef, useState } from 'react';
import { Badge, Button, Input, Loader, Select, Switch, Table } from '@cloudflare/kumo';
import { CaretLeftIcon, CaretRightIcon, GiftIcon, ImageIcon, PlusIcon, SmileyIcon, TrashIcon, XIcon } from '@phosphor-icons/react';
import { accountSlot, api } from '../lib/api.js';
import { matches, number, paged } from '../lib/format.js';
import { toast, toastError, useLive } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Loading, Pager, Panel, Status, Toolbar } from '../components/ui.jsx';
import { packPage, packQuery } from './packs.jsx';

export function filterGifts(gifts, query, filter) {
    return gifts.filter(gift => matches(query, gift.title, gift.emoji) && (filter === 'all' || gift.enabled === (filter === 'enabled')));
}

// a blank stock means unlimited, which the server takes as null
export function giftBody(fields) {
    return {
        title: fields.title.trim(),
        emoji: fields.emoji.trim(),
        stickerId: (fields.stickerId || '').trim() || null,
        stars: Number(fields.stars),
        convertStars: Number(fields.convertStars || 0),
        stock: fields.stock === '' ? null : Number(fields.stock),
        premiumOnly: fields.premiumOnly,
        enabled: fields.enabled,
        upgrade: upgradeBody(fields),
        market: marketBody(fields)
    };
}

// selling as variants needs a collection, so a plain gift sends nothing. A
// blank base price is the gift's price plus its upgrade (null to the server).
export function marketBody(fields) {
    if (!fields.upgradable) return undefined;
    return {
        enabled: Boolean(fields.variants),
        baseStars: (fields.baseStars || '').trim() === '' ? null : Number(fields.baseStars),
        rarityMultiplier: Number(fields.rarityMultiplier || 1)
    };
}

export function marketFields(gift) {
    let market = gift?.market;
    return {
        variants: Boolean(market?.enabled),
        baseStars: market?.baseStars ? String(market.baseStars) : '',
        rarityMultiplier: String(market?.rarityMultiplier ?? 3)
    };
}

// what the table and the form say about a gift's market
export function marketText(market) {
    if (!market) return '—';
    let parts = [];
    if (market.enabled) parts.push(market.offers > 0 ? `${number(market.offers)} variants` : 'Variants sold out');
    if (market.listed) parts.push(`${number(market.listed)} listed`);
    return parts.length ? parts.join(' · ') : '—';
}

// the commission is kept in permille; the panel edits a percent
export function commissionPercent(permille) {
    return String(permille / 10);
}

export function commissionBody(percent) {
    return { commissionPermille: Math.round(Number(percent) * 10) };
}

// null makes the gift plain again; the supply is the collection's, shared by
// whatever upgrades into it (one gift at a time). A blank supply is
// unlimited, which the server takes as null.
export function upgradeBody(fields) {
    if (!fields.upgradable) return null;
    return {
        setId: Number(fields.setId),
        stars: Number(fields.upgradeStars),
        transferStars: Number(fields.transferStars || 0),
        supply: fields.supply.trim() === '' ? null : Number(fields.supply)
    };
}

// the form's fields for a gift's upgrade: an upgradable gift's supply as it
// is (blank when unlimited), a plain gift's limited stock as one to start with
export function upgradeFields(gift) {
    let up = gift?.upgrade;
    let supply = up ? (up.supply ? String(up.supply) : '') : gift?.stockTotal ? String(gift.stockTotal) : '';
    return {
        upgradable: Boolean(up),
        setId: up ? String(up.setId) : '',
        upgradeStars: up ? String(up.stars) : '',
        transferStars: up ? String(up.transferStars) : '25',
        supply
    };
}

// a collection's supply of 0 is unlimited
export function supplyText(issued, supply) {
    return supply ? `${number(issued)} of ${number(supply)} issued` : `${number(issued)} issued, unlimited`;
}

// a collection another gift upgrades into can't be picked
export function collectionItems(collections, giftId) {
    return Object.fromEntries(collections.map(set => [String(set.id), {
        label: `${set.title} · ${number(set.models)} models · ${supplyText(set.issued, set.supply)}`,
        disabled: Boolean(set.giftId) && set.giftId !== giftId
    }]));
}

// a picked custom emoji only fills the emoji if none was typed. The id stays a
// string: document ids don't fit a JS number.
export function pickSticker(fields, item) {
    return { ...fields, stickerId: item.id, emoji: fields.emoji.trim() ? fields.emoji : item.emoji };
}

// the kind comes from the file: a saved gift's id is all the form knows about it.
// TGS is gzipped Lottie, drawn on one frame, since a whole pack playing would
// eat the page. Fetched once on screen, packs run to hundreds.
function StickerPicture({ id, alt = '', size = 40 }) {
    let box = useRef(null);
    let [picture, setPicture] = useState(null);
    useEffect(() => {
        let alive = true;
        let url;
        let animation;
        let draw = async () => {
            let response = await fetch(`/api/sticker-media/${id}?account=${accountSlot()}`);
            if (!response.ok) throw new Error(response.statusText);
            let blob = await response.blob();
            if (!alive) return;
            if (blob.type !== 'application/x-tgsticker') {
                url = URL.createObjectURL(blob);
                setPicture({ url, video: blob.type === 'video/webm' });
                return;
            }
            let data = await new Response(blob.stream().pipeThrough(new DecompressionStream('gzip'))).json();
            let { default: lottie } = await import('lottie-web/build/player/lottie_light.js');
            if (!alive) return;
            animation = lottie.loadAnimation({ container: box.current, renderer: 'svg', loop: false, autoplay: false, animationData: data });
            animation.goToAndStop(Math.floor(animation.totalFrames / 2), true);
        };
        let watcher = new IntersectionObserver(entries => {
            if (!entries.some(entry => entry.isIntersecting)) return;
            watcher.disconnect();
            draw().catch(() => { if (alive) setPicture({ failed: true }); });
        });
        watcher.observe(box.current);
        return () => {
            alive = false;
            watcher.disconnect();
            animation?.destroy();
            if (url) URL.revokeObjectURL(url);
        };
    }, [id]);
    return <span ref={box} className="inline-flex shrink-0 items-center justify-center overflow-hidden text-lg" style={{ width: size, height: size }}>
        {picture?.url && !picture.video ? <img className="size-full object-contain" src={picture.url} alt={alt} /> : null}
        {picture?.url && picture.video ? <video className="size-full object-contain" src={picture.url} muted playsInline preload="metadata" /> : null}
        {picture?.failed ? alt : null}
    </span>;
}

function PackItems({ pack, onPick, onBack }) {
    let [state, setState] = useState({});
    useEffect(() => {
        let alive = true;
        api(`/admin/sticker-packs/${pack.id}/items`).then(data => { if (alive) setState({ items: data.items }); }, failure => { if (alive) setState({ error: failure.message }); });
        return () => { alive = false; };
    }, [pack.id]);
    return <>
        <div className="flex items-center gap-2">
            <Button type="button" size="sm" variant="ghost" icon={CaretLeftIcon} onClick={onBack}>Packs</Button>
            <span className="min-w-0 truncate text-sm font-medium text-kumo-default">{pack.title}</span>
        </div>
        {state.error ? <p className="text-sm text-kumo-danger">{state.error}</p> : null}
        {!state.items && !state.error ? <Loader /> : null}
        {state.items?.length === 0 ? <p className="text-sm text-kumo-subtle">This pack is empty.</p> : null}
        {state.items?.length ? <div className="grid max-h-64 grid-cols-[repeat(auto-fill,minmax(52px,1fr))] gap-1 overflow-y-auto">
            {state.items.map(item => <button key={item.id} type="button" title={item.emoji} aria-label={`${item.emoji || 'Custom emoji'} ${item.id}`} className="flex items-center justify-center rounded-md p-1.5 hover:bg-kumo-recessed focus-visible:ring focus-visible:ring-kumo-line" onClick={() => onPick(item)}>
                <StickerPicture id={item.id} alt={item.emoji} />
            </button>)}
        </div> : null}
    </>;
}

function EmojiPicker({ onPick }) {
    let [query, setQuery] = useState('');
    let [page, setPage] = useState(1);
    let [data, setData] = useState(null);
    let [pack, setPack] = useState(null);
    let key = packQuery(query, 'emoji', page);
    useEffect(() => {
        let alive = true;
        let timer = setTimeout(() => {
            api('/admin/sticker-packs?' + key).then(result => { if (alive) setData(result); }, failure => { if (alive) toastError(failure); });
        }, 200);
        return () => { alive = false; clearTimeout(timer); };
    }, [key]);
    let view = packPage(data);
    return <div className="flex flex-col gap-3 rounded-lg p-3 ring ring-kumo-line" data-testid="gift-emoji-picker">
        {pack ? <PackItems pack={pack} onPick={onPick} onBack={() => setPack(null)} /> : <>
            <Input aria-label="Search emoji packs" placeholder="Search emoji packs…" className="w-full" value={query} onChange={event => { setQuery(event.target.value); setPage(1); }} onKeyDown={event => { if (event.key === 'Enter') event.preventDefault(); }} autoComplete="off" />
            {!data ? <Loader /> : null}
            {data && !view.total ? <p className="text-sm text-kumo-subtle">No emoji packs match.</p> : null}
            {view.total ? <div className="flex max-h-64 flex-col gap-1 overflow-y-auto">
                {view.items.map(item => <button key={item.id} type="button" className="flex min-w-0 items-center justify-between gap-3 rounded-md px-2 py-1.5 text-left hover:bg-kumo-recessed focus-visible:ring focus-visible:ring-kumo-line" onClick={() => setPack(item)}>
                    <span className="min-w-0 truncate text-sm text-kumo-default">{item.title}</span>
                    <span className="flex shrink-0 items-center gap-1 text-xs text-kumo-subtle">{number(item.count)}<CaretRightIcon size={14} /></span>
                </button>)}
            </div> : null}
            {view.pages > 1 ? <div className="flex items-center justify-between gap-2">
                <Button type="button" size="sm" variant="ghost" disabled={view.page <= 1} onClick={() => setPage(view.page - 1)}>Previous</Button>
                <span className="text-xs text-kumo-subtle">{view.page} / {view.pages}</span>
                <Button type="button" size="sm" variant="ghost" disabled={view.page >= view.pages} onClick={() => setPage(view.page + 1)}>Next</Button>
            </div> : null}
        </>}
    </div>;
}

function SwitchRow({ label, description, checked, onCheckedChange }) {
    return <div className="flex items-center justify-between gap-4 rounded-lg px-3 py-3 ring ring-kumo-line">
        <span className="min-w-0">
            <span className="block text-sm font-medium text-kumo-default">{label}</span>
            <span className="block text-xs text-kumo-subtle">{description}</span>
        </span>
        <Switch aria-label={label} checked={checked} onCheckedChange={onCheckedChange} />
    </div>;
}

function UpgradeSection({ gift, fields, setFields, set }) {
    let [collections, setCollections] = useState(null);
    useEffect(() => {
        let alive = true;
        api('/admin/collectibles').then(data => { if (alive) setCollections(data.collections); }, failure => { if (alive) { setCollections([]); toastError(failure); } });
        return () => { alive = false; };
    }, []);
    let picked = collections?.find(item => String(item.id) === fields.setId);
    return <>
        <SwitchRow label="Upgradable" description="Its owner can turn it into a numbered collectible with a random model, pattern and backdrop." checked={fields.upgradable} onCheckedChange={value => setFields(current => ({ ...current, upgradable: value }))} />
        {fields.upgradable ? <div className="flex flex-col gap-3 rounded-lg p-3 ring ring-kumo-line" data-testid="gift-upgrade">
            {!collections ? <Loader /> : null}
            {collections && !collections.length ? <p className="text-sm text-kumo-subtle">No collections are imported yet. Import a bundle with ggcollectibles first.</p> : null}
            {collections?.length ? <Select label="Collection" hideLabel={false} className="w-full" placeholder="Choose a collection" value={fields.setId} onValueChange={value => setFields(current => ({ ...current, setId: value ?? '', supply: current.supply || String(collections.find(item => String(item.id) === value)?.supply || '') }))} items={collectionItems(collections, gift?.id)} /> : null}
            {picked ? <div className="flex flex-wrap items-center gap-2">
                {picked.preview.map(id => <StickerPicture key={id} id={id} alt={picked.title} size={48} />)}
                <span className="text-xs text-kumo-subtle">{number(picked.models)} models · {number(picked.patterns)} patterns · {number(picked.backdrops)} backdrops</span>
                {picked.originalId ? <Button type="button" size="sm" variant="secondary" icon={ImageIcon} onClick={() => setFields(current => ({ ...current, stickerId: picked.originalId }))}>Use its art for the gift</Button> : null}
            </div> : null}
            <Input label="Upgrade price in Stars" description="What the owner pays, or the sender with the gift." className="w-full" inputMode="numeric" required value={fields.upgradeStars} onChange={set('upgradeStars')} autoComplete="off" />
            <Input label="Transfer fee in Stars" description="0 lets owners give it away for free." className="w-full" inputMode="numeric" value={fields.transferStars} onChange={set('transferStars')} autoComplete="off" />
            <Input label="Supply" description={`How many can ever be issued. Leave empty for unlimited.${picked ? ` ${number(picked.issued)} issued so far; a limit can't go under that plus the upgrades already paid for.` : ''}`} placeholder="Unlimited" className="w-full" inputMode="numeric" value={fields.supply} onChange={set('supply')} autoComplete="off" />
        </div> : null}
        {fields.upgradable ? <MarketSection gift={gift} fields={fields} setFields={setFields} set={set} /> : null}
    </>;
}

function MarketSection({ gift, fields, setFields, set }) {
    let market = gift?.market;
    let base = Number(fields.stars || 0) + Number(fields.upgradeStars || 0);
    return <>
        <SwitchRow label="Sell as variants" description="The shop opens the collection instead: buyers filter by model, backdrop and symbol and pick the one they want. It's minted with the next number when bought." checked={fields.variants} onCheckedChange={value => setFields(current => ({ ...current, variants: value }))} />
        {fields.variants ? <div className="flex flex-col gap-3 rounded-lg p-3 ring ring-kumo-line" data-testid="gift-market">
            <Input label="Base price in Stars" description="What the most common combination costs. Leave empty for the gift's price plus its upgrade." placeholder={base ? String(base) : 'Price plus upgrade'} className="w-full" inputMode="numeric" value={fields.baseStars} onChange={set('baseStars')} autoComplete="off" />
            <Input label="Rarity multiplier" description="The rarest combination costs the base times this, the rest in between by rarity. 1 is one price for all." className="w-full" inputMode="decimal" value={fields.rarityMultiplier} onChange={set('rarityMultiplier')} autoComplete="off" />
            {market?.enabled ? <span className="text-xs text-kumo-subtle">{marketText(market)} · from {number(market.minStars)} to {number(market.maxStars)} Stars</span> : null}
        </div> : null}
    </>;
}

function CommissionDialog({ onClose }) {
    let [state, setState] = useState(null);
    let [percent, setPercent] = useState('');
    useEffect(() => {
        let alive = true;
        api('/admin/gift-market').then(data => { if (alive) { setState(data); setPercent(commissionPercent(data.commissionPermille)); } }, failure => { if (alive) toastError(failure); });
        return () => { alive = false; };
    }, []);
    return <ActionDialog open onClose={onClose} icon={GiftIcon} title="Resale commission" description="What the instance keeps when people sell collectibles to each other. The seller gets the rest; the commission goes to the gifts account." confirmLabel="Save" onConfirm={async () => {
        await api('/admin/gift-market', 'PATCH', commissionBody(percent));
        onClose();
        toast('Commission saved.', 'success');
    }}>
        {!state ? <Loader /> : <Input label="Commission, %" description={`0 to ${commissionPercent(state.maxCommissionPermille)}%. Listings cost ${number(state.minResaleStars)} to ${number(state.maxResaleStars)} Stars.`} className="w-full" inputMode="decimal" required value={percent} onChange={event => setPercent(event.target.value)} autoComplete="off" />}
    </ActionDialog>;
}

function GiftEditor({ gift, removing, onRemove, onClose, reload }) {
    let [fields, setFields] = useState(() => ({ title: gift?.title || '', emoji: gift?.emoji || '', stickerId: gift?.stickerId || '', stars: String(gift?.stars ?? ''), convertStars: String(gift?.convertStars ?? ''), stock: gift?.stock === null || gift?.stock === undefined ? '' : String(gift.stock), premiumOnly: Boolean(gift?.premiumOnly), enabled: gift ? gift.enabled : true, ...upgradeFields(gift), ...marketFields(gift) }));
    let set = key => event => setFields(current => ({ ...current, [key]: event.target.value }));
    let [picking, setPicking] = useState(false);
    let sold = gift?.sold > 0;
    let stickerId = fields.stickerId.trim();
    return <ActionDialog open onClose={onClose} icon={removing ? TrashIcon : GiftIcon} destructive={removing} title={removing ? 'Remove gift?' : gift ? 'Edit gift' : 'Add gift'} description={removing ? (sold ? `${number(gift.sold)} sold, so it stays on their profiles and only leaves the catalog.` : 'Nobody bought this gift, so it is deleted.') : 'The picture comes from the imported packs, by emoji, or is the custom emoji you pick.'} confirmLabel={removing ? 'Remove' : 'Save'} onConfirm={async () => {
        if (removing) await api('/admin/gifts/' + gift.id, 'DELETE');
        else if (gift) await api('/admin/gifts/' + gift.id, 'PATCH', giftBody(fields));
        else await api('/admin/gifts', 'POST', giftBody(fields));
        onClose();
        await reload();
        toast(removing ? 'Gift removed.' : 'Gift saved.', 'success');
    }}>
        <Input label="Title" className="w-full" maxLength={64} required readOnly={removing} value={fields.title} onChange={set('title')} autoComplete="off" />
        <Input label="Emoji" description={stickerId ? 'Left empty, it comes from the custom emoji\'s pack.' : undefined} className="w-full" maxLength={16} required={!stickerId} readOnly={removing} value={fields.emoji} onChange={set('emoji')} autoComplete="off" />
        <Input label="Custom emoji ID" description="Optional. A custom emoji or sticker's document id: it becomes the picture instead of one picked by emoji." className="w-full" inputMode="numeric" readOnly={removing} value={fields.stickerId} onChange={set('stickerId')} />
        {removing ? null : <div className="flex items-center gap-2">
            {stickerId ? <StickerPicture key={stickerId} id={stickerId} alt={fields.emoji} /> : null}
            <Button type="button" size="sm" variant="secondary" icon={SmileyIcon} aria-expanded={picking} onClick={() => setPicking(open => !open)}>{picking ? 'Hide packs' : 'Pick from packs'}</Button>
            {stickerId ? <Button type="button" size="sm" variant="ghost" icon={XIcon} onClick={() => setFields(current => ({ ...current, stickerId: '' }))}>Clear</Button> : null}
        </div>}
        {picking && !removing ? <EmojiPicker onPick={item => { setFields(current => pickSticker(current, item)); setPicking(false); }} /> : null}
        <Input label="Price in Stars" className="w-full" inputMode="numeric" required readOnly={removing} value={fields.stars} onChange={set('stars')} autoComplete="off" />
        <Input label="Converts to Stars" className="w-full" inputMode="numeric" readOnly={removing} value={fields.convertStars} onChange={set('convertStars')} autoComplete="off" />
        <Input label="Stock left" description="Leave empty for unlimited." className="w-full" inputMode="numeric" readOnly={removing} value={fields.stock} onChange={set('stock')} autoComplete="off" />
        {removing ? null : <>
            <SwitchRow label="Premium only" description="Only Premium members can send it." checked={fields.premiumOnly} onCheckedChange={value => setFields(current => ({ ...current, premiumOnly: value }))} />
            <SwitchRow label="On sale" description="Off keeps the gift here but hides it from the gift list." checked={fields.enabled} onCheckedChange={value => setFields(current => ({ ...current, enabled: value }))} />
            <UpgradeSection gift={gift} fields={fields} setFields={setFields} set={set} />
        </>}
        {gift && !removing ? <div className="flex items-center justify-between gap-4 border-t border-kumo-hairline pt-4">
            <span className="text-xs text-kumo-subtle">Gifts people already have keep what they were worth.</span>
            <Button type="button" size="sm" variant="secondary-destructive" icon={TrashIcon} onClick={onRemove}>Remove gift</Button>
        </div> : null}
    </ActionDialog>;
}

export default function Gifts() {
    let live = useLive('/admin/gifts');
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [editing, setEditing] = useState(null);
    let [commission, setCommission] = useState(false);
    if (!live.data) return <Loading error={live.error} label="Loading gifts…" />;
    let all = live.data.gifts;
    let view = paged(filterGifts(all, query, filter), page, 12);
    return <>
        <Panel flush title={<>Gifts <Badge variant="secondary" className="ml-1 align-middle">{number(all.length)}</Badge></>} description="What people can send each other for Stars." actions={<div className="flex gap-2"><Button variant="secondary" onClick={() => setCommission(true)}>Resale commission</Button><Button icon={PlusIcon} variant="primary" onClick={() => setEditing({ gift: null, removing: false })}>Add gift</Button></div>}>
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search gifts…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All gifts'], ['enabled', 'On sale'], ['disabled', 'Off sale']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Gift</Table.Head>
                        <Table.Head>Price</Table.Head>
                        <Table.Head>Converts to</Table.Head>
                        <Table.Head>Stock</Table.Head>
                        <Table.Head>Sold</Table.Head>
                        <Table.Head>Upgrades</Table.Head>
                        <Table.Head>Market</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(gift => <Table.Row key={gift.id}>
                        <Table.Cell className="text-sm font-medium text-kumo-default">{gift.emoji} {gift.title}</Table.Cell>
                        <Table.Cell className="font-mono text-sm tabular-nums text-kumo-default">{number(gift.stars)}</Table.Cell>
                        <Table.Cell className="font-mono text-sm tabular-nums text-kumo-subtle">{number(gift.convertStars)}</Table.Cell>
                        <Table.Cell className="font-mono text-sm tabular-nums text-kumo-subtle">{gift.limited ? `${number(gift.stock)} / ${number(gift.stockTotal)}` : 'Unlimited'}</Table.Cell>
                        <Table.Cell className="font-mono text-sm tabular-nums text-kumo-subtle">{number(gift.sold)}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{gift.upgrade ? <span title={gift.upgrade.title}><span className="text-kumo-default">{gift.upgrade.title}</span> <span className="font-mono tabular-nums">{number(gift.upgrade.issued)} / {gift.upgrade.supply ? number(gift.upgrade.supply) : 'Unlimited'}</span></span> : '—'}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{marketText(gift.market)}</Table.Cell>
                        <Table.Cell><Status>{gift.enabled ? 'On sale' : 'Off sale'}</Status></Table.Cell>
                        <Table.Cell className="text-right"><Button size="sm" variant="ghost" onClick={() => setEditing({ gift, removing: false })}>Manage <CaretRightIcon size={14} /></Button></Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={GiftIcon} title="No matching gifts" description="Try another search or filter, or add a gift." />}
            <Pager view={view} setPage={setPage} noun="gifts" />
        </Panel>
        {commission ? <CommissionDialog onClose={() => setCommission(false)} /> : null}
        {editing ? <GiftEditor key={editing.gift?.id || 'new'} gift={editing.gift} removing={editing.removing} onRemove={() => setEditing(current => ({ ...current, removing: true }))} onClose={() => setEditing(null)} reload={live.reload} /> : null}
    </>;
}
