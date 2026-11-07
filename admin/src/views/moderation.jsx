import { useRef, useState } from 'react';
import { Badge, Banner, Button } from '@cloudflare/kumo';
import { ChatCircleIcon, ShieldCheckIcon, TrashIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { matches, number, paged } from '../lib/format.js';
import { toast, useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { ActionDialog, Avatar, EmptyState, Pager, Panel, Toolbar } from '../components/ui.jsx';

// prefixed so Object.fromEntries in Toolbar keeps "All senders" first
export function senderValue(id) {
    return 'user:' + id;
}

export function filterMessages(messages, query, filter) {
    return messages.filter(message => matches(query, message.body, message.author) && (filter === 'all' || senderValue(message.user_id) === filter));
}

export default function Moderation() {
    let { data, refresh } = useAdmin();
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [removing, setRemoving] = useState(null);
    let top = useRef(null);
    let users = data.users || [];
    let entries = filterMessages(data.recent || [], query, filter);
    let view = paged(entries, page, 20);
    // the feed can shrink under us after a refresh
    if (view.page !== page) setPage(view.page);

    function turn(value) {
        setPage(value);
        top.current?.scrollIntoView({ block: 'nearest' });
    }

    return <>
        <Banner icon={<ShieldCheckIcon weight="fill" />} variant="secondary" description="Moderators can review all stored conversations, including private messages." />
        <div ref={top} className="scroll-mt-6">
            <Panel flush title={<>Recent messages <Badge variant="secondary" className="ml-1 align-middle">{number(entries.length)}</Badge></>} description="Review the latest 50 messages across your workspace.">
                <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search messages or senders…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All senders'], ...users.map(user => [senderValue(user.id), user.name])]} />
                {view.total ? <ul className="divide-y divide-kumo-hairline">
                    {view.items.map(message => {
                        let author = users.find(user => user.id === message.user_id);
                        return <li key={message.id} className="flex flex-col gap-2 px-4 py-4">
                            <div className="flex items-center gap-3">
                                <Avatar user={author || { id: message.user_id, name: message.author }} size={32} />
                                <div className="min-w-0 flex-1">
                                    {author ? <button type="button" className="block truncate text-left text-sm font-medium text-kumo-default hover:underline" onClick={() => dialogs.open('member', { id: author.id })}>{message.author}</button> : <span className="block truncate text-sm font-medium text-kumo-default">{message.author}</span>}
                                    <span className="block text-xs text-kumo-subtle">{new Date(message.created_at * 1000).toLocaleString()}</span>
                                </div>
                                <Badge variant="outline" className="max-w-56 truncate">{message.chat_title || 'Conversation'}</Badge>
                            </div>
                            <p className="text-sm whitespace-pre-wrap break-words text-kumo-default">{message.body}</p>
                            <div className="flex items-center justify-between gap-3">
                                <span className="text-xs text-kumo-subtle tabular-nums">Message #{message.id}</span>
                                <Button size="sm" variant="ghost" icon={TrashIcon} className="text-kumo-danger" onClick={() => setRemoving(message)}>Remove message</Button>
                            </div>
                        </li>;
                    })}
                </ul> : <EmptyState icon={ChatCircleIcon} title="No messages to review" description="Messages will appear here as your community starts talking." />}
                <Pager view={view} setPage={turn} noun="messages" />
            </Panel>
        </div>
        <ActionDialog open={Boolean(removing)} onClose={() => setRemoving(null)} icon={TrashIcon} destructive title="Remove this message?" description="The message will be deleted for everyone. This moderation action is recorded in the audit log." confirmLabel="Remove message" onConfirm={async () => {
            await api('/admin/messages/' + encodeURIComponent(removing.id), 'DELETE', {});
            setRemoving(null);
            await refresh();
            toast('Message removed.', 'success');
        }} />
    </>;
}
