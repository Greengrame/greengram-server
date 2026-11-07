import { useState } from 'react';
import { Badge, Button, Input, InputArea, Switch, Table } from '@cloudflare/kumo';
import { RobotIcon, SealCheckIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { matches, number, paged } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Person, Status, Toolbar } from '../components/ui.jsx';

export function filterBots(bots, query, filter) {
    return bots.filter(bot => matches(query, bot.name, bot.username) && (filter === 'all' || filter === 'builtin' && !bot.ownerId || filter === 'owned' && bot.ownerId));
}

export function botPatch(fields) {
    return { name: String(fields.name || '').trim(), username: String(fields.username || '').trim(), bio: String(fields.bio || ''), description: String(fields.description || ''), disabled: !fields.enabled };
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

function BotEditor({ bot, onClose }) {
    let { refresh } = useAdmin();
    let [fields, setFields] = useState(() => ({ name: bot.name || '', username: bot.username || '', bio: bot.bio || '', description: bot.description || '', enabled: !bot.disabled }));
    let set = key => event => setFields(current => ({ ...current, [key]: event.target.value }));
    if (!bot.canEdit) return <ActionDialog open onClose={onClose} icon={RobotIcon} title={'Manage @' + bot.username} cancelLabel="Close">
        <p className="text-sm text-kumo-subtle">This bot profile cannot be edited.</p>
    </ActionDialog>;
    return <ActionDialog open onClose={onClose} icon={RobotIcon} title={'Manage @' + bot.username} confirmLabel="Save" onConfirm={async () => {
        await api('/admin/bots/' + bot.id, 'PATCH', botPatch(fields));
        await refresh();
        onClose();
        toast('Bot updated.', 'success');
    }}>
        <Input label="Name" className="w-full" maxLength={80} required value={fields.name} onChange={set('name')} autoComplete="off" passwordManagerIgnore />
        <Input label="Username" className="w-full" maxLength={32} required value={fields.username} onChange={set('username')} autoComplete="off" passwordManagerIgnore />
        <InputArea label="About" className="w-full" maxLength={500} rows={2} value={fields.bio} onChange={set('bio')} autoComplete="off" data-1p-ignore />
        <InputArea label="Description" className="w-full" maxLength={500} rows={3} value={fields.description} onChange={set('description')} autoComplete="off" data-1p-ignore />
        <SwitchRow label="Enable bot" description="Disabled bots cannot use the Bot API." checked={fields.enabled} onCheckedChange={value => setFields(current => ({ ...current, enabled: value }))} />
    </ActionDialog>;
}

export default function Bots() {
    let { data, refresh } = useAdmin();
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [managing, setManaging] = useState(null);
    let [busy, setBusy] = useState(null);
    let all = data.bots || [];
    let view = paged(filterBots(all, query, filter), page, 12);
    async function manage(bot) {
        setBusy('manage:' + bot.id);
        try { setManaging({ ...await api('/admin/bots/' + bot.id), id: bot.id }); }
        catch (error) { toastError(error); }
        finally { setBusy(null); }
    }
    async function verify(bot) {
        let verified = !bot.isVerified;
        setBusy('verify:' + bot.id);
        try {
            await api('/admin/verification', 'PATCH', { kind: 'user', id: bot.id, verified });
            await refresh();
            toast(verified ? 'Verification granted.' : 'Verification revoked.', 'success');
        } catch (error) { toastError(error); }
        finally { setBusy(null); }
    }
    return <>
        <Panel flush title={<>Bots <Badge variant="secondary" className="ml-1 align-middle">{number(all.length)}</Badge></>} description="Bot accounts are managed separately from people.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search bots…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All bots'], ['builtin', 'Built-in bots'], ['owned', 'Member bots']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Bot</Table.Head>
                        <Table.Head>About</Table.Head>
                        <Table.Head>Type</Table.Head>
                        <Table.Head>Owner</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(bot => {
                        let owner = bot.ownerId ? data.users.find(user => user.id === bot.ownerId) : null;
                        return <Table.Row key={bot.id}>
                            <Table.Cell><Person user={bot} /></Table.Cell>
                            <Table.Cell><span className="block max-w-64 truncate text-sm text-kumo-subtle" title={bot.bio || ''}>{bot.bio || '—'}</span></Table.Cell>
                            <Table.Cell><Badge variant={bot.ownerId ? 'secondary' : 'primary'}>{bot.ownerId ? 'Member bot' : 'Built-in'}</Badge></Table.Cell>
                            <Table.Cell className="text-sm">{bot.ownerId ? owner ? <button type="button" className="text-kumo-default hover:underline" onClick={() => dialogs.open('member', { id: owner.id })}>{owner.name}</button> : <span className="text-kumo-subtle">#{bot.ownerId}</span> : <span className="text-kumo-subtle">Instance service</span>}</Table.Cell>
                            <Table.Cell>{bot.kind === 'custom' ? <Status>{bot.disabled ? 'Disabled' : 'Active'}</Status> : null}</Table.Cell>
                            <Table.Cell className="text-right">
                                <span className="inline-flex items-center gap-1">
                                    <Button size="sm" variant="ghost" icon={bot.isVerified ? null : SealCheckIcon} disabled={bot.disabled} loading={busy === 'verify:' + bot.id} aria-label={(bot.isVerified ? 'Revoke verification for ' : 'Verify ') + (bot.name || 'this peer')} onClick={() => verify(bot)}>{bot.isVerified ? 'Revoke verification' : 'Verify'}</Button>
                                    {bot.kind === 'custom' ? <Button size="sm" variant="secondary" loading={busy === 'manage:' + bot.id} onClick={() => manage(bot)}>Manage</Button> : null}
                                </span>
                            </Table.Cell>
                        </Table.Row>;
                    })}
                </Table.Body>
            </Table> : <EmptyState icon={RobotIcon} title="No bots found" description="Try another search or filter." />}
            <Pager view={view} setPage={setPage} noun="bots" />
        </Panel>
        {managing ? <BotEditor key={managing.id} bot={managing} onClose={() => setManaging(null)} /> : null}
    </>;
}
