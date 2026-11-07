import { useEffect, useState } from 'react';
import { Badge, Button, Input, Select, Table, Tabs } from '@cloudflare/kumo';
import { DiamondIcon, GiftIcon, MagnifyingGlassIcon, SparkleIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { date, number } from '../lib/format.js';
import { toastError, useLive } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Loading, Pager, Panel, Row, Rows, Status, Toolbar } from '../components/ui.jsx';

const tabs = [{ value: 'gifts', label: 'Gifts' }, { value: 'collectibles', label: 'Collectibles' }, { value: 'fragment', label: 'Fragment' }, { value: 'holdings', label: 'Holdings lookup' }];
const size = 25;

// #circulation/user/42 opens the lookup on account 42 (the member dialog links here)
export function circulationTarget(hash) {
    let [, kind, id] = String(hash || '').replace(/^#/, '').split('/');
    let user = Number(id);
    return kind === 'user' && Number.isSafeInteger(user) && user > 0 ? { tab: 'holdings', user } : { tab: 'gifts', user: null };
}

// a server page as Pager reads it
export function serverView(page) {
    let total = Number(page?.total || 0);
    let per = Number(page?.pageSize || size);
    let current = Number(page?.page || 1);
    let count = page?.items?.length || 0;
    return { items: page?.items || [], page: current, pages: Math.max(1, Math.ceil(total / per)), total, from: count ? (current - 1) * per + 1 : 0, to: (current - 1) * per + count };
}

export function query(path, params) {
    let search = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== '' && value !== null && value !== undefined && value !== false).map(([key, value]) => [key, value === true ? '1' : String(value)]));
    let text = search.toString();
    return text ? path + '?' + text : path;
}

export function holderLabel(owner) {
    if (!owner) return 'Instance reserve';
    if (owner.kind === 'channel') return (owner.name || 'Channel #' + owner.id) + (owner.username ? ' · @' + owner.username : '');
    if (owner.state === 'deleted') return 'Deleted Account #' + owner.id;
    return (owner.name || '#' + owner.id) + (owner.username ? ' · @' + owner.username : '');
}

export function stockText(gift) {
    if (gift.stock === null || gift.stock === undefined) return 'Unlimited';
    return `${number(gift.stock)} of ${number(gift.stockTotal ?? gift.stock)}`;
}

function Holder({ owner, onOpen }) {
    let label = holderLabel(owner);
    let note = owner?.kind === 'channel' ? 'Channel' : owner?.isSystem ? 'System account' : owner?.state && owner.state !== 'active' ? owner.state : null;
    let body = <span className="min-w-0">
        <span className="block truncate text-sm text-kumo-default">{label}</span>
        {note ? <small className="block text-xs text-kumo-subtle">{note}</small> : null}
    </span>;
    if (!owner || owner.kind !== 'user' || !onOpen) return body;
    return <button type="button" className="min-w-0 text-left hover:underline" onClick={() => onOpen(owner.id)}>{body}</button>;
}

function Cell({ children, className = '' }) {
    return <Table.Cell className={'text-sm tabular-nums text-kumo-default ' + className}>{children}</Table.Cell>;
}

function Holders({ gift, onClose, onOpenUser }) {
    let [page, setPage] = useState(1);
    let live = useLive(query(`/admin/circulation/gifts/${gift.id}/holders`, { page, size: 10 }));
    let view = serverView(live.data);
    return <ActionDialog open onClose={onClose} icon={GiftIcon} title={`${gift.emoji} ${gift.title}: holders`} description={`${number(gift.inCirculation)} in circulation with ${number(gift.holders)} holders, most first.`} cancelLabel="Close" size="lg">
        {!live.data ? <Loading error={live.error} /> : view.total ? <div className="divide-y divide-kumo-hairline">
            {view.items.map((line, index) => <div key={index} className="flex items-center justify-between gap-4 py-2.5">
                <Holder owner={line.owner} onOpen={id => { onClose(); onOpenUser(id); }} />
                <span className="shrink-0 text-sm tabular-nums text-kumo-default">{number(line.count)}{line.upgraded ? <small className="ml-1 text-xs text-kumo-subtle">({number(line.upgraded)} upgraded)</small> : null}</span>
            </div>)}
        </div> : <EmptyState icon={GiftIcon} title="Nobody holds this gift" />}
        {view.total ? <Pager view={view} setPage={setPage} noun="holders" /> : null}
    </ActionDialog>;
}

function Gifts({ onOpenUser }) {
    let [page, setPage] = useState(1);
    let [holders, setHolders] = useState(null);
    let live = useLive(query('/admin/circulation/gifts', { page, size }));
    let view = serverView(live.data);
    return <Panel flush title="Catalog gifts" description="Every gift people can buy, and what became of the ones sold. Converted gifts were turned into Stars; upgraded ones are collectibles now.">
        {!live.data ? <Loading error={live.error} /> : view.total ? <Table>
            <Table.Header>
                <Table.Row>
                    <Table.Head>Gift</Table.Head>
                    <Table.Head>Sold</Table.Head>
                    <Table.Head>In circulation</Table.Head>
                    <Table.Head>Converted</Table.Head>
                    <Table.Head>Upgraded</Table.Head>
                    <Table.Head>Stock left</Table.Head>
                    <Table.Head>Holders</Table.Head>
                </Table.Row>
            </Table.Header>
            <Table.Body>
                {view.items.map(gift => <Table.Row key={gift.id}>
                    <Table.Cell>
                        <span className="block text-sm font-medium text-kumo-default">{gift.emoji} {gift.title}</span>
                        <small className="block text-xs text-kumo-subtle">{number(gift.stars)} Stars{gift.enabled ? '' : ' · Disabled'}{gift.heldByDeleted ? ` · ${number(gift.heldByDeleted)} on deleted accounts` : ''}</small>
                    </Table.Cell>
                    <Cell>{number(gift.sold)}</Cell>
                    <Cell>{number(gift.inCirculation)}</Cell>
                    <Cell>{number(gift.converted)}</Cell>
                    <Cell>{number(gift.upgraded)}</Cell>
                    <Cell>{stockText(gift)}</Cell>
                    <Cell>{gift.holders ? <Button size="sm" variant="ghost" className="-ml-2.5 tabular-nums underline" aria-label={`${number(gift.holders)} holders of ${gift.title}`} onClick={() => setHolders(gift)}>{number(gift.holders)}</Button> : '0'}</Cell>
                </Table.Row>)}
            </Table.Body>
        </Table> : <EmptyState icon={GiftIcon} title="No gifts in the catalog" />}
        {view.total ? <Pager view={view} setPage={setPage} noun="gifts" /> : null}
        {holders ? <Holders gift={holders} onClose={() => setHolders(null)} onOpenUser={onOpenUser} /> : null}
    </Panel>;
}

function Pieces({ collection, onClose, onOpenUser }) {
    let [page, setPage] = useState(1);
    let [listed, setListed] = useState(false);
    let [num, setNum] = useState('');
    let live = useLive(query(`/admin/circulation/collections/${collection.id}/items`, { page, size: 10, listed, num: num.trim() }));
    let view = serverView(live.data);
    return <ActionDialog open onClose={onClose} icon={SparkleIcon} title={collection.title} description={`${number(collection.issued)} issued${collection.supply ? ' of ' + number(collection.supply) : ', no supply cap'} · ${number(collection.owners)} owners`} cancelLabel="Close" size="lg">
        <div className="flex flex-wrap items-center gap-2">
            <Input aria-label="Number" placeholder="Find #number" inputMode="numeric" value={num} onChange={event => { setNum(event.target.value); setPage(1); }} className="w-40" passwordManagerIgnore />
            <Select aria-label="Filter" className="w-44" value={listed ? 'listed' : 'all'} onValueChange={value => { setListed(value === 'listed'); setPage(1); }} items={{ all: 'All pieces', listed: 'For resale' }} />
        </div>
        {!live.data ? <Loading error={live.error} /> : view.total ? <div className="divide-y divide-kumo-hairline">
            {view.items.map(piece => <div key={piece.id} className="flex items-center justify-between gap-4 py-2.5">
                <span className="min-w-0">
                    <span className="block text-sm font-medium text-kumo-default">#{piece.num} <span className="font-normal text-kumo-subtle">{piece.model} · {piece.pattern} · {piece.backdrop}</span></span>
                    <span className="block text-xs text-kumo-subtle"><Holder owner={piece.owner} onOpen={id => { onClose(); onOpenUser(id); }} /></span>
                </span>
                {piece.listed ? <Status>{`${number(piece.price)} Stars`}</Status> : <Status variant="secondary">Not for sale</Status>}
            </div>)}
        </div> : <EmptyState icon={SparkleIcon} title="Nothing matches" />}
        {view.total ? <Pager view={view} setPage={setPage} noun="pieces" /> : null}
    </ActionDialog>;
}

function Collectibles({ onOpenUser }) {
    let [page, setPage] = useState(1);
    let [open, setOpen] = useState(null);
    let live = useLive(query('/admin/circulation/collections', { page, size }));
    let view = serverView(live.data);
    return <Panel flush title="Collections" description="Upgraded gifts, by collection. Open one for every piece, its owner and resale price.">
        {!live.data ? <Loading error={live.error} /> : view.total ? <Table>
            <Table.Header>
                <Table.Row>
                    <Table.Head>Collection</Table.Head>
                    <Table.Head>Issued</Table.Head>
                    <Table.Head>Owners</Table.Head>
                    <Table.Head>For resale</Table.Head>
                    <Table.Head><span className="sr-only">Pieces</span></Table.Head>
                </Table.Row>
            </Table.Header>
            <Table.Body>
                {view.items.map(set => <Table.Row key={set.id}>
                    <Table.Cell>
                        <span className="block text-sm font-medium text-kumo-default">{set.title}</span>
                        <small className="block text-xs text-kumo-subtle">/nft/{set.slug}-…{set.giftId ? '' : ' · No gift upgrades into it'}</small>
                    </Table.Cell>
                    <Cell>{number(set.issued)}{set.supply ? ' of ' + number(set.supply) : ''}</Cell>
                    <Cell>{number(set.owners)}</Cell>
                    <Cell>{number(set.listed)}</Cell>
                    <Table.Cell className="text-right"><Button size="sm" variant="ghost" disabled={!set.issued} onClick={() => setOpen(set)}>Pieces</Button></Table.Cell>
                </Table.Row>)}
            </Table.Body>
        </Table> : <EmptyState icon={SparkleIcon} title="No collections imported" />}
        {view.total ? <Pager view={view} setPage={setPage} noun="collections" /> : null}
        {open ? <Pieces collection={open} onClose={() => setOpen(null)} onOpenUser={onOpenUser} /> : null}
    </Panel>;
}

function Fragment({ onOpenUser }) {
    let [page, setPage] = useState(1);
    let [search, setSearch] = useState('');
    let [filter, setFilter] = useState('all');
    let live = useLive(query('/admin/circulation/fragment', { page, size, q: search.trim(), kind: filter === 'username' || filter === 'number' ? filter : '', listed: filter === 'listed' }));
    let view = serverView(live.data);
    return <Panel flush title="Fragment" description="Every collectible username and number: who owns it, the channel using it, the asking price and the last sale.">
        <Toolbar query={search} onQuery={value => { setSearch(value); setPage(1); }} placeholder="Search usernames and numbers" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All items'], ['username', 'Usernames'], ['number', 'Numbers'], ['listed', 'For sale']]} />
        {!live.data ? <Loading error={live.error} /> : view.total ? <Table>
            <Table.Header>
                <Table.Row>
                    <Table.Head>Identifier</Table.Head>
                    <Table.Head>Owner</Table.Head>
                    <Table.Head>Status</Table.Head>
                    <Table.Head>Last sale</Table.Head>
                </Table.Row>
            </Table.Header>
            <Table.Body>
                {view.items.map(item => <Table.Row key={item.id}>
                    <Table.Cell>
                        <span className="block text-sm font-medium text-kumo-default">{item.displayTitle || item.title}</span>
                        <small className="block text-xs text-kumo-subtle">{item.kind === 'number' ? 'Number' : 'Username'}{item.channel ? ' · used by ' + item.channel.title : ''}</small>
                    </Table.Cell>
                    <Table.Cell><Holder owner={item.owner} onOpen={onOpenUser} /></Table.Cell>
                    <Table.Cell>{item.listed ? <Status>{`${item.price} GRAM`}</Status> : <Status>Unlisted</Status>}</Table.Cell>
                    <Cell>{item.lastSale ? <>{item.lastSale} GRAM<small className="block text-xs text-kumo-subtle">{date(item.lastSaleAt)}</small></> : <span className="text-kumo-subtle">Never sold</span>}</Cell>
                </Table.Row>)}
            </Table.Body>
        </Table> : <EmptyState icon={DiamondIcon} title="Nothing matches" />}
        {view.total ? <Pager view={view} setPage={setPage} noun="items" /> : null}
    </Panel>;
}

function Holdings({ user, setUser }) {
    let [search, setSearch] = useState('');
    let [results, setResults] = useState(null);
    let live = useLive(user ? '/admin/circulation/users/' + user : null);
    async function find(event) {
        event.preventDefault();
        if (!search.trim()) return;
        try { setResults((await api(query('/admin/circulation/users', { q: search.trim() }))).items); }
        catch (error) { toastError(error); }
    }
    let data = user ? live.data : null;
    return <div className="flex flex-col gap-6">
        <Panel title="Find an account" description="Name, @username or #id. Deleted accounts and the Deleted Accounts system account are listed too.">
            <form className="flex flex-wrap items-end gap-2" onSubmit={find} autoComplete="off" data-1p-ignore>
                <Input aria-label="Account" placeholder="@username, name or #id" value={search} onChange={event => setSearch(event.target.value)} className="w-72" passwordManagerIgnore />
                <Button type="submit" variant="secondary" icon={MagnifyingGlassIcon}>Search</Button>
            </form>
            {results ? results.length ? <div className="mt-3 flex flex-wrap gap-2">
                {results.map(found => <Button key={found.id} size="sm" variant={found.id === user ? 'primary' : 'secondary'} onClick={() => setUser(found.id)}>{holderLabel(found)}{found.isSystem ? ' · system' : ''}</Button>)}
            </div> : <p className="mt-3 text-sm text-kumo-subtle">No account matches.</p> : null}
        </Panel>
        {!user ? null : !data ? <Panel><Loading error={live.error} /></Panel> : <>
            <Panel title={holderLabel(data.user)} description={data.deletedAt ? `Deleted ${date(data.deletedAt)} (${data.deletionSource})` : data.user.isSystem ? 'System account' : 'Account #' + data.user.id}>
                <Rows>
                    <Row label="Stars">{number(data.stars)}</Row>
                    {data.wallet.map(coin => <Row key={coin.asset} label={coin.asset}>{coin.main || '0'}<small className="block text-xs text-kumo-subtle">DeFi {coin.defi || '0'} · Earn {coin.earn || '0'}</small></Row>)}
                    <Row label="Collectibles">{number(data.collectibleCount)}</Row>
                    <Row label="Fragment items">{number(data.fragment.length)}</Row>
                </Rows>
            </Panel>
            <div className="grid gap-6 xl:grid-cols-2">
                <Panel title="Gifts" description="Kept gifts by catalog gift; converted ones aren't held any more.">
                    {data.gifts.length ? <Rows>{data.gifts.map(gift => <Row key={gift.giftId} label={`${gift.emoji} ${gift.title}`}>{number(gift.plain + gift.upgraded)}{gift.upgraded ? <small className="block text-xs text-kumo-subtle">{number(gift.upgraded)} upgraded</small> : null}</Row>)}</Rows> : <p className="text-sm text-kumo-subtle">No gifts.</p>}
                </Panel>
                <Panel title="Fragment" description="Usernames and numbers this account owns.">
                    {data.fragment.length ? <Rows>{data.fragment.map(item => <Row key={item.id} label={item.displayTitle || item.title}>{item.listed ? `For sale · ${item.price} GRAM` : 'Unlisted'}{item.lastSale ? <small className="block text-xs text-kumo-subtle">Bought for {item.lastSale} GRAM, {date(item.lastSaleAt)}</small> : null}</Row>)}</Rows> : <p className="text-sm text-kumo-subtle">No Fragment items.</p>}
                </Panel>
            </div>
            <Panel flush title={<>Collectibles <Badge variant="secondary" className="ml-1 align-middle">{number(data.collectibleCount)}</Badge></>} description={data.collectibleCount > data.collectibles.length ? `The first ${number(data.collectibles.length)} by collection and number.` : 'By collection and number.'}>
                {data.collectibles.length ? <Table>
                    <Table.Header><Table.Row><Table.Head>Piece</Table.Head><Table.Head>Model</Table.Head><Table.Head>Resale</Table.Head></Table.Row></Table.Header>
                    <Table.Body>{data.collectibles.map(piece => <Table.Row key={piece.id}>
                        <Table.Cell className="text-sm text-kumo-default">{piece.collection} #{piece.num}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{piece.model}</Table.Cell>
                        <Table.Cell>{piece.listed ? <Status>{`${number(piece.price)} Stars`}</Status> : <Status variant="secondary">Not for sale</Status>}</Table.Cell>
                    </Table.Row>)}</Table.Body>
                </Table> : <EmptyState icon={SparkleIcon} title="No collectibles" />}
            </Panel>
        </>}
    </div>;
}

export default function Circulation() {
    let [state, setState] = useState(() => circulationTarget(globalThis.location?.hash));
    useEffect(() => {
        let change = () => { if (location.hash.startsWith('#circulation')) setState(circulationTarget(location.hash)); };
        window.addEventListener('hashchange', change);
        return () => window.removeEventListener('hashchange', change);
    }, []);
    function openUser(id) {
        location.hash = 'circulation/user/' + id;
    }
    return <div className="flex flex-col gap-6">
        <Tabs variant="underline" aria-label="Circulation" tabs={tabs} value={state.tab} onValueChange={tab => setState(current => ({ ...current, tab }))} />
        {state.tab === 'gifts' ? <Gifts onOpenUser={openUser} /> : null}
        {state.tab === 'collectibles' ? <Collectibles onOpenUser={openUser} /> : null}
        {state.tab === 'fragment' ? <Fragment onOpenUser={openUser} /> : null}
        {state.tab === 'holdings' ? <Holdings user={state.user} setUser={openUser} /> : null}
    </div>;
}
