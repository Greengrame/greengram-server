import { useRef, useState } from 'react';
import { Badge } from '@cloudflare/kumo';
import { CaretRightIcon, ClockCounterClockwiseIcon, CrownIcon, DevicesIcon, GearIcon, MegaphoneIcon, ShieldIcon, StarIcon } from '@phosphor-icons/react';
import { auditDetails, auditLabel, auditTarget, matches, number, paged } from '../lib/format.js';
import { useAdmin } from '../lib/store.jsx';
import { EmptyState, Pager, Panel, Toolbar } from '../components/ui.jsx';

const icons = { stars: StarIcon, premium: CrownIcon, update_settings: GearIcon, broadcast: MegaphoneIcon, admin_sessions_revoked: DevicesIcon };

export const auditFilters = [['all', 'All actions'], ['disable', 'Account status'], ['role', 'Roles'], ['premium', 'Premium'], ['stars', 'Stars'], ['delete_message', 'Messages'], ['delete_conversation', 'Conversations'], ['update_settings', 'Settings'], ['broadcast', 'Announcements'], ['admin_sessions_revoked', 'Sessions']];

export function filterAudit(entries, query, filter) {
    return entries.filter(entry => matches(query, entry.actor, entry.action, auditLabel(entry.action), entry.target_id) && (filter === 'all' || entry.action === filter));
}

function AuditEntry({ entry }) {
    let Icon = icons[entry.action] || ShieldIcon;
    return <li className="px-4 py-3">
        <div className="flex items-start gap-3">
            <span className="mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-kumo-recessed text-kumo-subtle"><Icon size={16} /></span>
            <div className="min-w-0 flex-1">
                <p className="text-sm text-kumo-default"><span className="font-medium">{entry.actor}</span> {auditLabel(entry.action)}</p>
                <p className="text-xs text-kumo-subtle tabular-nums">{new Date(entry.created_at * 1000).toLocaleString()} · {auditTarget(entry)}</p>
                <details className="group mt-2">
                    <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded text-xs text-kumo-subtle select-none hover:text-kumo-default [&::-webkit-details-marker]:hidden">
                        <CaretRightIcon size={12} className="transition-transform group-open:rotate-90" />View details
                    </summary>
                    <pre className="mt-2 max-h-80 overflow-auto rounded-md bg-kumo-recessed p-3 font-mono text-xs whitespace-pre-wrap break-all text-kumo-default ring ring-kumo-hairline">{auditDetails(entry.details)}</pre>
                </details>
            </div>
        </div>
    </li>;
}

export default function Audit() {
    let { data } = useAdmin();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let top = useRef(null);
    let entries = filterAudit(data.audit || [], query, filter);
    let view = paged(entries, page, 20);
    // the feed can shrink under us after a refresh
    if (view.page !== page) setPage(view.page);

    function turn(value) {
        setPage(value);
        top.current?.scrollIntoView({ block: 'nearest' });
    }

    return <div ref={top} className="scroll-mt-6">
        <Panel flush title={<>Administrative activity <Badge variant="secondary" className="ml-1 align-middle">{number(entries.length)}</Badge></>} description="The latest 100 changes. Entries cannot be edited through the panel.">
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search actions or administrators…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={auditFilters} />
            {view.total ? <ul className="divide-y divide-kumo-hairline">
                {view.items.map(entry => <AuditEntry key={entry.id} entry={entry} />)}
            </ul> : <EmptyState icon={ClockCounterClockwiseIcon} title="Nothing to show" description="Try another filter. New administrative actions will appear here." />}
            <Pager view={view} setPage={turn} noun="entries" />
        </Panel>
    </div>;
}
