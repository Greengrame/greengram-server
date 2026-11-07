import { useState } from 'react';
import { Button, Select, Table } from '@cloudflare/kumo';
import { CrownIcon } from '@phosphor-icons/react';
import { matches, paged, premiumDateTime, premiumRemaining, subscriptionStatus } from '../lib/format.js';
import { useAdmin } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Person, Status, Toolbar } from '../components/ui.jsx';

export function filterPremium(users, query, filter, now = Math.floor(Date.now() / 1000)) {
    return users.filter(user => matches(query, user.name, user.username) && (filter === 'all' ? user.premiumUntil > 0 || user.isPremium : filter === 'active' ? user.isPremium : filter === 'expiring' ? subscriptionStatus(user, now) === 'Expiring Soon' : filter === 'unlimited' ? user.isPremium && user.premiumUnlimited : filter === 'expired' ? !user.isPremium && user.premiumUntil > 0 && user.premiumUntil <= now : !user.isPremium && !user.premiumUntil));
}

export function premiumEnds(user) {
    return user.premiumUnlimited && user.isPremium ? 'Never' : user.premiumUntil > 0 ? premiumDateTime(user.premiumUntil) : '—';
}

export default function Premium() {
    let { data } = useAdmin();
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [granting, setGranting] = useState(false);
    let [recipient, setRecipient] = useState('');
    let now = Math.floor(Date.now() / 1000);
    let view = paged(filterPremium(data.users, query, filter, now), page, 12);
    let accounts = Object.fromEntries(data.users.map(user => [String(user.id), `${user.name} · @${user.username}`]));
    return <>
        <Panel flush title="Premium Subscriptions" description="Grant access, adjust expiration dates, or revoke a subscription." actions={<Button size="sm" variant="primary" icon={CrownIcon} onClick={() => { setRecipient(String(data.users[0]?.id ?? '')); setGranting(true); }}>Grant Premium</Button>}>
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search subscriptions…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All subscriptions'], ['active', 'Active'], ['expiring', 'Expires within 7 days'], ['unlimited', 'Unlimited'], ['expired', 'Expired'], ['free', 'Without a subscription']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Subscriber</Table.Head>
                        <Table.Head>Subscription</Table.Head>
                        <Table.Head>Starts</Table.Head>
                        <Table.Head>Ends</Table.Head>
                        <Table.Head>Time Remaining</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(user => <Table.Row key={user.id}>
                        <Table.Cell><Person user={user} onOpen={() => dialogs.open('member', { id: user.id })} /></Table.Cell>
                        <Table.Cell><Status>{subscriptionStatus(user, now)}</Status></Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{premiumDateTime(user.premiumStartsAt)}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{premiumEnds(user)}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-default tabular-nums">{premiumRemaining(user, now)}</Table.Cell>
                        <Table.Cell className="text-right"><Button size="sm" variant="secondary" onClick={() => dialogs.open('premium', { ids: [user.id] })}>{user.isPremium ? 'Manage' : 'Grant Premium'}</Button></Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={CrownIcon} title="No subscriptions found" description="Choose a different filter or grant Premium to an account." />}
            <Pager view={view} setPage={setPage} noun="subscriptions" />
        </Panel>
        <ActionDialog open={granting} onClose={() => setGranting(false)} icon={CrownIcon} title="Grant Premium" confirmLabel="Continue" confirmDisabled={!recipient} onConfirm={async () => {
            let id = Number(recipient);
            if (!data.users.some(user => user.id === id)) return;
            setGranting(false);
            dialogs.open('premium', { ids: [id] });
        }}>
            <Select label="Account" hideLabel={false} required value={recipient} onValueChange={value => setRecipient(value ?? '')} items={accounts} />
        </ActionDialog>
    </>;
}
