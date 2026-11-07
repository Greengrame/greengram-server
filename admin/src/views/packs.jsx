import { useCallback, useEffect, useRef, useState } from 'react';
import { Badge, Button, Table, cn } from '@cloudflare/kumo';
import { StickerIcon, TrashIcon } from '@phosphor-icons/react';
import { accountSlot, api } from '../lib/api.js';
import { number } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Loading, Pager, Panel, Toolbar } from '../components/ui.jsx';

const staticCovers = ['image/png', 'image/jpeg', 'image/webp', 'image/svg+xml'];

const toggles = [
    ['premiumOnly', 'Premium Only', 'Everyone'],
    ['isDefault', 'Default', 'Optional'],
    ['enabled', 'Published', 'Unpublished']
];

export function packQuery(query, filter, page) {
    let search = new URLSearchParams({ q: query, page: String(page) });
    if (['sticker', 'emoji'].includes(filter)) search.set('kind', filter);
    return search.toString();
}

// only same-origin static images; animated covers would need a player
export function packCover(pack, slot = accountSlot()) {
    let cover = pack.cover;
    if (cover && staticCovers.includes(cover.mime) && /^\/api\/sticker-media\/\d+$/.test(cover.url)) return { src: cover.url + '?account=' + slot };
    return { label: cover?.mime === 'application/x-tgsticker' ? 'TGS' : cover?.mime === 'video/webm' ? 'WEBM' : 'PACK' };
}

export function packPage(data) {
    let total = data?.total || 0;
    let size = data?.pageSize || 20;
    let page = data?.page || 1;
    return { items: data?.packs || [], page, pages: Math.max(1, Math.ceil(total / size)), total, from: total ? (page - 1) * size + 1 : 0, to: Math.min(page * size, total) };
}

function PackPreview({ pack }) {
    let cover = packCover(pack);
    if (cover.src) return <img className="size-10 shrink-0 rounded-md bg-kumo-recessed object-contain" src={cover.src} alt="" loading="lazy" />;
    return <span className="inline-flex size-10 shrink-0 flex-col items-center justify-center rounded-md bg-kumo-recessed text-kumo-subtle">
        <StickerIcon size={16} />
        <span className="text-[9px] leading-none font-medium tracking-wide">{cover.label}</span>
    </span>;
}

export default function Packs() {
    let { refresh, version } = useAdmin();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [cache, setCache] = useState({ key: null, data: null, error: null });
    let [pending, setPending] = useState(null);
    let [deleting, setDeleting] = useState(null);
    let latest = useRef(null);
    let settled = useRef(null);
    let key = packQuery(query, filter, page);

    let load = useCallback(async function (wanted) {
        let result;
        latest.current = wanted;
        try { result = await api('/admin/sticker-packs?' + wanted); }
        catch (error) {
            if (latest.current === wanted) setCache({ key: wanted, data: null, error });
            return;
        }
        // a newer search or page went out while this one was in flight
        if (latest.current !== wanted) return;
        let params = new URLSearchParams(wanted);
        let actual = packQuery(params.get('q') || '', params.get('kind') || 'all', result.page);
        setCache({ key: actual, data: result, error: null });
        if (actual !== wanted) {
            // the server clamped the page; adopt it without asking again
            settled.current = actual;
            latest.current = actual;
            setPage(result.page);
        }
    }, []);

    useEffect(() => {
        let skip = settled.current === key;
        settled.current = null;
        if (!skip) load(key);
    }, [key, version, load]);

    let current = cache.key === key ? cache : null;
    let shown = cache.data;
    let view = packPage(shown);

    async function change(pack, setting) {
        setPending(pack.id + ':' + setting);
        try {
            await api('/admin/sticker-packs/' + pack.id, 'PATCH', { [setting]: !pack[setting] });
            await load(key);
            await refresh();
            toast('Pack settings updated.', 'success');
        } catch (error) { toastError(error); }
        finally { setPending(null); }
    }

    return <>
        <Panel flush title="Sticker & Emoji Packs" description="Optional stops automatic installation. Delete removes the pack from every account; existing message media is preserved.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search packs by title or short name…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All packs'], ['sticker', 'Stickers'], ['emoji', 'Custom Emoji']]} />
            {!shown ? <Loading error={cache.key === key ? cache.error : null} label="Loading packs…" /> : view.total ? <Table aria-busy={!current} className={cn('transition-opacity', !current && 'opacity-60')}>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Pack</Table.Head>
                        <Table.Head>Type</Table.Head>
                        <Table.Head>Access</Table.Head>
                        <Table.Head>Default for New Accounts</Table.Head>
                        <Table.Head>Published</Table.Head>
                        <Table.Head><span className="sr-only">Delete</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(pack => <Table.Row key={pack.id}>
                        <Table.Cell>
                            <span className="flex items-center gap-3">
                                <PackPreview pack={pack} />
                                <span className="min-w-0">
                                    <span className="flex items-center gap-2 text-sm font-medium text-kumo-default">{pack.title}{pack.isOfficial ? <Badge variant="outline">Official</Badge> : null}</span>
                                    <span className="block truncate text-xs text-kumo-subtle">{pack.shortName} · {number(pack.count)} items</span>
                                </span>
                            </span>
                        </Table.Cell>
                        <Table.Cell className="text-sm text-kumo-default">{pack.kind === 'emoji' ? 'Custom Emoji' : 'Stickers'}</Table.Cell>
                        {toggles.map(([setting, on, off]) => <Table.Cell key={setting}>
                            <Button size="sm" variant={pack[setting] ? 'secondary' : 'outline'} aria-pressed={Boolean(pack[setting])} loading={pending === pack.id + ':' + setting} disabled={Boolean(pending?.startsWith(pack.id + ':'))} onClick={() => change(pack, setting)}>{pack[setting] ? on : off}</Button>
                        </Table.Cell>)}
                        <Table.Cell className="text-right">
                            <Button size="sm" variant="ghost" icon={TrashIcon} className="text-kumo-danger" aria-label={`Delete ${pack.title}`} onClick={() => setDeleting(pack)}>Delete</Button>
                        </Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={StickerIcon} title="No packs found" description="Choose another type or search term." />}
            <Pager view={view} setPage={value => { if (current) setPage(value); }} noun="packs" />
        </Panel>
        <ActionDialog open={Boolean(deleting)} onClose={() => setDeleting(null)} icon={TrashIcon} destructive title={deleting ? `Delete ${deleting.title}?` : ''} description={deleting ? `This removes the ${deleting.isDefault ? 'default ' : ''}${deleting.kind === 'emoji' ? 'emoji' : 'sticker'} pack for everyone and prevents automatic restoration. Existing message media is kept. This action is recorded in the audit log.` : ''} confirmLabel="Delete pack" onConfirm={async () => {
            await api('/admin/sticker-packs/' + deleting.id, 'DELETE', {});
            setDeleting(null);
            await load(key);
            await refresh();
            toast('Pack deleted.', 'success');
        }} />
    </>;
}
