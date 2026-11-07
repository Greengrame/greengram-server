import { useEffect, useRef, useState } from 'react';
import { Badge, Button, Dialog, Input, Select, Switch, cn } from '@cloudflare/kumo';
import { ArrowSquareOutIcon, FlagIcon, XIcon } from '@phosphor-icons/react';
import { api, accountSlot } from '../lib/api.js';
import { ago, bytes, dateTime, matches, number, paged } from '../lib/format.js';
import { toast, useAdmin, useLive } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { Avatar, EmptyState, Loading, Pager, Panel, Status, Toolbar } from '../components/ui.jsx';

const kindLabel = { user: 'Person', bot: 'Bot', group: 'Group', channel: 'Channel' };
const reportKinds = { message: 'Messages', peer: 'Whole chat or profile', photo: 'Profile picture', story: 'Story', reaction: 'Reaction', spam: 'Spam bar' };
const actionLabel = { delete: 'messages deleted', freeze: 'account frozen', ban: 'banned from the chat' };

export function reportsPath(status, reason, target) {
    let query = new URLSearchParams({ status });
    if (reason !== 'all') query.set('reason', reason);
    if (target !== 'all') query.set('target', target);
    return '/admin/reports?' + query;
}

// what the search box looks through: the target, the reporters and what they saw
export function filterGroups(groups, query) {
    return groups.filter(group => matches(query, group.target.name, group.target.username, ...group.reports.flatMap(report => [report.reporter.name, report.reporter.username, report.comment, ...(report.snapshot.messages || []).map(message => message.text)])));
}

export function messengerLink(peerId) {
    return '/#' + peerId;
}

function Photo({ id, className }) {
    let [failed, setFailed] = useState(false);
    if (!id || failed) return <span className="text-xs text-kumo-subtle">(picture no longer available)</span>;
    return <a href={`/api/admin/report-photos/${id}?account=${accountSlot()}`} target="_blank" rel="noreferrer"><img className={cn('max-h-64 max-w-full rounded-lg object-contain ring ring-kumo-line', className)} src={`/api/admin/report-photos/${id}?account=${accountSlot()}`} alt="Reported picture" loading="lazy" onError={() => setFailed(true)} /></a>;
}

// TGS is gzipped Lottie, drawn on one frame
function Sticker({ id, emoji }) {
    let box = useRef(null);
    let [picture, setPicture] = useState(null);
    useEffect(() => {
        let alive = true;
        let url;
        let animation;
        (async () => {
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
        })().catch(() => { if (alive) setPicture({ failed: true }); });
        return () => {
            alive = false;
            animation?.destroy();
            if (url) URL.revokeObjectURL(url);
        };
    }, [id]);
    return <span ref={box} className="inline-flex size-28 items-center justify-center overflow-hidden text-4xl" title={'Sticker ' + (emoji || '')}>
        {picture?.url && !picture.video ? <img className="size-full object-contain" src={picture.url} alt={emoji || 'Sticker'} /> : null}
        {picture?.url && picture.video ? <video className="size-full object-contain" src={picture.url} muted playsInline autoPlay loop /> : null}
        {picture?.failed ? emoji || 'Sticker' : null}
    </span>;
}

export function mediaText(media) {
    if (!media) return '';
    let what = { photo: 'Photo', sticker: 'Sticker', video: 'Video', gif: 'GIF', voice: 'Voice message', audio: 'Audio', document: 'File', other: media.what || 'Attachment' }[media.type] || 'Attachment';
    return [what, media.emoji, media.name, media.size ? bytes(media.size) : ''].filter(Boolean).join(' · ');
}

function Media({ media }) {
    if (!media) return null;
    if (media.type === 'photo') return <Photo id={media.id} />;
    if (media.type === 'sticker' && media.id) return <Sticker id={media.id} emoji={media.emoji} />;
    return <span className="inline-flex rounded-md px-2 py-1 text-xs text-kumo-subtle ring ring-kumo-line">{mediaText(media)}</span>;
}

function Snapshot({ report }) {
    let shot = report.snapshot || {};
    return <div className="flex flex-col gap-2">
        {(shot.messages || []).map(message => <div key={message.id} className="rounded-lg px-3 py-2 ring ring-kumo-line">
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 text-xs text-kumo-subtle">
                <span className="font-medium text-kumo-default">{message.fromName || 'Unknown'}</span>
                <span className="tabular-nums">{dateTime(message.date)}{message.editDate ? ' · edited before the report' : ''}</span>
            </div>
            {message.text ? <p className="mt-1 text-sm whitespace-pre-wrap break-words text-kumo-default">{message.text}</p> : null}
            {message.media ? <div className="mt-2"><Media media={message.media} /></div> : null}
        </div>)}
        {shot.story ? <div className="rounded-lg px-3 py-2 ring ring-kumo-line">
            <span className="text-xs text-kumo-subtle">Story · {dateTime(shot.story.date)}</span>
            {shot.story.caption ? <p className="mt-1 text-sm whitespace-pre-wrap break-words text-kumo-default">{shot.story.caption}</p> : null}
            {shot.story.media ? <div className="mt-2"><Media media={shot.story.media} /></div> : null}
        </div> : report.kind === 'story' ? <p className="text-xs text-kumo-subtle">The story was gone or hidden from the reporter when they reported it.</p> : null}
        {shot.photo ? <Photo id={shot.photo.id} /> : null}
        {shot.reaction ? <p className="text-sm text-kumo-default">Reacted with <span className="text-lg">{shot.reaction.split(' ').map(item => item.startsWith('#') ? '(custom emoji)' : item).join(' ')}</span></p> : null}
        {shot.peer && report.kind !== 'message' ? <div className="rounded-lg px-3 py-2 text-sm ring ring-kumo-line">
            <span className="font-medium text-kumo-default">{shot.peer.name}</span>{shot.peer.username ? <span className="text-kumo-subtle"> @{shot.peer.username}</span> : null}
            {shot.peer.about ? <p className="mt-1 whitespace-pre-wrap break-words text-xs text-kumo-subtle">{shot.peer.about}</p> : null}
        </div> : null}
    </div>;
}

function ReportCard({ report }) {
    let where = report.chat.kind === 'user' ? 'private chat with ' + report.chat.title : (report.chat.kind === 'group' ? 'group ' : 'channel ') + report.chat.title;
    return <li className="flex flex-col gap-2 py-4">
        <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{report.reasonLabel}</Badge>
            <span className="text-xs text-kumo-subtle">{reportKinds[report.kind]} · {where}</span>
            {report.status !== 'open' ? <Status variant="secondary">{report.status === 'dismissed' ? 'Dismissed' : 'Resolved'}</Status> : null}
        </div>
        <p className="text-xs text-kumo-subtle">Reported by <span className="text-kumo-default">{report.reporter.name}</span>{report.reporter.username ? ' @' + report.reporter.username : ''} · {ago(report.createdAt)}</p>
        {report.comment ? <blockquote className="border-l-2 border-kumo-line pl-3 text-sm whitespace-pre-wrap break-words text-kumo-default">{report.comment}</blockquote> : null}
        <Snapshot report={report} />
        {report.status !== 'open' ? <p className="text-xs text-kumo-subtle">{report.status === 'dismissed' ? 'Dismissed' : 'Resolved: ' + report.actions.map(action => actionLabel[action] || action).join(', ')} by {report.closedBy || 'an admin'} · {ago(report.closedAt)}</p> : null}
    </li>;
}

function SwitchRow({ label, description, checked, onCheckedChange }) {
    return <label className="flex items-center justify-between gap-4 rounded-lg px-3 py-3 ring ring-kumo-line">
        <span className="min-w-0">
            <span className="block text-sm font-medium text-kumo-default">{label}</span>
            <span className="block text-xs text-kumo-subtle">{description}</span>
        </span>
        <Switch aria-label={label} checked={checked} onCheckedChange={onCheckedChange} />
    </label>;
}

// actions apply to the open reports on screen; one filed meanwhile stays open
export function resolveBody(group, choice, note) {
    return { reports: group.reports.filter(report => report.status === 'open').map(report => report.id), delete: Boolean(choice.delete), freeze: Boolean(choice.freeze), ban: Boolean(choice.ban), note: note.trim() };
}

function Review({ group, onClose, reload }) {
    let dialogs = useDialogs();
    let admin = useAdmin();
    let [choice, setChoice] = useState({});
    let [note, setNote] = useState('');
    let [busy, setBusy] = useState('');
    let [error, setError] = useState('');
    let open = group.reports.filter(report => report.status === 'open');
    let closed = group.reports.filter(report => report.status !== 'open');
    let target = group.target;
    let chosen = choice.delete || choice.freeze || choice.ban;
    async function act(kind) {
        setBusy(kind);
        setError('');
        try {
            let first = open[0].id;
            if (kind === 'dismiss') await api(`/admin/reports/${first}/dismiss`, 'POST', { reports: open.map(report => report.id), note: note.trim() });
            else await api(`/admin/reports/${first}/resolve`, 'POST', resolveBody(group, choice, note));
            onClose();
            // the sidebar's count comes with the overview
            await Promise.all([reload(), admin.refresh()]);
            toast(kind === 'dismiss' ? 'Reports dismissed.' : 'Done. The reports are closed.', 'success');
        } catch (failure) { setError(failure.message || String(failure)); }
        finally { setBusy(''); }
    }
    return <Dialog.Root open onOpenChange={value => { if (!value) onClose(); }}>
        <Dialog size="lg" className="max-h-[90dvh] overflow-y-auto p-6">
            <div className="flex flex-col gap-4">
                <div className="flex items-start justify-between gap-4">
                    <div className="flex min-w-0 items-center gap-3">
                        <Avatar user={{ id: target.id, name: target.name, avatarPhotoId: target.avatarPhotoId }} size={40} />
                        <div className="min-w-0">
                            <Dialog.Title className="truncate text-lg font-semibold text-kumo-default">{target.name || 'Unknown'}</Dialog.Title>
                            <Dialog.Description className="flex flex-wrap items-center gap-x-2 text-sm text-kumo-subtle">
                                <span>{kindLabel[target.kind]}{target.username ? ' · @' + target.username : ''} · id {target.id}</span>
                                {target.state && target.state !== 'active' ? <Status>{target.state}</Status> : null}
                                {target.gone ? <Status variant="secondary">deleted</Status> : null}
                            </Dialog.Description>
                        </div>
                    </div>
                    <Dialog.Close render={props => <Button {...props} type="button" variant="ghost" shape="square" size="sm" icon={XIcon} aria-label="Close" />} />
                </div>
                <div className="flex flex-wrap gap-2">
                    {target.kind === 'user' ? <Button size="sm" variant="secondary" onClick={() => dialogs.open('member', { id: target.id })}>Member details</Button> : null}
                    <a className="inline-flex items-center gap-1 text-sm text-kumo-default underline" href={messengerLink(target.peerId)} target="_blank" rel="noreferrer">Open in messenger <ArrowSquareOutIcon size={14} /></a>
                </div>
                <ul className="divide-y divide-kumo-hairline">
                    {open.map(report => <ReportCard key={report.id} report={report} />)}
                    {closed.map(report => <ReportCard key={report.id} report={report} />)}
                </ul>
                {open.length ? <div className="flex flex-col gap-2 border-t border-kumo-hairline pt-4">
                    <span className="text-sm font-medium text-kumo-default">What to do about {open.length === 1 ? 'this report' : `these ${open.length} reports`}</span>
                    {group.canDelete ? <SwitchRow label="Delete the reported messages" description="Gone for everyone in the chat." checked={Boolean(choice.delete)} onCheckedChange={value => setChoice(current => ({ ...current, delete: value }))} /> : null}
                    {group.canBan ? <SwitchRow label="Ban from the group" description="Removed from the group or channel where it happened and kept out." checked={Boolean(choice.ban)} onCheckedChange={value => setChoice(current => ({ ...current, ban: value }))} /> : null}
                    {group.canFreeze ? <SwitchRow label="Freeze the account" description="Read-only until an admin unfreezes it. They can appeal through @SpamBot." checked={Boolean(choice.freeze)} onCheckedChange={value => setChoice(current => ({ ...current, freeze: value }))} /> : null}
                    <Input label="Note" description="Optional. Kept in the audit log." className="w-full" maxLength={500} value={note} onChange={event => setNote(event.target.value)} autoComplete="off" />
                    {error ? <p className="text-sm text-kumo-danger" role="alert">{error}</p> : null}
                    <div className="flex flex-wrap justify-end gap-2 pt-1">
                        <Button type="button" variant="secondary" loading={busy === 'dismiss'} disabled={Boolean(busy)} onClick={() => act('dismiss')}>Dismiss</Button>
                        <Button type="button" variant={choice.freeze || choice.ban || choice.delete ? 'destructive' : 'primary'} loading={busy === 'resolve'} disabled={!chosen || Boolean(busy)} onClick={() => act('resolve')}>Take action</Button>
                    </div>
                </div> : null}
            </div>
        </Dialog>
    </Dialog.Root>;
}

export default function Reports() {
    let [status, setStatus] = useState('open');
    let [reason, setReason] = useState('all');
    let [target, setTarget] = useState('all');
    let [query, setQuery] = useState('');
    let [page, setPage] = useState(1);
    let [reviewing, setReviewing] = useState(null);
    let live = useLive(reportsPath(status, reason, target), { interval: 30000 });
    if (!live.data) return <Loading error={live.error} label="Loading reports…" />;
    let groups = filterGroups(live.data.groups, query);
    let view = paged(groups, page, 15);
    let current = reviewing && live.data.groups.find(group => group.key === reviewing);
    let reset = setter => value => { setter(value ?? 'all'); setPage(1); };
    return <>
        <Panel flush title={<>Reports <Badge variant="secondary" className="ml-1 align-middle">{number(live.data.open)}</Badge></>} description="What people reported from the apps, by the account or chat it's about.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search names or reported text…" filter={status} onFilter={reset(setStatus)} filters={[['open', 'Open'], ['closed', 'Closed'], ['all', 'All reports']]}>
                <Select aria-label="Reason" className="w-48" value={reason} onValueChange={reset(setReason)} items={Object.fromEntries([['all', 'Any reason'], ...live.data.reasons.map(item => [item.key, item.label])])} />
                <Select aria-label="About" className="w-40" value={target} onValueChange={reset(setTarget)} items={{ all: 'Anyone', user: 'People', bot: 'Bots', group: 'Groups', channel: 'Channels' }} />
            </Toolbar>
            {view.total ? <ul className="divide-y divide-kumo-hairline">
                {view.items.map(group => {
                    let latest = group.reports[0];
                    let preview = latest.comment || (latest.snapshot.messages || [])[0]?.text || '';
                    return <li key={group.key}>
                        <button type="button" className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-kumo-tint" onClick={() => setReviewing(group.key)}>
                            <Avatar user={{ id: group.target.id, name: group.target.name, avatarPhotoId: group.target.avatarPhotoId }} size={36} />
                            <span className="min-w-0 flex-1">
                                <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                                    <span className="truncate text-sm font-medium text-kumo-default">{group.target.name || 'Unknown'}</span>
                                    <Badge variant="outline">{kindLabel[group.target.kind]}</Badge>
                                    {group.target.state && group.target.state !== 'active' ? <Status>{group.target.state}</Status> : null}
                                </span>
                                <span className="mt-0.5 block truncate text-xs text-kumo-subtle">{(group.open ? group.reasons : [latest.reasonLabel]).join(', ')}</span>
                                {preview ? <span className="mt-1 block truncate text-sm text-kumo-default">{preview}</span> : null}
                            </span>
                            <span className="shrink-0 text-right text-xs text-kumo-subtle">
                                <span className="block font-medium text-kumo-default tabular-nums">{group.open ? `${group.open} open` : `${group.reports.length} closed`}</span>
                                <span className="block">{group.reporters > 1 ? `${group.reporters} people · ` : ''}{ago(group.latestAt)}</span>
                            </span>
                        </button>
                    </li>;
                })}
            </ul> : <EmptyState icon={FlagIcon} title={status === 'open' ? 'Nothing to review' : 'No reports here'} description="Reports show up here when someone presses Report in the apps." />}
            <Pager view={view} setPage={setPage} noun="accounts and chats" />
        </Panel>
        {current ? <Review key={current.key} group={current} onClose={() => setReviewing(null)} reload={live.reload} /> : null}
    </>;
}
