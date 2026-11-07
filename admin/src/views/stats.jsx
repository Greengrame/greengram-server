import { useState } from 'react';
import { Tabs, Text } from '@cloudflare/kumo';
import { number, percentChange } from '../lib/format.js';
import { useAdmin, useLive } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { DailyChart, HourChart } from '../lib/charts.jsx';
import { Change, Kpi, KpiStrip, Loading, Panel } from '../components/ui.jsx';
import { RankList, groupItems } from './overview.jsx';

function points(stats, key) {
    return stats.series.map(point => ({ day: point.day, value: point[key] }));
}

export default function Statistics() {
    let { data } = useAdmin();
    let dialogs = useDialogs();
    let [days, setDays] = useState('30');
    let live = useLive('/admin/stats?days=' + days);
    let stats = live.data && String(live.data.days) === days ? live.data : null;
    let members = new Set(data.users.map(user => user.id));
    return <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
            <Tabs variant="segmented" value={days} onValueChange={value => setDays(String(value))} tabs={[{ value: '7', label: '7 days' }, { value: '30', label: '30 days' }, { value: '90', label: '90 days' }]} />
            <Text variant="secondary" size="sm">Days and hours in UTC, compared with the {days} days before.</Text>
        </div>
        {!stats ? <Panel><Loading error={live.error} /></Panel> : <>
            <KpiStrip>
                <Kpi label="Messages" value={number(stats.totals.messages)} note={<Change value={percentChange(stats.totals.messages, stats.totals.messagesBefore)} />} />
                <Kpi label="Per day" value={number(Math.round(stats.totals.messages / stats.days))} note="average" />
                <Kpi label="Active senders" value={number(stats.totals.active)} note={<Change value={percentChange(stats.totals.active, stats.totals.activeBefore)} />} />
                <Kpi label="New members" value={number(stats.totals.signups)} note={<Change value={percentChange(stats.totals.signups, stats.totals.signupsBefore)} />} />
            </KpiStrip>
            <div className="grid gap-6 lg:grid-cols-2">
                <Panel title="Messages" description="Per day">
                    <DailyChart label="Messages per day" series={[{ name: 'Messages', points: points(stats, 'messages') }]} />
                </Panel>
                <Panel title="Active senders" description="Members who sent at least one message">
                    <DailyChart type="line" label="Active senders per day" series={[{ name: 'Senders', points: points(stats, 'active') }]} />
                </Panel>
                <Panel title="New members" description="Accounts created per day">
                    <DailyChart label="Sign-ups per day" series={[{ name: 'Sign-ups', points: points(stats, 'signups') }]} />
                </Panel>
                <Panel title="Busiest hours" description="Messages by hour of day">
                    <HourChart values={stats.hours} />
                </Panel>
                <Panel flush title="Busiest groups">
                    <RankList items={groupItems(stats.topChats)} empty="No group messages in this period." />
                </Panel>
                <Panel flush title="Most active members">
                    <RankList items={stats.topSenders.map(user => ({ key: user.id, label: user.name, sub: '@' + user.username, value: user.messages, detail: members.has(user.id) ? user.id : null }))} empty="Nobody has sent messages in this period." onOpen={id => dialogs.open('member', { id })} />
                </Panel>
            </div>
        </>}
    </div>;
}
