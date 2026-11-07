import { useState } from 'react';
import { Badge, Button, LayerCard } from '@cloudflare/kumo';
import { StarIcon } from '@phosphor-icons/react';
import { matches, number, paged } from '../lib/format.js';
import { useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { EmptyState, Pager, Panel, Toolbar } from '../components/ui.jsx';

export function filterStarTransactions(entries, query, filter) {
    return entries.filter(entry => matches(query, entry.name, entry.username, entry.transactionId || '', entry.note || '') && (filter === 'all' || filter === 'incoming' && entry.amount > 0 || filter === 'outgoing' && entry.amount < 0));
}

export function totalStars(users) {
    return users.reduce((sum, user) => sum + Number(user.stars || 0), 0);
}

export default function Stars() {
    let { data } = useAdmin();
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let all = data.starTransactions || [];
    let view = paged(filterStarTransactions(all, query, filter), page, 20);
    return <>
        <LayerCard>
            <LayerCard.Primary className="flex flex-col items-center gap-2 py-8 text-center">
                <img src="/stars-logo.svg" alt="" width={96} height={96} className="" />
                <span className="text-3xl font-semibold tabular-nums text-kumo-default">{number(totalStars(data.users))}</span>
                <span className="text-sm text-kumo-subtle">Stars across member accounts</span>
                <Button className="mt-2" variant="primary" icon={StarIcon} onClick={() => dialogs.open('stars', {})}>New transaction</Button>
            </LayerCard.Primary>
        </LayerCard>
        <Panel flush title={<>Transactions <Badge variant="secondary" className="ml-1 align-middle">{number(all.length)}</Badge></>} description="The latest 200 transactions. Select one to view its receipt.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search recipient, transaction ID or reason…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All transactions'], ['incoming', 'Credits'], ['outgoing', 'Debits']]} />
            {view.total ? <ul className="divide-y divide-kumo-hairline">
                {view.items.map(entry => <li key={entry.id}>
                    <button type="button" className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-kumo-tint focus-visible:bg-kumo-tint focus-visible:outline-none" onClick={() => dialogs.open('receipt', { entry })}>
                        <img src="/stars-logo.svg" alt="" width={36} height={36} className="shrink-0" />
                        <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm font-medium text-kumo-default first-letter:uppercase">{entry.title || String(entry.reason || '').replaceAll('_', ' ')}</span>
                            <span className="block truncate text-xs text-kumo-subtle">{entry.name} · @{entry.username}</span>
                            <span className="block truncate text-xs text-kumo-subtle">{entry.note || new Date(entry.created_at * 1000).toLocaleString()}</span>
                        </span>
                        <span className="shrink-0 text-right">
                            <span className={'block text-sm font-semibold tabular-nums ' + (entry.amount > 0 ? 'text-kumo-success' : 'text-kumo-danger')}>{entry.amount > 0 ? '+' : ''}{number(entry.amount)}</span>
                            <span className="block text-xs text-kumo-subtle">Stars</span>
                        </span>
                    </button>
                </li>)}
            </ul> : <EmptyState icon={StarIcon} title="No transactions found" description="Create a transaction or try another search." />}
            <Pager view={view} setPage={setPage} noun="transactions" />
        </Panel>
    </>;
}
