import { useState } from 'react';
import { Button, Dialog, Input, Select } from '@cloudflare/kumo';
import { StarIcon, XIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { number } from '../lib/format.js';
import { useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { Row, Rows } from '../components/ui.jsx';

export const starsReviewError = 'Choose a member, a positive whole amount and a reason. The balance cannot become negative.';

export function signedStars(amount) {
    return (amount > 0 ? '+' : '') + number(amount) + ' Stars';
}

// first submit of the dialog: validate locally, nothing is sent yet
export function reviewStarsTransaction({ people, recipientId, direction, amount, note, requestId, limit }) {
    let reviewed = { recipientId: Number(recipientId), amount: Number(amount) * (direction === 'debit' ? -1 : 1), note: String(note ?? '').trim(), requestId };
    let recipient = people.find(user => user.id === reviewed.recipientId);
    if (!recipient || !Number.isSafeInteger(reviewed.amount) || !reviewed.amount || Number(amount) < 1 || Math.abs(reviewed.amount) > limit || recipient.stars + reviewed.amount < 0 || !reviewed.note) throw new Error(starsReviewError);
    return { reviewed, recipient };
}

function Shell({ title, description, onClose, onSubmit, children }) {
    return <Dialog.Root open onOpenChange={value => { if (!value) onClose(); }}>
        <Dialog size="base" className="max-h-[90dvh] overflow-y-auto p-6">
            <form onSubmit={onSubmit} className="flex flex-col gap-4" autoComplete="off" data-1p-ignore>
                <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0 flex-1">
                        {title}
                        {description ? <Dialog.Description className="mt-1 text-sm text-kumo-subtle">{description}</Dialog.Description> : null}
                    </div>
                    <Dialog.Close render={props => <Button {...props} type="button" variant="ghost" shape="square" size="sm" icon={XIcon} aria-label="Close" />} />
                </div>
                {children}
            </form>
        </Dialog>
    </Dialog.Root>;
}

function Emblem({ size }) {
    return <img src="/stars-logo.svg" alt="" width={size} height={size} className="shrink-0" />;
}

export function StarsDialog({ recipientId, onClose }) {
    let { data, refresh } = useAdmin();
    let dialogs = useDialogs();
    let people = data.users.filter(user => !user.disabled);
    let limit = data.settings.starsAdjustmentLimit;
    // one id per dialog so a retry after a dropped response cannot double-credit
    let [requestId] = useState(() => crypto.randomUUID());
    let [form, setForm] = useState(() => ({ recipientId: people.some(user => user.id === recipientId) ? String(recipientId) : null, direction: 'credit', amount: '', note: '' }));
    let [review, setReview] = useState(null);
    let [busy, setBusy] = useState(false);
    let [error, setError] = useState('');
    let set = key => value => setForm(current => ({ ...current, [key]: value }));
    async function submit(event) {
        event.preventDefault();
        if (busy) return;
        setError('');
        if (!review) {
            try { setReview(reviewStarsTransaction({ people, ...form, requestId, limit })); }
            catch (failure) { setError(failure.message); }
            return;
        }
        setBusy(true);
        try {
            let result = await api('/admin/star-transactions', 'POST', review.reviewed);
            await refresh();
            dialogs.open('receipt', { entry: { ...result, note: review.reviewed.note } });
        } catch (failure) {
            setError(failure.message || String(failure));
            setBusy(false);
        }
    }
    let title = <div className="flex items-start gap-3">
        <span className="mt-0.5 inline-flex size-9 shrink-0 items-center justify-center rounded-lg text-kumo-default ring ring-kumo-line"><StarIcon size={18} weight="fill" /></span>
        <Dialog.Title className="pt-1 text-lg font-semibold text-kumo-default">New Stars transaction</Dialog.Title>
    </div>;
    return <Shell title={title} description="Send Stars as Admin or return a previous credit. Every transaction has a permanent receipt." onClose={onClose} onSubmit={submit}>
        {review ? <div className="flex flex-col gap-2">
            <div className="flex flex-col items-center gap-1 rounded-lg bg-kumo-recessed px-4 py-5 text-center">
                <span className="text-2xl font-semibold tabular-nums text-kumo-default">{signedStars(review.reviewed.amount)}</span>
                <span className="text-sm text-kumo-subtle">{review.recipient.name} · @{review.recipient.username}</span>
            </div>
            <Rows>
                <Row label="Source">Admin</Row>
                <Row label="Reason">{review.reviewed.note}</Row>
                <Row label="Resulting balance">{number(review.recipient.stars + review.reviewed.amount)} Stars</Row>
            </Rows>
        </div> : <div className="flex flex-col gap-4">
            <Select label="Recipient" hideLabel={false} className="w-full" placeholder="Choose a member" value={form.recipientId} onValueChange={set('recipientId')} items={people.map(user => ({ value: String(user.id), label: `${user.name} · @${user.username} · ${number(user.stars)} Stars` }))} />
            <Select label="Transaction type" hideLabel={false} className="w-full" value={form.direction} onValueChange={value => set('direction')(value ?? 'credit')} items={{ credit: 'Credit Stars', debit: 'Return Stars' }} />
            <Input label="Amount" className="w-full" type="number" min={1} max={limit} step={1} required placeholder="0" value={form.amount} onChange={event => set('amount')(event.target.value)} description={`Up to ${number(limit)} Stars per transaction.`} passwordManagerIgnore autoComplete="off" />
            <Input label="Reason" className="w-full" maxLength={256} required placeholder="For example, community reward" value={form.note} onChange={event => set('note')(event.target.value)} description="Visible in the transaction receipt." passwordManagerIgnore autoComplete="off" />
        </div>}
        {error ? <p className="text-sm text-kumo-danger" role="alert">{error}</p> : null}
        <div className="flex justify-end gap-2 pt-1">
            <Button type="button" variant="secondary" disabled={busy} onClick={() => review ? setReview(null) : onClose()}>{review ? 'Edit' : 'Cancel'}</Button>
            <Button type="submit" variant="primary" loading={busy}>{review ? 'Confirm transaction' : 'Review transaction'}</Button>
        </div>
    </Shell>;
}

export function StarsReceipt({ entry, onClose }) {
    let { data } = useAdmin();
    let recipient = data.users.find(user => user.id === entry.user_id || user.id === entry.recipientId);
    let amount = Number(entry.amount);
    let title = <div className="flex flex-col items-center gap-2 pt-2 text-center">
        <Emblem size={80} />
        <Dialog.Title className="text-2xl font-semibold tabular-nums text-kumo-default">{signedStars(amount)}</Dialog.Title>
        <p className="text-sm text-kumo-subtle">{entry.title || (amount > 0 ? 'Stars received' : 'Stars returned')}</p>
    </div>;
    return <Shell title={title} onClose={onClose} onSubmit={event => { event.preventDefault(); onClose(); }}>
        <Rows>
            <Row label="Recipient">
                <span className="block">{recipient?.name || entry.name || ''}</span>
                <span className="block text-xs text-kumo-subtle">@{recipient?.username || entry.username || ''}</span>
            </Row>
            <Row label="Source">Admin</Row>
            <Row label="Transaction ID">{entry.transactionId ? <code className="font-mono text-xs break-all">{entry.transactionId}</code> : 'Legacy transaction #' + entry.id}</Row>
            {entry.created_at ? <Row label="Date">{new Date(entry.created_at * 1000).toLocaleString()}</Row> : null}
            {entry.note ? <Row label="Reason">{entry.note}</Row> : null}
            {entry.balance !== undefined ? <Row label="New balance">{number(entry.balance)} Stars</Row> : null}
        </Rows>
        <div className="flex justify-end pt-1">
            <Button type="submit" variant="primary">Done</Button>
        </div>
    </Shell>;
}
