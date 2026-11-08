import { useEffect, useRef, useState } from 'react';
import { Button, Checkbox, Dialog, Input, InputArea, Pagination, Select, Switch, Tabs, Text } from '@cloudflare/kumo';
import { ChatCircleIcon, CrownIcon, DevicesIcon, StarIcon, UsersIcon, XIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { date, deviceInfo, number, paged, premiumExpiry } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { Avatar, Loading, Row, Rows } from '../components/ui.jsx';

const tabs = [{ value: 'profile', label: 'Profile' }, { value: 'access', label: 'Access' }, { value: 'sessions', label: 'Devices' }, { value: 'crypto', label: 'Crypto' }, { value: 'memberships', label: 'Chats' }, { value: 'privacy', label: 'Privacy' }];
const accessLabels = { active: 'Restore Access', frozen: 'Freeze Account', locked: 'Lock Account', suspended: 'Suspend Account', banned: 'Ban Account', deleted: 'Delete Account' };
const accessDescriptions = { active: 'Restore sign-in and allow changes again.', frozen: 'Keep existing content readable, but prevent changes and purchases.', suspended: 'Temporarily hide this account and its message contents while preserving its data for restoration. All devices are signed out.', locked: 'Sign out all devices and block sign-in until unlocked.', banned: 'Sign out all devices and ban access until an administrator lifts the ban.', deleted: 'Sign out every device, remove the profile photos, name, username, phone and password, and leave every group and channel (owned ones pass to their longest-serving admin). Owned bots stop. Fragment names and numbers go back to the instance. Stars, wallet balances and collectible gifts move to the Deleted Accounts system account. Messages stay and show as Deleted Account. This cannot be undone.' };
const sessionId = /^[a-f0-9]{64}$/;
const pageSize = 8;

export function accountState(user) {
    return user.accountState || (user.disabled ? 'locked' : 'active');
}

// 'all' or one 64-hex session id, and never the session this admin is using
export function canTerminate(selected, sessions = []) {
    return Boolean(selected) && (selected === 'all' || sessionId.test(selected)) && !sessions.some(session => session.id === selected && session.isCurrent);
}

export function membershipTitle(chat) {
    return chat.title?.trim() || (chat.username ? '@' + chat.username : 'Chat #' + chat.id);
}

export function membershipKind(chat) {
    return chat.isChannel ? 'Channel' : chat.kind === 'saved' ? 'Saved messages' : chat.kind === 'direct' ? 'Direct message' : chat.kind;
}

export function privacyLabel(key) {
    let label = key.replace(/([A-Z])/g, ' $1').replaceAll('_', ' ');
    return label.charAt(0).toUpperCase() + label.slice(1);
}

function Heading({ children }) {
    return <h3 className="text-sm font-semibold text-kumo-default">{children}</h3>;
}

function Muted({ children, role }) {
    return <p className="text-sm text-kumo-subtle" role={role}>{children}</p>;
}

function Failure({ children }) {
    return children ? <p className="text-sm text-kumo-danger" role="alert">{children}</p> : null;
}

function Line({ title, note, children }) {
    return <div className="flex items-center justify-between gap-4 py-2.5">
        <span className="min-w-0">
            <span className="block truncate text-sm font-medium text-kumo-default">{title}</span>
            {note ? <small className="block truncate text-xs text-kumo-subtle">{note}</small> : null}
        </span>
        {children}
    </div>;
}

function List({ children, empty }) {
    let items = [].concat(children).filter(Boolean);
    return items.length ? <div className="divide-y divide-kumo-hairline">{items}</div> : <Muted>{empty}</Muted>;
}

// ui Pager is padded for table panels; this one sits flush with the dialog content
function Section({ view, setPage, children }) {
    return <div className="flex flex-col">
        {children}
        <div className="mt-2 flex flex-wrap items-center justify-between gap-3 border-t border-kumo-hairline pt-3">
            <Text variant="secondary" size="sm">{view.from}–{view.to} of {number(view.total)}</Text>
            {view.pages > 1 ? <Pagination page={view.page} setPage={setPage} perPage={pageSize} totalCount={view.total} controls="simple" /> : null}
        </div>
    </div>;
}

function Benefit({ icon: Icon, title, note, action, onAction }) {
    return <div className="flex items-center gap-3 rounded-lg px-3 py-3 ring ring-kumo-line">
        <span className="inline-flex size-9 shrink-0 items-center justify-center rounded-lg bg-kumo-recessed text-kumo-default"><Icon size={18} /></span>
        <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium text-kumo-default">{title}</span>
            <small className="block truncate text-xs text-kumo-subtle">{note}</small>
        </span>
        <Button size="sm" variant="secondary" onClick={onAction}>{action}</Button>
    </div>;
}

// the read-only identity lines on the Profile tab
export function identityLines(user, detail) {
    let lines = [['Phone', user.phoneNumber || detail.user?.phoneNumber || 'No phone number']];
    for (let item of detail.fragment || []) lines.push([item.kind === 'number' ? 'Fragment number' : 'Fragment username', (item.displayTitle || item.title) + (item.listed ? ` · for sale, ${item.price} GRAM` : '')]);
    if (detail.deletedAt) lines.push(['Deleted', `${date(detail.deletedAt)} · ${{ admin: 'by an administrator', self: 'by its owner', inactive: 'after inactivity' }[detail.deletionSource] || detail.deletionSource || ''}`]);
    return lines;
}

function Identity({ id, user, detail, onClose }) {
    return <div className="flex flex-col gap-2 rounded-lg px-3 py-3 ring ring-kumo-line">
        <Rows>{identityLines(user, detail).map(([label, value], index) => <Row key={index} label={label}><span className="font-mono text-sm">{value}</span></Row>)}</Rows>
        <div className="flex justify-end"><Button type="button" size="sm" variant="secondary" onClick={() => { onClose(); location.hash = 'circulation/user/' + id; }}>View holdings</Button></div>
    </div>;
}

// what Reset Info can wipe, in the server's order (account.ResetFields)
export const resetFields = [
    ['phone', 'Phone number', 'A made-up +888 number replaces it and every number it could sign in with before.'],
    ['name', 'Name', `Becomes "User" and the account id.`],
    ['bio', 'Bio', 'Emptied.'],
    ['username', 'Username', 'Free for anyone to take. A hidden placeholder stays, good for username sign-in.'],
    ['photos', 'Profile photos', 'Every profile photo is removed.'],
    ['birthday', 'Birthday', 'Removed.'],
    ['channel', 'Profile channel', 'The channel shown on the profile is unlinked.']
];

// the picked fields in list order, unknown ones dropped
export function resetPicked(picked = []) {
    return resetFields.filter(([key]) => picked.includes(key)).map(([key]) => key);
}

export function resetSummary(picked = []) {
    return resetFields.filter(([key]) => picked.includes(key)).map(([, label]) => label);
}

function ResetInfo({ id, user, onDetail, refresh }) {
    let [picked, setPicked] = useState([]);
    let [confirming, setConfirming] = useState(false);
    let [confirmation, setConfirmation] = useState('');
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    let fields = resetPicked(picked);
    async function submit(event) {
        event.preventDefault();
        setBusy(true);
        setError('');
        try {
            let result = await api('/admin/users/' + id + '/reset', 'POST', { fields, confirmation });
            onDetail(result);
            await refresh();
            toast(result.reset?.newPhone ? `Account info reset. New sign-in number: ${result.reset.newPhone}` : 'Account info reset.', 'success');
        } catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    return <section className="flex flex-col gap-3 rounded-lg px-3 py-3 ring ring-kumo-line">
        <div>
            <Heading>Reset Info</Heading>
            <Muted>Wipe personal details and keep the account. It stays signed in with the same password.</Muted>
        </div>
        <Checkbox.Group value={picked} onValueChange={value => { setPicked(value); setConfirming(false); setError(''); }} disabled={busy}>
            <Checkbox.Legend className="sr-only">Fields to reset</Checkbox.Legend>
            {resetFields.map(([key, label, note]) => <div key={key} className="flex flex-col">
                <Checkbox.Item value={key} label={label} />
                <small className="pl-6 text-xs text-kumo-subtle">{note}</small>
            </div>)}
        </Checkbox.Group>
        {confirming ? <form className="flex flex-col gap-3 border-t border-kumo-hairline pt-3" onSubmit={submit} autoComplete="off" data-1p-ignore>
            <p className="text-sm text-kumo-default">This resets: <strong>{resetSummary(fields).join(', ')}</strong>. The old values are gone for good.</p>
            <Input label={`Type ${user.username} to confirm`} value={confirmation} onChange={event => setConfirmation(event.target.value)} required autoComplete="off" passwordManagerIgnore className="w-full" />
            <Failure>{error}</Failure>
            <div className="flex justify-end gap-2">
                <Button type="button" size="sm" variant="secondary" onClick={() => { setConfirming(false); setConfirmation(''); setError(''); }}>Cancel</Button>
                <Button type="submit" size="sm" variant="destructive" loading={busy} disabled={confirmation !== user.username}>Reset {fields.length === 1 ? '1 Field' : fields.length + ' Fields'}</Button>
            </div>
        </form> : <div className="flex justify-end">
            <Button type="button" size="sm" variant="secondary-destructive" disabled={!fields.length} onClick={() => setConfirming(true)}>Reset Info</Button>
        </div>}
    </section>;
}

function ProfileTab({ id, user, me, detail, onDetail, refresh, onClose }) {
    let [values, setValues] = useState({ name: user.name || '', username: user.username || '', bio: user.bio || '', usernamePublic: user.usernamePublic !== false });
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    let [removing, setRemoving] = useState(false);
    let set = key => event => setValues(current => ({ ...current, [key]: event.target.value }));
    async function save(event) {
        let payload = { name: values.name.trim(), username: values.username.trim(), bio: values.bio, usernamePublic: values.usernamePublic };
        event.preventDefault();
        setBusy(true);
        setError('');
        try {
            onDetail(await api('/admin/users/' + id, 'PATCH', payload));
            await refresh();
            toast('Account profile updated.', 'success');
        } catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    async function removePhoto() {
        setRemoving(true);
        setError('');
        try {
            onDetail(await api('/admin/users/' + id, 'PATCH', { removePhoto: true }));
            await refresh();
            toast('Profile photo removed.', 'success');
        } catch (failure) { setError(failure.message); }
        finally { setRemoving(false); }
    }
    return <div className="flex flex-col gap-4">
        <div className="grid grid-cols-3 gap-2">
            {[[ChatCircleIcon, 'Messages', detail.messageCount], [UsersIcon, 'Chats', detail.chatCount], [DevicesIcon, 'Devices', detail.sessions?.length]].map(([Icon, label, count]) => <div key={label} className="flex flex-col gap-1 rounded-lg px-3 py-2.5 ring ring-kumo-line">
                <span className="flex items-center gap-1.5 text-xs text-kumo-subtle"><Icon size={14} />{label}</span>
                <span className="text-lg font-semibold tabular-nums text-kumo-default">{number(count)}</span>
            </div>)}
        </div>
        <Identity id={id} user={user} detail={detail} onClose={onClose} />
        {accountState(user) === 'deleted' ? null : <form className="flex flex-col gap-4" onSubmit={save} autoComplete="off" data-1p-ignore>
            <Input label="Name" value={values.name} onChange={set('name')} maxLength={80} required autoComplete="off" passwordManagerIgnore className="w-full" />
            <Input label="Username" value={values.username} onChange={set('username')} maxLength={32} required autoComplete="off" passwordManagerIgnore className="w-full" />
            <InputArea label="Bio" value={values.bio} onChange={set('bio')} maxLength={user.isPremium ? 140 : 70} rows={3} autoComplete="off" data-1p-ignore className="w-full" />
            <div className="flex items-start justify-between gap-4 rounded-lg px-3 py-3 ring ring-kumo-line">
                <span className="min-w-0">
                    <span className="block text-sm font-medium text-kumo-default">Public Username</span>
                    <small className="block text-xs text-kumo-subtle">Show the username on this account's profile.</small>
                </span>
                <Switch aria-label="Public Username" checked={values.usernamePublic} onCheckedChange={checked => setValues(current => ({ ...current, usernamePublic: checked }))} />
            </div>
            {user.avatarPhotoId ? <div className="flex items-center justify-between gap-4 rounded-lg px-3 py-3 ring ring-kumo-line">
                <span className="min-w-0">
                    <span className="block text-sm font-medium text-kumo-default">Profile Photo</span>
                    <small className="block text-xs text-kumo-subtle">Use the default avatar for this account.</small>
                </span>
                <Button type="button" size="sm" variant="secondary" loading={removing} onClick={removePhoto}>Remove Profile Photo</Button>
            </div> : null}
            <Failure>{error}</Failure>
            <div className="flex justify-end"><Button type="submit" variant="primary" loading={busy}>Save Profile</Button></div>
        </form>}
        {accountState(user) === 'deleted' || user.role === 'admin' || user.id === me?.id ? null : <ResetInfo id={id} user={user} onDetail={onDetail} refresh={refresh} />}
    </div>;
}

function AccessForm({ id, user, pending, onCancel, onDone }) {
    let [reason, setReason] = useState('');
    let [confirmation, setConfirmation] = useState('');
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    async function submit(event) {
        event.preventDefault();
        setBusy(true);
        setError('');
        try {
            await api('/admin/users/' + id + '/access', 'PATCH', { state: pending, reason, confirmation: pending === 'deleted' ? confirmation : null });
            await onDone();
        } catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    return <form className="flex flex-col gap-3 rounded-lg px-3 py-3 ring ring-kumo-line" onSubmit={submit} autoComplete="off" data-1p-ignore>
        <Heading>{accessLabels[pending]}</Heading>
        <p className="text-sm text-kumo-default">{accessDescriptions[pending]}</p>
        <Input label="Reason" value={reason} onChange={event => setReason(event.target.value)} maxLength={500} autoComplete="off" passwordManagerIgnore className="w-full" />
        {pending === 'deleted' ? <Input label={`Type ${user.username} to confirm`} value={confirmation} onChange={event => setConfirmation(event.target.value)} required autoComplete="off" passwordManagerIgnore className="w-full" /> : null}
        <Failure>{error}</Failure>
        <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="secondary" onClick={onCancel}>Cancel</Button>
            <Button type="submit" size="sm" variant={pending === 'deleted' ? 'destructive' : 'primary'} loading={busy}>Confirm</Button>
        </div>
    </form>;
}

function AccountAccess({ id, user, me, pending, setPending, onDetail, refresh }) {
    let current = accountState(user);
    if (user.role === 'admin' || user.id === me.id) return null;
    if (current === 'deleted') return <Muted>This account was deleted and cannot be restored.</Muted>;
    return <section className="flex flex-col gap-3">
        <div>
            <Heading>Account Access</Heading>
            <Muted>Current status: {current}</Muted>
        </div>
        <div className="flex flex-wrap gap-2">
            {['active', 'frozen', 'locked', 'suspended', 'banned', 'deleted'].filter(value => value !== current).map(value => <Button key={value} size="sm" variant={value === 'deleted' || value === 'banned' ? 'secondary-destructive' : 'secondary'} aria-pressed={pending === value} onClick={() => setPending(value)}>{accessLabels[value]}</Button>)}
        </div>
        {pending ? <AccessForm key={pending} id={id} user={user} pending={pending} onCancel={() => setPending(null)} onDone={async () => {
            onDetail(await api('/admin/users/' + id));
            setPending(null);
            await refresh();
            toast('Account access updated.', 'success');
        }} /> : null}
    </section>;
}

// the same alphabet everywhere: no lookalikes, it gets read out to someone
function randomPassword() {
    let alphabet = 'abcdefghjkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ2345679';
    return Array.from(crypto.getRandomValues(new Uint32Array(16)), value => alphabet[value % alphabet.length]).join('');
}

function PasswordReset({ id, user, onDetail, refresh }) {
    let [open, setOpen] = useState(false);
    let [value, setValue] = useState('');
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    let [done, setDone] = useState('');
    async function submit(event) {
        event.preventDefault();
        setBusy(true);
        setError('');
        try {
            await api('/admin/users/' + id + '/password', 'POST', { password: value });
            setDone(value);
            setValue('');
            setOpen(false);
            onDetail(await api('/admin/users/' + id));
            await refresh();
            toast('Password reset. All devices were signed out.', 'success');
        } catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    return <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-4">
            <span className="min-w-0">
                <Heading>Password</Heading>
                <Muted>Set a new sign-in password for this account.</Muted>
            </span>
            {open ? null : <Button size="sm" variant="secondary" className="shrink-0" onClick={() => { setOpen(true); setDone(''); setValue(randomPassword()); }}>Reset Password</Button>}
        </div>
        {done ? <p className="rounded-lg bg-kumo-recessed px-3 py-2 text-sm text-kumo-default">New password for @{user.username}: <code className="select-all font-mono">{done}</code>. It is not shown again.</p> : null}
        {open ? <form className="flex flex-col gap-3 rounded-lg px-3 py-3 ring ring-kumo-line" onSubmit={submit} autoComplete="off" data-1p-ignore>
            <Input label="New password" value={value} onChange={event => setValue(event.target.value)} minLength={10} maxLength={256} required autoComplete="off" passwordManagerIgnore className="w-full font-mono" description="10 to 256 characters. A random one is filled in, or type your own." />
            <p className="text-sm text-kumo-default">Every device on this account is signed out right away.</p>
            <Failure>{error}</Failure>
            <div className="flex justify-end gap-2">
                <Button type="button" size="sm" variant="secondary" onClick={() => { setOpen(false); setError(''); setValue(''); }}>Cancel</Button>
                <Button type="button" size="sm" variant="secondary" onClick={() => setValue(randomPassword())}>Generate</Button>
                <Button type="submit" size="sm" variant="primary" loading={busy}>Reset Password</Button>
            </div>
        </form> : null}
    </section>;
}

function AccessTab({ id, user, me, detail, section, accessState, setAccessState, onDetail, refresh }) {
    let dialogs = useDialogs();
    let ledger = section('ledger', detail.ledger || []);
    let gone = accountState(user) === 'deleted';
    return <div className="flex flex-col gap-4">
        {gone ? null : <>
            <Benefit icon={CrownIcon} title={user.isPremium ? 'Premium member' : 'Free plan'} note={user.isPremium ? premiumExpiry(user, 'Expires ') : 'Local Premium access'} action="Manage" onAction={() => dialogs.open('premium', { ids: [id] })} />
            <Benefit icon={StarIcon} title={`${number(user.stars)} Stars`} note="Create a traceable Stars transaction" action="Transact" onAction={() => dialogs.open('stars', { recipientId: id })} />
            {user.id !== me.id ? <div><Button size="sm" variant="secondary" onClick={() => dialogs.open('role', { id })}>{user.role === 'admin' ? 'Remove Admin' : 'Make Admin'}</Button></div> : null}
        </>}
        <AccountAccess id={id} user={user} me={me} pending={accessState} setPending={setAccessState} onDetail={onDetail} refresh={refresh} />
        {user.role !== 'admin' && user.id !== me.id && accountState(user) !== 'deleted' ? <PasswordReset id={id} user={user} onDetail={onDetail} refresh={refresh} /> : null}
        <Section view={ledger.view} setPage={ledger.setPage}>
            <Heading>Stars History</Heading>
            <List empty="No Stars transactions yet.">{ledger.view.items.map((entry, index) => <Line key={entry.id ?? index} title={String(entry.reason || '').replaceAll('_', ' ')} note={date(entry.created_at)}>
                <strong className={'shrink-0 text-sm font-semibold tabular-nums ' + (entry.amount > 0 ? 'text-kumo-success' : 'text-kumo-danger')}>{entry.amount > 0 ? '+' : ''}{number(entry.amount)}</strong>
            </Line>)}</List>
        </Section>
    </div>;
}

function SessionsTab({ id, user, me, detail, section, confirmation, setConfirmation, onSessions, refresh }) {
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    let sessions = detail.sessions || [];
    let feed = section('sessions', sessions);
    async function terminate() {
        let selected = confirmation;
        if (!canTerminate(selected, detail.sessions)) return;
        setBusy(true);
        setError('');
        try {
            let result = await api('/admin/users/' + id + '/sessions' + (selected === 'all' ? '' : '/' + selected), 'DELETE', {});
            onSessions(result.sessions || []);
            setConfirmation(null);
            await refresh();
            toast('Sessions terminated.', 'success');
        } catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    return <div className="flex flex-col gap-4">
        {confirmation ? <div className="flex flex-col gap-3 rounded-lg px-3 py-3 ring ring-kumo-line">
            <p className="text-sm text-kumo-default">{confirmation === 'all' ? 'Terminate all sessions except your current administrator session?' : 'Terminate this session? The device will need to sign in again.'}</p>
            <Failure>{error}</Failure>
            <div className="flex justify-end gap-2">
                <Button size="sm" variant="secondary" onClick={() => { setError(''); setConfirmation(null); }}>Cancel</Button>
                <Button size="sm" variant="destructive" loading={busy} onClick={terminate}>Terminate</Button>
            </div>
        </div> : <div className="flex flex-col items-start gap-1">
            <Button size="sm" variant="secondary-destructive" disabled={!sessions.some(session => !session.isCurrent)} onClick={() => setConfirmation('all')}>Terminate {user.id === me.id ? 'Other' : 'All'} Sessions</Button>
            <small className="text-xs text-kumo-subtle">The current administrator session is always protected.</small>
        </div>}
        <Section view={feed.view} setPage={feed.setPage}>
            <List empty="No active sessions.">{feed.view.items.map(session => <Line key={session.id} title={<span title={session.userAgent}>{session.userAgent ? deviceInfo(session.userAgent).name : 'Unknown browser'}{session.isCurrent ? <span className="ml-2 text-xs font-normal text-kumo-subtle">Current Session</span> : null}</span>} note={`Active ${date(session.lastActive)} · Expires ${date(session.expiresAt)}${session.userAgent ? ' · ' + session.userAgent : ''}`}>
                {session.isCurrent ? null : <Button size="sm" variant="secondary-destructive" onClick={() => setConfirmation(session.id)}>Terminate</Button>}
            </Line>)}</List>
        </Section>
    </div>;
}

function CryptoForm({ id, accounts, request, onCreated }) {
    let [asset, setAsset] = useState(accounts[0]?.currency || '');
    let [direction, setDirection] = useState('credit');
    let [amount, setAmount] = useState('');
    let [note, setNote] = useState('');
    let [error, setError] = useState('');
    let [busy, setBusy] = useState(false);
    async function submit(event) {
        let payload = { recipientId: id, asset: String(asset || ''), direction: String(direction || ''), amount: amount.trim(), note: note.trim() };
        let signature = JSON.stringify(payload);
        event.preventDefault();
        // same payload = same request id, so a retry after a dropped response can't double-post
        if (!request.current || request.current.signature !== signature) request.current = { signature, requestId: crypto.randomUUID() };
        payload.requestId = request.current.requestId;
        setBusy(true);
        setError('');
        try { await onCreated(payload); }
        catch (failure) { setError(failure.message); }
        finally { setBusy(false); }
    }
    return <form className="flex flex-col gap-3" onSubmit={submit} autoComplete="off" data-1p-ignore>
        <div className="grid grid-cols-2 gap-3">
            <Select label="Asset" hideLabel={false} value={asset} onValueChange={value => setAsset(value ?? '')} items={Object.fromEntries(accounts.map(account => [account.currency, `${account.name} (${account.currency})`]))} />
            <Select label="Action" hideLabel={false} value={direction} onValueChange={value => setDirection(value ?? 'credit')} items={{ credit: 'Add crypto', debit: 'Remove crypto' }} />
        </div>
        <Input label="Amount" type="text" inputMode="decimal" pattern="[0-9]+([.][0-9]+)?" required placeholder="0.00" value={amount} onChange={event => setAmount(event.target.value)} autoComplete="off" passwordManagerIgnore className="w-full" />
        <Input label="Reason" maxLength={300} value={note} onChange={event => setNote(event.target.value)} autoComplete="off" passwordManagerIgnore className="w-full" />
        <Failure>{error}</Failure>
        <div className="flex justify-end"><Button type="submit" variant="primary" loading={busy}>Create Transaction</Button></div>
    </form>;
}

function CryptoTab({ id, wallet, setWallet, section, request }) {
    let [formKey, setFormKey] = useState(0);
    if (!wallet.data) return <Loading error={wallet.error} label="Loading crypto balances…" />;
    let data = wallet.data;
    let history = section('crypto', data.history || []);
    return <div className="flex flex-col gap-4">
        <div>
            <Heading>Crypto Balances</Heading>
            <List empty="No crypto accounts.">{(data.accounts || []).map(account => <Line key={account.currency} title={`${account.name} (${account.currency})`} note={account.rate ? '$' + account.rate + ' · ' + (data.market?.source || '') + (account.market_stale ? ' · Stale' : '') : 'Market price unavailable'}>
                <strong className="shrink-0 font-mono text-sm text-kumo-default">{account.available_balance}</strong>
            </Line>)}</List>
            {data.market?.updatedAt ? <Muted>Market data updated {date(data.market.updatedAt)}</Muted> : null}
        </div>
        <CryptoForm key={formKey} id={id} accounts={data.accounts || []} request={request} onCreated={async payload => {
            let receipt = await api('/admin/crypto-transactions', 'POST', payload);
            setWallet(current => ({ ...current, receipt }));
            let next = await api('/admin/users/' + id + '/crypto');
            request.current = null;
            setWallet(current => ({ ...current, data: next, error: null }));
            setFormKey(key => key + 1);
            toast('Crypto transaction created.', 'success');
        }} />
        {wallet.receipt ? <p className="rounded-lg bg-kumo-recessed px-3 py-2 font-mono text-xs text-kumo-default" role="status">Transaction {wallet.receipt.transactionId} · Balance {wallet.receipt.balance} {wallet.receipt.asset}</p> : null}
        <Section view={history.view} setPage={history.setPage}>
            <Heading>Crypto History</Heading>
            <List empty="No crypto transactions yet.">{history.view.items.map((entry, index) => <Line key={entry.id ?? index} title={entry.kind || entry.type || 'Transaction'} note={date(entry.created_at || entry.createdAt)}>
                <strong className="shrink-0 font-mono text-sm text-kumo-default">{entry.amount || entry.units || ''} {entry.asset || entry.currency || ''}</strong>
            </Line>)}</List>
        </Section>
    </div>;
}

function ChatsTab({ detail, section }) {
    let feed = section('memberships', detail.memberships || []);
    return <Section view={feed.view} setPage={feed.setPage}>
        <List empty="No conversations.">{feed.view.items.map(chat => <Line key={chat.id} title={membershipTitle(chat)} note={`${membershipKind(chat)} · ${chat.role}${chat.username ? ' · @' + chat.username : ''}`} />)}</List>
    </Section>;
}

function PrivacyTab({ detail }) {
    return <Rows>
        <Row label="Profile Photos">{number(detail.photoCount)}</Row>
        <Row label="Delete if away for">{number(detail.accountTtlDays)} days</Row>
        <Row label="Last seen">{date(detail.lastSeenAt)}</Row>
        {Object.entries(detail.privacy || {}).map(([key, value]) => <Row key={key} label={privacyLabel(key)}>
            {value.visibility || 'Default'}
            <small className="block text-xs text-kumo-subtle">{number(value.allowedPeople)} people allowed · {number(value.blockedPeople)} blocked · {number(value.allowedChats)} chats allowed · {number(value.blockedChats)} blocked</small>
        </Row>)}
    </Rows>;
}

export default function MemberDialog({ id, onClose }) {
    let { data, user: me, refresh } = useAdmin();
    let [detail, setDetail] = useState(null);
    let [revision, setRevision] = useState(0);
    let [tab, setTab] = useState('profile');
    let [pages, setPages] = useState({ sessions: 1, memberships: 1, ledger: 1, crypto: 1 });
    let [confirmation, setConfirmation] = useState(null);
    let [accessState, setAccessState] = useState(null);
    let [wallet, setWallet] = useState({ data: null, error: null, receipt: null });
    let cryptoRequest = useRef(null);

    useEffect(() => {
        let live = true;
        api('/admin/users/' + id)
            .then(result => { if (live) replaceDetail(result); })
            .catch(error => { if (live) { toastError(error); onClose(); } });
        return () => { live = false; };
    }, [id]);

    function replaceDetail(next) {
        setDetail(next);
        setRevision(value => value + 1);
    }

    function section(key, records) {
        let view = paged(records, pages[key] || 1, pageSize);
        return { view, setPage: page => setPages(current => ({ ...current, [key]: page })) };
    }

    async function openTab(next) {
        setTab(next);
        setConfirmation(null);
        if (next !== 'crypto' || wallet.data) return;
        try {
            let result = await api('/admin/users/' + id + '/crypto');
            setWallet(current => ({ ...current, data: result, error: null }));
        } catch (error) { setWallet(current => ({ ...current, error })); }
    }

    let user = detail ? detail.user || data.users.find(member => member.id === id) : null;
    let shared = { id, user, me, detail, section, refresh };

    return <Dialog.Root open onOpenChange={value => { if (!value) onClose(); }}>
        <Dialog size="lg" className="max-h-[90dvh] overflow-y-auto p-6">
            {!user ? <>
                <Dialog.Title className="sr-only">Member details</Dialog.Title>
                <Loading />
            </> : <div className="flex flex-col gap-4">
                <div className="flex items-start justify-between gap-4">
                    <div className="flex min-w-0 items-center gap-4">
                        <Avatar user={user} size={56} />
                        <div className="min-w-0">
                            <Dialog.Title className="truncate text-lg font-semibold text-kumo-default">{user.name}</Dialog.Title>
                            <p className="truncate text-sm text-kumo-subtle">@{user.username || ''}</p>
                            <small className="text-xs text-kumo-subtle">{user.role === 'admin' ? 'Administrator' : 'Member'} · {accountState(user)}</small>
                        </div>
                    </div>
                    <Dialog.Close render={props => <Button {...props} type="button" variant="ghost" shape="square" size="sm" icon={XIcon} aria-label="Close member details" />} />
                </div>
                <Tabs variant="underline" aria-label="Account management" tabs={tabs} value={tab} onValueChange={openTab} />
                <section role="tabpanel" aria-label={tabs.find(item => item.value === tab)?.label}>
                    {tab === 'profile' ? <ProfileTab key={revision} {...shared} onDetail={replaceDetail} onClose={onClose} /> : null}
                    {tab === 'access' ? <AccessTab {...shared} accessState={accessState} setAccessState={setAccessState} onDetail={replaceDetail} /> : null}
                    {tab === 'sessions' ? <SessionsTab {...shared} confirmation={confirmation} setConfirmation={setConfirmation} onSessions={sessions => setDetail(current => ({ ...current, sessions }))} /> : null}
                    {tab === 'crypto' ? <CryptoTab id={id} wallet={wallet} setWallet={setWallet} section={section} request={cryptoRequest} /> : null}
                    {tab === 'memberships' ? <ChatsTab detail={detail} section={section} /> : null}
                    {tab === 'privacy' ? <PrivacyTab detail={detail} /> : null}
                </section>
            </div>}
        </Dialog>
    </Dialog.Root>;
}
