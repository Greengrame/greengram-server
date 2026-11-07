import { useState } from 'react';
import { Badge, Button, InputArea, Radio, Text } from '@cloudflare/kumo';
import { MegaphoneIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { dateTime, number } from '../lib/format.js';
import { logoUrl, toast, useAdmin, useLive } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Loading, Panel } from '../components/ui.jsx';

export const audiences = { all: ['All members', 'Everyone with an active account'], active: ['Active this week', 'Active in the last 7 days'], premium: ['Premium members', 'Members with an active subscription'], admins: ['Administrators', 'Useful for a test run'] };

function people(count) {
    return `${number(count)} ${count === 1 ? 'person' : 'people'}`;
}

export default function Broadcast() {
    let { data, branding, refresh } = useAdmin();
    let [audience, setAudience] = useState('all');
    let [text, setText] = useState('');
    let [confirming, setConfirming] = useState(false);
    let reach = useLive('/admin/broadcasts?audience=' + audience);
    let history = useLive('/admin/broadcasts');
    let recipients = reach.data?.audience === audience ? reach.data.recipients : null;
    let limit = data.settings.messageMaxLength;
    let ready = Boolean(text.trim()) && Boolean(recipients);
    return <div className="flex flex-col gap-6">
        <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(280px,2fr)]">
            <Panel title="New announcement" description={`Arrives as a direct message from the ${data.settings.siteName} account.`}>
                <form className="flex flex-col gap-5" onSubmit={event => { event.preventDefault(); if (ready) setConfirming(true); }} autoComplete="off" data-1p-ignore>
                    <Radio.Group legend="Audience" value={audience} onValueChange={value => setAudience(value)}>
                        {Object.entries(audiences).map(([value, [label, hint]]) => <Radio.Item key={value} appearance="card" value={value} label={label} description={hint} />)}
                    </Radio.Group>
                    <InputArea label="Message" value={text} onValueChange={setText} maxLength={limit} minRows={6} autoResize maxRows={14} placeholder="What do your members need to know?" description={`${number(text.length)} / ${number(limit)}`} className="w-full" />
                    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-kumo-hairline pt-4">
                        <Text variant="secondary" size="sm">{recipients === null ? 'Counting recipients…' : <>Reaches <strong className="font-medium text-kumo-default">{people(recipients)}</strong></>}</Text>
                        <Button type="submit" variant="primary" icon={MegaphoneIcon} disabled={!ready}>Review and send</Button>
                    </div>
                </form>
            </Panel>
            <Panel title="Preview" className="lg:sticky lg:top-0">
                <div className="overflow-hidden rounded-lg ring ring-kumo-hairline">
                    <div className="flex items-center gap-3 border-b border-kumo-hairline bg-kumo-elevated px-3 py-2.5">
                        <img src={logoUrl(branding)} alt="" className="size-8 rounded-full object-cover" />
                        <span>
                            <span className="block text-sm font-medium text-kumo-default">{data.settings.siteName}</span>
                            <span className="block text-xs text-kumo-subtle">service notifications</span>
                        </span>
                    </div>
                    <div className="flex min-h-56 items-end bg-kumo-recessed p-3">
                        <div className="relative max-w-[88%] rounded-xl rounded-bl-sm bg-kumo-base px-3 pt-2 pb-5 ring ring-kumo-hairline">
                            <p className="min-w-24 text-sm whitespace-pre-wrap break-words text-kumo-default">{text.trim() ? text : <span className="text-kumo-subtle">Your announcement appears here.</span>}</p>
                            <time className="absolute right-3 bottom-1 text-[11px] text-kumo-subtle">{new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>
                        </div>
                    </div>
                </div>
            </Panel>
        </div>
        <Panel flush title="Sent announcements" description="The latest 25, newest first.">
            {!history.data ? <Loading error={history.error} /> : history.data.broadcasts.length ? <ul className="divide-y divide-kumo-hairline">
                {history.data.broadcasts.map(entry => <li key={entry.id} className="flex flex-col gap-2 px-4 py-3">
                    <p className="line-clamp-3 text-sm whitespace-pre-wrap break-words text-kumo-default">{entry.text}</p>
                    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-kumo-subtle">
                        <Badge variant="outline">{audiences[entry.audience]?.[0] || entry.audience}</Badge>
                        <span>{people(entry.recipients)}</span>
                        <span>{entry.actor}</span>
                        <time>{dateTime(entry.createdAt)}</time>
                    </div>
                </li>)}
            </ul> : <EmptyState icon={MegaphoneIcon} title="Nothing sent yet" description="Announcements you send show up here." />}
        </Panel>
        <ActionDialog open={confirming} onClose={() => setConfirming(false)} icon={MegaphoneIcon} title={`Send to ${people(recipients || 0)}?`} description={`Each recipient gets a direct message from ${data.settings.siteName}. It cannot be unsent from here.`} confirmLabel="Send now" onConfirm={async () => {
            let result = await api('/admin/broadcasts', 'POST', { text: text.trim(), audience });
            setConfirming(false);
            setText('');
            await refresh();
            toast(`Announcement sent to ${people(result.recipients)}.`, 'success');
        }}>
            <blockquote className="max-h-48 overflow-y-auto rounded-md border-l-2 border-kumo-brand bg-kumo-recessed px-3 py-2 text-sm whitespace-pre-wrap break-words text-kumo-default">{text.trim()}</blockquote>
        </ActionDialog>
    </div>;
}
