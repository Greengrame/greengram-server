import { Button, Link } from '@cloudflare/kumo';
import { CaretRightIcon, ChartBarIcon, ClockCounterClockwiseIcon, CrownIcon, MegaphoneIcon, ShieldWarningIcon, UserPlusIcon, WarningIcon } from '@phosphor-icons/react';
import { ago, auditLabel, auditTarget, duration, number, percentChange, subscriptionStatus } from '../lib/format.js';
import { logoUrl, useAdmin, useLive } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { DailyChart } from '../lib/charts.jsx';
import { Change, Kpi, KpiStrip, Loading, Panel, Person } from '../components/ui.jsx';

function SeeAll({ href, children = 'View all' }) {
    return <Link href={href} variant="plain" className="inline-flex items-center gap-1 text-xs text-kumo-subtle hover:text-kumo-default">{children}<CaretRightIcon size={12} /></Link>;
}

export function RankList({ items, empty, onOpen }) {
    let max = Math.max(1, ...items.map(item => item.value));
    if (!items.length) return <p className="px-4 py-8 text-center text-sm text-kumo-subtle">{empty}</p>;
    return <ol className="divide-y divide-kumo-hairline">
        {items.map((item, index) => <li key={item.key ?? index}>
            <button type="button" disabled={!onOpen || !item.detail} onClick={() => onOpen?.(item.detail)} className="relative flex w-full items-center gap-3 px-4 py-3 text-left enabled:hover:bg-kumo-tint">
                <span className="w-4 text-xs text-kumo-subtle tabular-nums">{index + 1}</span>
                <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium text-kumo-default">{item.label}</span>
                    <span className="block truncate text-xs text-kumo-subtle">{item.sub}</span>
                </span>
                <span className="text-sm font-medium text-kumo-default tabular-nums">{number(item.value)}</span>
                <span className="absolute right-4 bottom-0 left-11 h-px bg-kumo-hairline"><span className="block h-px bg-kumo-brand" style={{ width: item.value / max * 100 + '%' }} /></span>
            </button>
        </li>)}
    </ol>;
}

export function groupItems(chats) {
    return chats.map(chat => ({ key: chat.id, label: chat.title || '#' + chat.id, sub: (chat.isChannel ? 'Channel' : 'Group') + (chat.username ? ' · @' + chat.username : '') + ' · ' + number(chat.members) + ' members', value: chat.messages }));
}

export default function Overview() {
    let { data, branding } = useAdmin();
    let dialogs = useDialogs();
    let stats = useLive('/admin/stats?days=30');
    let health = useLive('/admin/health', { interval: 15000 });
    let users = data.users;
    let totals = stats.data?.totals;
    let premium = users.filter(user => user.isPremium).length;
    let expiring = users.filter(user => subscriptionStatus(user) === 'Expiring Soon').length;
    let restricted = users.filter(user => user.disabled || user.accountState && user.accountState !== 'active').length;
    let newest = [...users].sort((a, b) => Number(b.createdAt || 0) - Number(a.createdAt || 0)).slice(0, 5);
    let serverErrors = health.data?.requests.serverErrors;
    let attention = [
        { icon: CrownIcon, label: 'Premium expiring this week', value: number(expiring), href: '#premium', alert: expiring > 0 },
        { icon: ShieldWarningIcon, label: 'Restricted accounts', value: number(restricted), href: '#members', alert: restricted > 0 },
        { icon: WarningIcon, label: 'Server errors since start', value: serverErrors === undefined ? '…' : number(serverErrors), href: '#health', alert: serverErrors > 0 },
        { icon: UserPlusIcon, label: 'New registrations', value: data.settings.registrationEnabled ? 'Open' : 'Closed', href: '#settings', alert: false }
    ];
    return <div className="flex flex-col gap-6">
        <section className="flex flex-wrap items-center gap-4">
            <img src={logoUrl(branding)} alt="" className="size-12 rounded-full object-cover" />
            <div className="min-w-0 flex-1">
                <h2 className="truncate text-lg font-semibold text-kumo-default">{data.settings.siteName}</h2>
                <p className="text-sm text-kumo-subtle">{number(data.chats)} chats · {number(data.messageCount)} messages stored{health.data ? ' · up ' + duration(health.data.process.uptime) : ''}</p>
            </div>
            <div className="flex gap-2">
                <Button variant="secondary" icon={MegaphoneIcon} onClick={() => location.assign('#broadcast')}>Broadcast</Button>
                <Button variant="secondary" icon={ChartBarIcon} onClick={() => location.assign('#stats')}>Statistics</Button>
            </div>
        </section>
        <KpiStrip>
            <Kpi label="Members" value={number(users.length)} note={totals ? `+${number(totals.signups)} in 30 days` : ''} />
            <Kpi label="Online now" value={health.data ? number(health.data.live.online) : '…'} note={health.data ? number(health.data.tables.sessions) + ' active sessions' : ''} />
            <Kpi label="Messages, 30 days" value={totals ? number(totals.messages) : '…'} note={totals ? <><Change value={percentChange(totals.messages, totals.messagesBefore)} /> vs previous 30</> : ''} />
            <Kpi label="Active senders" value={totals ? number(totals.active) : '…'} note={totals ? <><Change value={percentChange(totals.active, totals.activeBefore)} /> vs previous 30</> : ''} />
            <Kpi label="Premium" value={number(premium)} note={users.length ? Math.round(premium / users.length * 100) + '% of members' : ''} />
        </KpiStrip>
        <div className="grid gap-6 lg:grid-cols-3">
            <Panel className="lg:col-span-2" title="Messages per day" description="Last 30 days, UTC" actions={<SeeAll href="#stats">Statistics</SeeAll>}>
                {stats.data ? <DailyChart label="Messages per day" series={[{ name: 'Messages', points: stats.data.series.map(point => ({ day: point.day, value: point.messages })) }]} /> : <Loading error={stats.error} />}
            </Panel>
            <Panel flush title="Needs attention">
                <ul className="divide-y divide-kumo-hairline">
                    {attention.map(row => <li key={row.label}>
                        <a href={row.href} className="flex items-center gap-3 px-4 py-3.5 text-sm text-kumo-default hover:bg-kumo-tint">
                            <row.icon size={18} className={row.alert ? 'text-kumo-warning' : 'text-kumo-subtle'} />
                            <span className="flex-1">{row.label}</span>
                            <span className={'font-medium tabular-nums ' + (row.alert ? 'text-kumo-warning' : 'text-kumo-default')}>{row.value}</span>
                        </a>
                    </li>)}
                </ul>
            </Panel>
        </div>
        <div className="grid gap-6 lg:grid-cols-3">
            <Panel flush title="Newest members" actions={<SeeAll href="#members" />}>
                <ul className="divide-y divide-kumo-hairline">
                    {newest.map(user => <li key={user.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                        <Person user={user} onOpen={() => dialogs.open('member', { id: user.id })} />
                        <span className="shrink-0 text-xs text-kumo-subtle">{ago(user.createdAt)}</span>
                    </li>)}
                </ul>
            </Panel>
            <Panel flush title="Busiest groups" description="Last 30 days" actions={<SeeAll href="#conversations" />}>
                {stats.data ? <RankList items={groupItems(stats.data.topChats)} empty="No group messages in this period." /> : <Loading error={stats.error} />}
            </Panel>
            <Panel flush title="Recent admin activity" actions={<SeeAll href="#audit">Audit log</SeeAll>}>
                {data.audit.length ? <ul className="divide-y divide-kumo-hairline">
                    {data.audit.slice(0, 5).map(entry => <li key={entry.id} className="flex gap-3 px-4 py-3">
                        <ClockCounterClockwiseIcon size={16} className="mt-0.5 shrink-0 text-kumo-subtle" />
                        <span className="min-w-0 text-sm">
                            <span className="text-kumo-default"><strong className="font-medium">{entry.actor}</strong> {auditLabel(entry.action)}</span>
                            <span className="block text-xs text-kumo-subtle">{ago(entry.created_at)} · {auditTarget(entry)}</span>
                        </span>
                    </li>)}
                </ul> : <p className="px-4 py-8 text-center text-sm text-kumo-subtle">No administrative changes yet.</p>}
            </Panel>
        </div>
    </div>;
}
