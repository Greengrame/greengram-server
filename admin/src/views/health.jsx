import { Banner, Meter } from '@cloudflare/kumo';
import { CheckCircleIcon, WarningCircleIcon } from '@phosphor-icons/react';
import { bytes, duration, number } from '../lib/format.js';
import { useLive } from '../lib/store.jsx';
import { MinuteChart } from '../lib/charts.jsx';
import { Kpi, KpiStrip, Loading, Panel, Row, Rows } from '../components/ui.jsx';

function Usage({ label, used, total, note }) {
    return <div className="flex flex-col gap-1">
        <Meter label={label} value={total ? Math.min(100, used / total * 100) : 0} customValue={`${bytes(used)} / ${bytes(total)}`} />
        {note ? <span className="text-xs text-kumo-subtle">{note}</span> : null}
    </div>;
}

export default function Health() {
    let live = useLive('/admin/health', { interval: 10000 });
    let health = live.data;
    if (!health) return <Panel><Loading error={live.error} /></Panel>;
    let errors = health.requests.serverErrors;
    let minute = health.requests.perMinute;
    return <div className="flex flex-col gap-6">
        <Banner variant={errors ? 'error' : 'secondary'} icon={errors ? <WarningCircleIcon weight="fill" /> : <CheckCircleIcon weight="fill" />}
            title={errors ? `${number(errors)} server ${errors === 1 ? 'error' : 'errors'} since start` : 'Running normally'}
            description={`Up ${duration(health.process.uptime)} · started ${new Date(health.process.startedAt * 1000).toLocaleString()} · ${health.process.runtime} on ${health.process.platform} · pid ${health.process.pid} · refreshes every 10 seconds`} />
        <KpiStrip>
            <Kpi label="Online now" value={number(health.live.online)} note={number(health.live.streams) + ' live connections'} />
            <Kpi label="Requests, last minute" value={number(minute[minute.length - 1])} note={number(health.requests.total) + ' since start'} />
            <Kpi label="Latency p50" value={health.requests.p50 + ' ms'} note="last 500 API requests" />
            <Kpi label="Latency p95" value={health.requests.p95 + ' ms'} />
            <Kpi label="Client errors" value={number(health.requests.clientErrors)} note="4xx since start" />
        </KpiStrip>
        <div className="grid gap-6 lg:grid-cols-2">
            <Panel title="API requests" description="Per minute, last hour">
                <MinuteChart values={minute} />
            </Panel>
            <Panel title="Memory">
                <div className="flex flex-col gap-5 py-1">
                    <Usage label="Process (RSS)" used={health.process.rss} total={health.host.totalMemory} note="Share of host memory" />
                    <Usage label="JS heap" used={health.process.heapUsed} total={Math.max(health.process.heapTotal, health.process.heapUsed)} />
                    <Usage label="Host in use" used={health.host.totalMemory - health.host.freeMemory} total={health.host.totalMemory} note={number(health.host.cpus) + ' CPUs' + (health.host.load.some(Boolean) ? ' · load ' + health.host.load.join(' ') : '')} />
                </div>
            </Panel>
            <Panel title="Database" description={`SQLite, ${health.database.journal} journal`}>
                <Rows>
                    <Row label="Database file">{bytes(health.database.file)}</Row>
                    <Row label="Write-ahead log">{bytes(health.database.wal)}</Row>
                    <Row label="Free pages">{bytes(health.database.free)}</Row>
                    <Row label="Attachments">{bytes(health.storage.files.bytes)} · {number(health.storage.files.count)} files</Row>
                    <Row label="Profile photos">{bytes(health.storage.photos.bytes)} · {number(health.storage.photos.count)} photos</Row>
                </Rows>
            </Panel>
            <Panel title="Records">
                <Rows>
                    {[['Members', health.tables.users], ['Bots', health.tables.bots], ['Chats', health.tables.chats], ['Messages', health.tables.messages], ['Active sessions', health.tables.sessions], ['Audit entries', health.tables.audit]].map(([label, value]) => <Row key={label} label={label}><span className="tabular-nums">{number(value)}</span></Row>)}
                </Rows>
            </Panel>
        </div>
    </div>;
}
