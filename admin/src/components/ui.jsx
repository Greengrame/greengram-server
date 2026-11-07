import { useState } from 'react';
import { Badge, Button, Dialog, Empty as KumoEmpty, Input, LayerCard, Loader, Pagination, Select, Text, cn } from '@cloudflare/kumo';
import { MagnifyingGlassIcon, SealCheckIcon, XIcon } from '@phosphor-icons/react';
import { accountSlot } from '../lib/api.js';
import { initials, number } from '../lib/format.js';

// telegram's peer colours, picked by id like the messenger does
const avatarColors = ['#65aadd', '#a695e7', '#7bc862', '#eea75a', '#ee7aae', '#6ec9cb'];

export function Avatar({ user, size = 32, className }) {
    let style = { width: size, height: size, fontSize: Math.round(size * 0.38) };
    if (user?.avatarPhotoId) return <img className={cn('shrink-0 rounded-full object-cover', className)} style={style} src={`/api/admin/photos/${encodeURIComponent(user.avatarPhotoId)}?account=${accountSlot()}`} alt="" />;
    return <span aria-hidden="true" className={cn('inline-flex shrink-0 items-center justify-center rounded-full font-medium text-white', className)} style={{ ...style, background: avatarColors[Math.abs(Number(user?.id) || 0) % avatarColors.length] }}>{initials(user?.name)}</span>;
}

export function Verified() {
    return <SealCheckIcon weight="fill" size={16} className="ml-1 inline shrink-0 align-[-2px] text-kumo-default" aria-label="Verified" role="img" />;
}

export function Person({ user, onOpen, suffix, size = 32 }) {
    let body = <>
        <Avatar user={user} size={size} />
        <span className="min-w-0 text-left">
            <span className="flex items-center truncate text-sm font-medium text-kumo-default">{user.name}{user.isVerified ? <Verified /> : null}{suffix}</span>
            {user.username ? <span className="block truncate text-xs text-kumo-subtle">@{user.username}</span> : null}
        </span>
    </>;
    if (!onOpen) return <span className="flex min-w-0 items-center gap-3">{body}</span>;
    return <button type="button" className="group flex min-w-0 items-center gap-3 rounded-md [&:hover_.font-medium]:underline" onClick={onOpen}>{body}</button>;
}

// a titled card; flush drops the body padding for tables and lists
export function Panel({ title, description, actions, children, flush = false, className }) {
    return <LayerCard className={cn('min-w-0', className)}>
        {title || actions ? <LayerCard.Secondary className="flex min-h-12 items-center justify-between gap-3">
            <div className="min-w-0">
                {title ? <h2 className="text-base font-medium text-kumo-default">{title}</h2> : null}
                {description ? <p className="text-xs font-normal text-kumo-subtle">{description}</p> : null}
            </div>
            {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
        </LayerCard.Secondary> : null}
        <LayerCard.Primary className={flush ? 'overflow-x-auto p-0' : ''}>{children}</LayerCard.Primary>
    </LayerCard>;
}

export function Toolbar({ query, onQuery, placeholder, filter, onFilter, filters, children }) {
    return <div className="flex flex-wrap items-center gap-2 border-b border-kumo-hairline px-4 py-3">
        <div className="relative min-w-48 flex-1 sm:max-w-80">
            <MagnifyingGlassIcon size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-kumo-subtle" />
            <Input aria-label={placeholder} placeholder={placeholder} value={query} onChange={event => onQuery(event.target.value)} className="w-full pl-9" passwordManagerIgnore />
        </div>
        {filters ? <Select aria-label="Filter" className="w-52" value={filter} onValueChange={value => onFilter(value ?? 'all')} items={Object.fromEntries(filters)} /> : null}
        <div className="ml-auto flex items-center gap-2">{children}</div>
    </div>;
}

export function Pager({ view, setPage, noun }) {
    return <div className="flex flex-wrap items-center justify-between gap-3 border-t border-kumo-hairline px-4 py-3">
        <Text variant="secondary" size="sm">{view.from}–{view.to} of {number(view.total)}{noun ? ' ' + noun : ''}</Text>
        {view.pages > 1 ? <Pagination page={view.page} setPage={setPage} perPage={Math.max(1, Math.ceil(view.total / view.pages))} totalCount={view.total} controls="simple" /> : null}
    </div>;
}

export function EmptyState({ icon: Icon, title, description }) {
    return <KumoEmpty size="sm" className="py-10" icon={Icon ? <Icon size={32} className="text-kumo-inactive" /> : null} title={title} description={description} />;
}

export function Loading({ error, label = 'Loading…' }) {
    if (error) return <div className="px-4 py-10 text-center text-sm text-kumo-danger" role="alert">{error.message || String(error)}</div>;
    return <div className="flex items-center justify-center gap-2 px-4 py-10 text-sm text-kumo-subtle" role="status"><Loader size="sm" />{label}</div>;
}

const tones = { Admin: 'blue', Active: 'success', Online: 'success', Enabled: 'success', Published: 'success', 'For Sale': 'blue', Disabled: 'secondary', Unlisted: 'secondary', Expired: 'secondary', Free: 'secondary', 'Expiring Soon': 'warning', Scheduled: 'outline', Unlimited: 'blue', frozen: 'outline', locked: 'destructive', banned: 'destructive', suspended: 'warning', deleted: 'secondary' };

export function Status({ children, variant }) {
    return <Badge variant={variant || tones[children] || 'outline'}>{children}</Badge>;
}

// one dialog shell for every confirm/form flow: busy state, inline error, cancel
export function ActionDialog({ open, onClose, title, description, icon: Icon, children, confirmLabel = 'Confirm', destructive = false, onConfirm, size = 'base', hideActions = false, confirmDisabled = false, cancelLabel = 'Cancel' }) {
    let [busy, setBusy] = useState(false);
    let [error, setError] = useState('');
    async function submit(event) {
        event?.preventDefault();
        if (busy || !onConfirm) return;
        setBusy(true);
        setError('');
        try { await onConfirm(); }
        catch (failure) { setError(failure.message || String(failure)); }
        finally { setBusy(false); }
    }
    return <Dialog.Root open={open} onOpenChange={value => { if (!value) { setError(''); onClose(); } }}>
        <Dialog size={size} className="max-h-[90dvh] overflow-y-auto p-6">
            <form onSubmit={submit} className="flex flex-col gap-4" autoComplete="off" data-1p-ignore>
                <div className="flex items-start justify-between gap-4">
                    <div className="flex min-w-0 items-start gap-3">
                        {Icon ? <span className={cn('mt-0.5 inline-flex size-9 shrink-0 items-center justify-center rounded-lg ring ring-kumo-line', destructive ? 'text-kumo-danger' : 'text-kumo-default')}><Icon size={18} /></span> : null}
                        <div className="min-w-0">
                            <Dialog.Title className="text-lg font-semibold text-kumo-default">{title}</Dialog.Title>
                            {description ? <Dialog.Description className="mt-1 text-sm text-kumo-subtle">{description}</Dialog.Description> : null}
                        </div>
                    </div>
                    <Dialog.Close render={props => <Button {...props} type="button" variant="ghost" shape="square" size="sm" icon={XIcon} aria-label="Close" />} />
                </div>
                {children}
                {error ? <p className="text-sm text-kumo-danger" role="alert">{error}</p> : null}
                {hideActions ? null : <div className="flex justify-end gap-2 pt-1">
                    <Button type="button" variant="secondary" onClick={onClose}>{cancelLabel}</Button>
                    {onConfirm ? <Button type="submit" variant={destructive ? 'destructive' : 'primary'} loading={busy} disabled={confirmDisabled}>{confirmLabel}</Button> : null}
                </div>}
            </form>
        </Dialog>
    </Dialog.Root>;
}

export function Kpi({ label, value, note }) {
    return <div className="flex min-w-0 flex-col gap-1 px-5 py-4">
        <span className="text-xs text-kumo-subtle">{label}</span>
        <span className="text-2xl font-semibold tabular-nums text-kumo-default">{value}</span>
        <span className="min-h-4 text-xs text-kumo-subtle">{note}</span>
    </div>;
}

export function KpiStrip({ children }) {
    return <LayerCard><LayerCard.Primary className="p-0"><div className="grid grid-cols-2 gap-px bg-kumo-hairline sm:grid-cols-3 xl:grid-flow-col xl:auto-cols-fr xl:grid-cols-none [&>*]:bg-kumo-base">{children}</div></LayerCard.Primary></LayerCard>;
}

export function Change({ value }) {
    if (value === null) return <span className="font-medium text-kumo-default">new</span>;
    return <span className={cn('font-medium tabular-nums', value > 0 ? 'text-kumo-success' : value < 0 ? 'text-kumo-danger' : 'text-kumo-subtle')}>{value > 0 ? '+' : ''}{value}%</span>;
}

export function Rows({ children }) {
    return <dl className="divide-y divide-kumo-hairline">{children}</dl>;
}

export function Row({ label, children }) {
    return <div className="flex items-start justify-between gap-6 py-3 text-sm"><dt className="shrink-0 text-kumo-subtle">{label}</dt><dd className="min-w-0 text-right break-words text-kumo-default">{children}</dd></div>;
}
