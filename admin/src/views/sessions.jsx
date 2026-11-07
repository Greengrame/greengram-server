import { useState } from 'react';
import { Badge, Button, Select, Table } from '@cloudflare/kumo';
import { AndroidLogoIcon, AppleLogoIcon, DesktopIcon, DevicesIcon, GlobeIcon, LinuxLogoIcon, WindowsLogoIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { ago, date, deviceInfo, matches, number, paged } from '../lib/format.js';
import { toast, useLive } from '../lib/store.jsx';
import { useDialogs } from '../lib/dialogs.jsx';
import { ActionDialog, EmptyState, Loading, Pager, Panel, Person, Toolbar } from '../components/ui.jsx';

const osIcons = { iOS: AppleLogoIcon, macOS: AppleLogoIcon, Android: AndroidLogoIcon, Windows: WindowsLogoIcon, Linux: LinuxLogoIcon };

export function filterSessions(sessions, query, filter, now = Date.now() / 1000) {
    return sessions.filter(session => matches(query, session.name, session.username, deviceInfo(session.userAgent).name) && (filter === 'all' || filter === 'online' && session.online || filter === 'idle' && now - session.lastActive > 7 * 86400));
}

export default function Sessions() {
    let live = useLive('/admin/sessions');
    let dialogs = useDialogs();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [revoking, setRevoking] = useState(null);
    let [pruning, setPruning] = useState(false);
    let [idleDays, setIdleDays] = useState('30');
    let all = live.data?.sessions;
    if (!all) return <Panel><Loading error={live.error} /></Panel>;
    let online = all.filter(session => session.online);
    let view = paged(filterSessions(all, query, filter), page, 12);
    return <>
        <Panel flush title={<>Signed-in devices <Badge variant="secondary" className="ml-1 align-middle">{number(all.length)}</Badge></>} description={`${number(online.length)} online now across ${number(new Set(all.map(session => session.userId)).size)} members.`} actions={<Button size="sm" variant="secondary" onClick={() => setPruning(true)}>Sign out idle devices</Button>}>
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search by member or device…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All sessions'], ['online', 'Online now'], ['idle', 'Idle for 7+ days']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Device</Table.Head>
                        <Table.Head>Member</Table.Head>
                        <Table.Head>Last active</Table.Head>
                        <Table.Head>Signed in</Table.Head>
                        <Table.Head>Expires</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(session => {
                        let device = deviceInfo(session.userAgent);
                        let Icon = osIcons[device.os] || (device.browser === 'Browser' ? DesktopIcon : GlobeIcon);
                        return <Table.Row key={session.id}>
                            <Table.Cell>
                                <span className="flex items-center gap-3">
                                    <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-kumo-recessed text-kumo-subtle"><Icon size={16} /></span>
                                    <span className="min-w-0">
                                        <span className="block text-sm font-medium text-kumo-default">{device.name}</span>
                                        <span className="block max-w-72 truncate text-xs text-kumo-subtle" title={session.userAgent}>{session.isCurrent ? 'This session' : session.online ? 'Online' : session.userAgent}</span>
                                    </span>
                                </span>
                            </Table.Cell>
                            <Table.Cell><Person user={{ id: session.userId, name: session.name, username: session.username, avatarPhotoId: session.avatarPhotoId }} onOpen={() => dialogs.open('member', { id: session.userId })} /></Table.Cell>
                            <Table.Cell>{session.online ? <Badge variant="success" appearance="dot">Now</Badge> : <span className="text-sm text-kumo-default">{ago(session.lastActive)}</span>}</Table.Cell>
                            <Table.Cell className="text-sm text-kumo-subtle">{date(session.createdAt)}</Table.Cell>
                            <Table.Cell className="text-sm text-kumo-subtle">{date(session.expiresAt)}</Table.Cell>
                            <Table.Cell className="text-right">{session.isCurrent ? null : <Button size="sm" variant="ghost" onClick={() => setRevoking(session)}>Terminate</Button>}</Table.Cell>
                        </Table.Row>;
                    })}
                </Table.Body>
            </Table> : <EmptyState icon={DevicesIcon} title="No sessions found" description="Try another search or filter." />}
            <Pager view={view} setPage={setPage} noun="sessions" />
        </Panel>
        <ActionDialog open={Boolean(revoking)} onClose={() => setRevoking(null)} icon={DevicesIcon} destructive title="Terminate this session?" description={revoking ? `${deviceInfo(revoking.userAgent).name} for ${revoking.name} · @${revoking.username}. The device will need to sign in again.` : ''} confirmLabel="Terminate" onConfirm={async () => {
            await api('/admin/sessions/' + revoking.id, 'DELETE', {});
            setRevoking(null);
            await live.reload();
            toast('Session terminated.', 'success');
        }} />
        <ActionDialog open={pruning} onClose={() => setPruning(false)} icon={DevicesIcon} destructive title="Sign out idle devices" description="Ends every session that has not been used for the chosen time. Your current session is kept." confirmLabel="Sign out" onConfirm={async () => {
            let result = await api('/admin/sessions', 'DELETE', { idleDays: Number(idleDays) });
            setPruning(false);
            await live.reload();
            toast(result.revoked ? `Signed out ${number(result.revoked)} sessions.` : 'No sessions were idle that long.', 'success');
        }}>
            <Select label="Idle for at least" hideLabel={false} value={idleDays} onValueChange={value => setIdleDays(value ?? '30')} items={{ 7: '7 days', 30: '30 days', 90: '90 days' }} />
        </ActionDialog>
    </>;
}
