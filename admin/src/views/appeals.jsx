import { useState } from 'react';
import { Badge, Button, Input, Table } from '@cloudflare/kumo';
import { GavelIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { ago, matches, number, paged } from '../lib/format.js';
import { toast, useLive } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Loading, Pager, Panel, Status, Toolbar } from '../components/ui.jsx';

const statusLabel = { open: 'Waiting', approved: 'Approved', rejected: 'Rejected' };

export function filterAppeals(appeals, query, filter) {
    return appeals.filter(appeal => matches(query, appeal.name, appeal.username, appeal.text) && (filter === 'all' || appeal.status === filter));
}

function Decision({ appeal, approving, onClose, reload }) {
    let [reason, setReason] = useState('');
    return <ActionDialog open onClose={onClose} icon={GavelIcon} destructive={!approving} title={approving ? 'Unfreeze this account?' : 'Reject this appeal?'} description={approving ? `${appeal.name} can use their account again right away. @SpamBot tells them.` : `${appeal.name} stays frozen and can appeal again in a day. @SpamBot tells them.`} confirmLabel={approving ? 'Unfreeze' : 'Reject'} onConfirm={async () => {
        await api(`/admin/appeals/${appeal.id}/${approving ? 'approve' : 'reject'}`, 'POST', approving ? {} : { reason: reason.trim() });
        onClose();
        await reload();
        toast(approving ? 'Account unfrozen.' : 'Appeal rejected.', 'success');
    }}>
        <p className="whitespace-pre-wrap rounded-lg px-3 py-3 text-sm text-kumo-default ring ring-kumo-line">{appeal.text}</p>
        {approving ? null : <Input label="Reason" description="Optional. Sent to them as it is." className="w-full" maxLength={500} value={reason} onChange={event => setReason(event.target.value)} autoComplete="off" />}
    </ActionDialog>;
}

export default function Appeals() {
    let live = useLive('/admin/appeals');
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('open');
    let [page, setPage] = useState(1);
    let [deciding, setDeciding] = useState(null);
    if (!live.data) return <Loading error={live.error} label="Loading appeals…" />;
    let all = live.data.appeals;
    let view = paged(filterAppeals(all, query, filter), page, 12);
    let waiting = all.filter(appeal => appeal.status === 'open').length;
    return <>
        <Panel flush title={<>Appeals <Badge variant="secondary" className="ml-1 align-middle">{number(waiting)}</Badge></>} description="Frozen accounts ask to be let back through @SpamBot.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search appeals…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['open', 'Waiting'], ['approved', 'Approved'], ['rejected', 'Rejected'], ['all', 'All appeals']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Member</Table.Head>
                        <Table.Head>Appeal</Table.Head>
                        <Table.Head>Filed</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(appeal => <Table.Row key={appeal.id}>
                        <Table.Cell className="text-sm font-medium text-kumo-default">{appeal.name}<span className="block text-xs font-normal text-kumo-subtle">@{appeal.username}</span></Table.Cell>
                        <Table.Cell className="max-w-96 whitespace-pre-wrap break-words text-sm text-kumo-default">{appeal.text}{appeal.reason ? <span className="mt-1 block text-xs text-kumo-subtle">Reason: {appeal.reason}</span> : null}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{ago(appeal.createdAt)}</Table.Cell>
                        <Table.Cell><Status>{statusLabel[appeal.status]}</Status></Table.Cell>
                        <Table.Cell className="text-right">{appeal.status === 'open' ? <span className="inline-flex gap-2">
                            <Button size="sm" variant="secondary" onClick={() => setDeciding({ appeal, approving: false })}>Reject</Button>
                            <Button size="sm" variant="primary" onClick={() => setDeciding({ appeal, approving: true })}>Unfreeze</Button>
                        </span> : null}</Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={GavelIcon} title="No appeals here" description="They show up once a frozen account writes to @SpamBot." />}
            <Pager view={view} setPage={setPage} noun="appeals" />
        </Panel>
        {deciding ? <Decision key={deciding.appeal.id} appeal={deciding.appeal} approving={deciding.approving} onClose={() => setDeciding(null)} reload={live.reload} /> : null}
    </>;
}
