import { useState } from 'react';
import { Input, Select, Switch } from '@cloudflare/kumo';
import { CrownIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { premiumActionData, premiumDateValue } from '../lib/format.js';
import { toast, useAdmin } from '../lib/store.jsx';
import { ActionDialog } from '../components/ui.jsx';

// a single member prefills from the current subscription; bulk grants start fresh
export function premiumDefaults(user = {}, now = Math.floor(Date.now() / 1000)) {
    let unlimited = Boolean(user.premiumUnlimited);
    let starts = user.premiumStartsAt || now;
    let ends = !unlimited && user.premiumUntil > starts ? user.premiumUntil : starts + 30 * 86400;
    return { unlimited, mode: 'duration', days: '30', hours: '0', minutes: '0', startsAt: premiumDateValue(starts), endsAt: premiumDateValue(ends) };
}

function Note({ children }) {
    return <p className="text-xs text-kumo-subtle">{children}</p>;
}

// onSaved lets a caller (the members bulk bar) clear its selection after every grant succeeded
export default function PremiumDialog({ ids = [], onClose, onSaved }) {
    let { data, refresh } = useAdmin();
    let user = data.users.find(member => member.id === ids[0]);
    let [values, setValues] = useState(() => premiumDefaults(ids.length === 1 ? user : {}));
    let reference = useState(() => crypto.randomUUID())[0];
    let timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    let exact = values.mode === 'dates';
    let locked = values.unlimited;
    let set = key => value => setValues(current => ({ ...current, [key]: value }));
    let field = key => ({ value: values[key], onChange: event => set(key)(event.target.value), autoComplete: 'off', passwordManagerIgnore: true });
    return <ActionDialog open onClose={onClose} icon={CrownIcon} title={'Manage Premium' + (ids.length > 1 ? ` · ${ids.length} members` : '')} description={ids.length === 1 && user ? `${user.name} · @${user.username}` : 'This change applies to the selected members.'} confirmLabel="Apply change" onConfirm={async () => {
        let premium = premiumActionData(values);
        let completed = 0;
        try {
            for (let id of ids) {
                await api('/admin', 'PATCH', { action: 'premium', id, reference: reference + '-' + id, ...premium });
                completed++;
            }
        } catch (error) {
            await refresh().catch(() => {});
            throw new Error((completed ? `${completed} changes saved. ` : '') + error.message);
        }
        onSaved?.();
        onClose();
        await refresh();
        toast('Changes saved.', 'success');
    }}>
        <div className="flex items-start justify-between gap-4 rounded-lg ring ring-kumo-line px-3 py-3">
            <div className="min-w-0">
                <p className="text-sm font-medium text-kumo-default">Unlimited Premium</p>
                <Note>Premium stays active until you revoke it.</Note>
            </div>
            <Switch aria-label="Unlimited Premium" checked={values.unlimited} onCheckedChange={set('unlimited')} />
        </div>
        <Select label="Set Premium by" hideLabel={false} value={values.mode} disabled={locked} onValueChange={value => set('mode')(value ?? 'duration')} items={{ duration: 'Duration', dates: 'Start and end dates' }} />
        {exact ? <div className="flex flex-col gap-3">
            <Input label="Starts at" type="datetime-local" step={60} required={!locked} disabled={locked} {...field('startsAt')} />
            <Input label="Ends at" type="datetime-local" step={60} required={!locked} disabled={locked} {...field('endsAt')} />
            <Note>Dates use your local time zone: {timezone}. A future start schedules Premium.</Note>
        </div> : <div className="flex flex-col gap-3">
            <div className="grid grid-cols-3 gap-3">
                <Input label="Days" type="number" min={0} step={1} required={!locked} disabled={locked} className="w-full" {...field('days')} />
                <Input label="Hours" type="number" min={0} max={23} step={1} disabled={locked} className="w-full" {...field('hours')} />
                <Input label="Minutes" type="number" min={0} max={59} step={1} disabled={locked} className="w-full" {...field('minutes')} />
            </div>
            <Note>Starts now and replaces the current expiry. Set all three values to 0 to revoke Premium.</Note>
        </div>}
    </ActionDialog>;
}
