import { useState } from 'react';
import { Badge, Button, Table, Text } from '@cloudflare/kumo';
import { CaretRightIcon, CheckCircleIcon, CrownIcon, DownloadSimpleIcon, ProhibitIcon, UsersIcon, XIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { csvCell, date, matches, number, paged, premiumExpiry } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Person, Status, Toolbar } from '../components/ui.jsx';

// premium/free/funded are the stars view's filters; kept so both views can share this
export function filterMembers(users, query, filter) {
    return users.filter(user => matches(query, user.name, user.username) && (filter === 'all' || filter === 'active' && !user.disabled || filter === 'disabled' && user.disabled || filter === 'admin' && user.role === 'admin' || filter === 'premium' && user.isPremium || filter === 'free' && !user.isPremium || filter === 'funded' && user.stars > 0));
}

export function memberStatus(user) {
    return user.accountState && user.accountState !== 'active' ? user.accountState : user.disabled ? 'Disabled' : 'Active';
}

export function membersCsv(users) {
    return [['ID', 'Name', 'Username', 'Role', 'Status', 'Premium until', 'Stars'], ...users.map(user => [user.id, user.name, user.username, user.role, user.disabled ? 'disabled' : 'active', user.isPremium ? premiumExpiry(user) : '', user.stars])].map(row => row.map(csvCell).join(',')).join('\r\n');
}

function download(csv, name) {
    let url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
    let link = document.createElement('a');
    link.href = url;
    link.download = name;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function VerificationButton({ user, onChanged }) {
    let [busy, setBusy] = useState(false);
    async function change() {
        let payload = { kind: 'user', id: user.id, verified: !user.isVerified };
        setBusy(true);
        try {
            await api('/admin/verification', 'PATCH', payload);
            await onChanged();
            toast(payload.verified ? 'Verification granted.' : 'Verification revoked.', 'success');
        } catch (error) { toastError(error); }
        finally { setBusy(false); }
    }
    return <Button size="sm" variant="ghost" loading={busy} disabled={user.disabled} aria-label={`${user.isVerified ? 'Revoke verification for' : 'Verify'} ${user.name || 'this peer'}`} onClick={change}>{user.isVerified ? 'Revoke verification' : 'Verify'}</Button>;
}

export default function Members() {
    let { data, user: me, refresh } = useAdmin();
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [selected, setSelected] = useState(() => new Set());
    let [bulk, setBulk] = useState(null);
    let reference = useState(() => crypto.randomUUID())[0];
    let users = filterMembers(data.users, query, filter);
    let view = paged(users, page, 12);
    let selectable = view.items.filter(user => user.role !== 'admin');
    let pageSelected = selectable.filter(user => selected.has(user.id)).length;
    let ids = [...selected];
    let first = data.users.find(user => user.id === ids[0]);

    function toggle(id, checked) {
        setSelected(current => {
            let next = new Set(current);
            if (checked) next.add(id);
            else next.delete(id);
            return next;
        });
    }

    function togglePage(checked) {
        setSelected(current => {
            let next = new Set(current);
            selectable.forEach(user => { if (checked) next.add(user.id); else next.delete(user.id); });
            return next;
        });
    }

    function exportMembers() {
        download(membersCsv(users), 'greengram-members.csv');
        toast('Member export downloaded.', 'success');
    }

    return <>
        <Panel flush title={<>All members <Badge variant="secondary" className="ml-1 align-middle">{number(users.length)}</Badge></>} description="Search, filter, and manage individual accounts.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search by name or username…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All members'], ['active', 'Active'], ['disabled', 'Disabled'], ['admin', 'Administrators']]}>
                <Button size="sm" variant="secondary" icon={DownloadSimpleIcon} onClick={exportMembers}>Export</Button>
            </Toolbar>
            {selected.size ? <div className="flex flex-wrap items-center gap-2 border-b border-kumo-hairline bg-kumo-recessed px-4 py-2">
                <Text size="sm" bold>{number(selected.size)} selected</Text>
                <Button size="sm" variant="secondary" icon={CheckCircleIcon} onClick={() => setBulk({ disabled: false })}>Enable</Button>
                <Button size="sm" variant="secondary" icon={ProhibitIcon} onClick={() => setBulk({ disabled: true })}>Disable</Button>
                <Button size="sm" variant="secondary" icon={CrownIcon} onClick={() => dialogs.open('premium', { ids, onSaved: () => setSelected(new Set()) })}>Grant Premium</Button>
                <Button size="sm" variant="ghost" shape="square" icon={XIcon} aria-label="Clear selection" className="ml-auto" onClick={() => setSelected(new Set())} />
            </div> : null}
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.CheckHead label="Select this page" checked={Boolean(selectable.length) && pageSelected === selectable.length} indeterminate={pageSelected > 0 && pageSelected < selectable.length} disabled={!selectable.length} onCheckedChange={togglePage} />
                        <Table.Head>Member</Table.Head>
                        <Table.Head>Role</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head>Plan</Table.Head>
                        <Table.Head>Joined</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(user => <Table.Row key={user.id}>
                        <Table.CheckCell label={`Select ${user.name}`} checked={selected.has(user.id)} disabled={user.role === 'admin'} onCheckedChange={checked => toggle(user.id, checked)} />
                        <Table.Cell><Person user={user} onOpen={() => dialogs.open('member', { id: user.id })} suffix={user.id === me.id ? <Badge variant="outline" className="ml-2">you</Badge> : null} /></Table.Cell>
                        <Table.Cell><Status>{user.role === 'admin' ? 'Admin' : 'Member'}</Status></Table.Cell>
                        <Table.Cell><Status>{memberStatus(user)}</Status></Table.Cell>
                        <Table.Cell>{user.isPremium ? <span className="flex items-center gap-2">
                            <CrownIcon size={14} className="shrink-0 text-kumo-default" />
                            <span className="min-w-0">
                                <span className="block text-sm text-kumo-default">Premium</span>
                                <small className="block text-xs text-kumo-subtle">{premiumExpiry(user, 'Until ')}</small>
                            </span>
                        </span> : <span className="text-sm text-kumo-subtle">Free plan</span>}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{date(user.createdAt)}</Table.Cell>
                        <Table.Cell className="text-right whitespace-nowrap">
                            <span className="inline-flex items-center gap-1">
                                <VerificationButton user={user} onChanged={refresh} />
                                <Button size="sm" variant="ghost" onClick={() => dialogs.open('member', { id: user.id })}>Manage <CaretRightIcon size={13} /></Button>
                            </span>
                        </Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={UsersIcon} title="No members found" description="Try another search or change your filter." />}
            <Pager view={view} setPage={setPage} noun="members" />
        </Panel>
        <ActionDialog open={Boolean(bulk)} onClose={() => setBulk(null)} icon={bulk?.disabled ? ProhibitIcon : CheckCircleIcon} destructive={Boolean(bulk?.disabled)} title={(bulk?.disabled ? 'Disable account' : 'Enable account') + (ids.length > 1 ? ` · ${ids.length} members` : '')} description={ids.length === 1 && first ? `${first.name} · @${first.username}` : 'This change applies to the selected members.'} confirmLabel="Apply change" onConfirm={async () => {
            let completed = 0;
            try {
                for (let id of ids) {
                    await api('/admin', 'PATCH', { action: 'disable', id, disabled: bulk.disabled, reference: reference + '-' + id });
                    completed++;
                }
            } catch (error) {
                await refresh().catch(() => {});
                throw new Error((completed ? `${completed} changes saved. ` : '') + error.message);
            }
            setSelected(new Set());
            setBulk(null);
            await refresh();
            toast('Changes saved.', 'success');
        }}>
            <p className="rounded-lg bg-kumo-recessed px-3 py-2 text-sm text-kumo-default">{bulk?.disabled ? 'The selected accounts will be signed out immediately and unable to sign in.' : 'The selected accounts will be allowed to sign in again.'}</p>
        </ActionDialog>
    </>;
}
