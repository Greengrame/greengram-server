import { useState } from 'react';
import { Badge, Button, LinkButton, Table } from '@cloudflare/kumo';
import { ArrowRightIcon, ChatsCircleIcon, SealCheckIcon, TrashIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { matches, number, paged } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Toolbar, Verified } from '../components/ui.jsx';

export function filterConversations(conversations, query, filter) {
    return conversations.filter(chat => matches(query, chat.title, chat.username || '', chat.owner) && (filter === 'all' || chat.kind === filter));
}

export default function Conversations() {
    let { data, refresh } = useAdmin();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [removing, setRemoving] = useState(null);
    let [pending, setPending] = useState(null);
    let entries = filterConversations(data.conversations || [], query, filter);
    let view = paged(entries, page, 20);

    async function changeVerification(chat) {
        let verified = !chat.isVerified;
        setPending(chat.id);
        try {
            await api('/admin/verification', 'PATCH', { kind: 'chat', id: chat.id, verified });
            await refresh();
            toast(verified ? 'Verification granted.' : 'Verification revoked.', 'success');
        } catch (error) { toastError(error); }
        finally { setPending(null); }
    }

    return <>
        <Panel flush title={<>Community spaces <Badge variant="secondary" className="ml-1 align-middle">{number(entries.length)}</Badge></>} description="All groups and broadcast channels in your workspace." actions={<LinkButton href="/" size="sm" variant="secondary">Open Messenger <ArrowRightIcon size={14} /></LinkButton>}>
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search conversations or owners…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All conversations'], ['group', 'Groups'], ['channel', 'Channels']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Conversation</Table.Head>
                        <Table.Head>Type</Table.Head>
                        <Table.Head>Members</Table.Head>
                        <Table.Head>Messages</Table.Head>
                        <Table.Head>Visibility</Table.Head>
                        <Table.Head>Owner</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(chat => <Table.Row key={chat.id}>
                        <Table.Cell>
                            <span className="block min-w-0">
                                <span className="flex items-center text-sm font-medium text-kumo-default">{chat.title}{chat.isVerified ? <Verified /> : null}</span>
                                <span className="block truncate text-xs text-kumo-subtle">{chat.username ? '@' + chat.username : '#' + chat.id}</span>
                            </span>
                        </Table.Cell>
                        <Table.Cell><Badge variant={chat.kind === 'channel' ? 'secondary' : 'outline'}>{chat.kind === 'channel' ? 'Channel' : 'Group'}</Badge></Table.Cell>
                        <Table.Cell className="text-sm tabular-nums text-kumo-default">{number(chat.member_count)}</Table.Cell>
                        <Table.Cell className="text-sm tabular-nums text-kumo-default">{number(chat.message_count)}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{chat.visibility === 'public' ? 'Public' : 'Private'}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{chat.owner}</Table.Cell>
                        <Table.Cell className="text-right whitespace-nowrap">
                            <span className="inline-flex items-center gap-1">
                                <Button size="sm" variant="ghost" icon={SealCheckIcon} loading={pending === chat.id} disabled={Boolean(chat.disabled)} aria-label={`${chat.isVerified ? 'Revoke verification for' : 'Verify'} ${chat.title || 'this peer'}`} onClick={() => changeVerification(chat)}>{chat.isVerified ? 'Revoke verification' : 'Verify'}</Button>
                                <Button size="sm" variant="ghost" icon={TrashIcon} className="text-kumo-danger" aria-label={`Remove ${chat.title}`} onClick={() => setRemoving(chat)}>Remove</Button>
                            </span>
                        </Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={ChatsCircleIcon} title="A space for your community" description="Create a group in the messenger or a channel in community tools." />}
            <Pager view={view} setPage={setPage} noun="conversations" />
        </Panel>
        <ActionDialog open={Boolean(removing)} onClose={() => setRemoving(null)} icon={TrashIcon} destructive title={removing ? `Remove ${removing.title}?` : ''} description={removing ? `This permanently deletes the conversation, its memberships, and ${number(removing.message_count)} messages for everyone. The action is audited.` : ''} confirmLabel="Remove conversation" onConfirm={async () => {
            await api('/admin/conversations/' + removing.id, 'DELETE', {});
            setRemoving(null);
            await refresh();
            toast('Conversation removed.', 'success');
        }} />
    </>;
}
