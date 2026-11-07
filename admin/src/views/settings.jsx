import { useEffect, useRef, useState } from 'react';
import { Button, Input, Switch, cn } from '@cloudflare/kumo';
import { ChatsCircleIcon, CheckIcon, CrownIcon, ImageIcon, ShieldCheckIcon, SquaresFourIcon } from '@phosphor-icons/react';
import { api, uploadLogo } from '../lib/api.js';
import { logoUrl, toast, toastError, useAdmin } from '../lib/store.jsx';
import { Panel } from '../components/ui.jsx';

// [name, title, description, min, max, unit]
export const settingSections = [
    { title: 'Messaging & groups', description: 'Set sensible boundaries for conversations.', icon: ChatsCircleIcon, fields: [
        ['messageMaxLength', 'Message length', 'Maximum length of new text messages.', 1, 10000, 'characters'],
        ['premiumMessageMaxLength', 'Premium message length', 'Maximum text length for members with active Premium.', 1, 10000, 'characters'],
        ['groupMemberLimit', 'Group size', 'Maximum members when creating a group.', 2, 500, 'members']
    ] },
    { title: 'Security & sign-in', description: 'Control session lifetimes and verification.', icon: ShieldCheckIcon, fields: [
        ['sessionDays', 'Default inactive session lifetime', 'Default inactivity limit for accounts without a Devices preference.', 1, 90, 'days'],
        ['loginAttemptsPerMinute', 'Password sign-in attempts', 'Maximum attempts per IP address each minute.', 3, 100, '/ minute'],
        ['otpExpirySeconds', 'Verification code lifetime', 'How long a newly issued code remains valid.', 60, 600, 'seconds'],
        ['maxOtpAttempts', 'Verification attempts', 'Incorrect attempts allowed for each code.', 3, 10, 'attempts'],
        ['qrExpirySeconds', 'QR code lifetime', 'Expired QR codes refresh automatically.', 30, 300, 'seconds']
    ] },
    { title: 'Premium & Stars', description: 'Configure your local member benefits.', icon: CrownIcon, note: 'These are local benefits. They do not connect to Telegram billing or cryptocurrency wallets.', fields: [
        ['defaultPremiumDays', 'Welcome Premium', 'Premium days granted to new accounts. Set 0 to disable.', 0, 3650, 'days'],
        ['premiumMonthlyStars', 'One month of Premium', 'Local Stars charged for a 30-day subscription.', 1, 1000000, 'Stars'],
        ['premiumQuarterlyStars', 'Three months of Premium', 'Local Stars charged for a 90-day subscription.', 1, 1000000, 'Stars'],
        ['premiumAnnualStars', 'One year of Premium', 'Local Stars charged for a 365-day subscription.', 1, 1000000, 'Stars'],
        ['starsPerUSD', 'Wallet to Stars rate', 'Conversion rate using current market prices.', 1, 1000000, 'Stars / USD'],
        ['starsAdjustmentLimit', 'Stars adjustment limit', 'Maximum Stars added or removed in one admin action.', 1, 1000000000, 'Stars']
    ] }
];

const fields = settingSections.flatMap(section => section.fields);

// every key the server sent goes back; everything but the name and the switch is a number
export function settingsData(settings, values) {
    let keys = new Set([...Object.keys(settings), ...fields.map(([key]) => key).filter(key => Object.hasOwn(values, key))]);
    return Object.fromEntries([...keys].map(key => [key, key === 'siteName' ? String(values.siteName ?? '') : key === 'registrationEnabled' ? Boolean(values.registrationEnabled) : Number(values[key])]));
}

export function settingsErrors(values) {
    let errors = {};
    let name = String(values.siteName ?? '').trim();
    if (!name || name.length > 50) errors.siteName = 'The instance name must contain 1–50 characters.';
    fields.forEach(([key, title, , min, max]) => {
        let raw = values[key];
        let value = Number(raw);
        if (!Object.hasOwn(values, key)) return;
        if (raw === '' || raw === null || raw === undefined || !Number.isSafeInteger(value) || value < min || value > max) errors[key] = `${title} must be a whole number from ${min.toLocaleString()} to ${max.toLocaleString()}.`;
    });
    return errors;
}

export function settingsDirty(settings, draft) {
    return Boolean(draft) && Object.keys(settings).some(key => String(draft[key]) !== String(settings[key]));
}

function NumberSetting({ field: [name, title, description, min, max, unit], value, error, onChange }) {
    let id = 'setting-' + name;
    return <div className="flex flex-col gap-2 py-4 sm:flex-row sm:items-start sm:justify-between sm:gap-6">
        <label htmlFor={id} className="min-w-0">
            <span className="block text-sm font-medium text-kumo-default">{title}</span>
            <span id={id + '-description'} className="block text-xs text-kumo-subtle">{description}</span>
        </label>
        <div className="flex shrink-0 flex-col gap-1 sm:items-end">
            <span className="flex items-center gap-2">
                <Input id={id} name={name} type="number" inputMode="numeric" min={min} max={max} step={1} required size="sm" className="w-32 tabular-nums" variant={error ? 'error' : 'default'} value={value ?? ''} onChange={event => onChange(name, event.target.value)} aria-invalid={Boolean(error)} aria-describedby={id + '-description' + (error ? ' ' + id + '-error' : '')} autoComplete="off" passwordManagerIgnore data-1p-ignore />
                <span className="min-w-20 text-xs text-kumo-subtle">{unit}</span>
            </span>
            {error ? <span id={id + '-error'} className="max-w-64 text-xs text-kumo-danger sm:text-right">{error}</span> : null}
        </div>
    </div>;
}

function SectionTitle({ icon: Icon, children }) {
    return <span className="flex items-center gap-2"><Icon size={16} className="text-kumo-subtle" />{children}</span>;
}

export default function Settings() {
    let { data, branding, refresh, setBranding } = useAdmin();
    let [draft, setDraft] = useState(null);
    let [saving, setSaving] = useState(false);
    let [uploading, setUploading] = useState(false);
    let [resetting, setResetting] = useState(false);
    let file = useRef(null);
    let settings = data.settings;
    let values = draft || settings;
    let dirty = settingsDirty(settings, draft);
    let errors = draft ? settingsErrors(values) : {};

    useEffect(() => {
        if (!dirty) return undefined;
        let warn = event => { event.preventDefault(); event.returnValue = ''; };
        window.addEventListener('beforeunload', warn);
        return () => window.removeEventListener('beforeunload', warn);
    }, [dirty]);

    function change(name, value) {
        setDraft(current => ({ ...(current || settings), [name]: value }));
    }

    async function upload(event) {
        let chosen = event.target.files?.[0];
        event.target.value = '';
        if (!chosen) return;
        setUploading(true);
        try {
            setBranding(await uploadLogo(chosen));
            await refresh();
            toast('Instance logo updated.', 'success');
        } catch (error) { toastError(error); }
        finally { setUploading(false); }
    }

    async function resetLogo() {
        setResetting(true);
        try {
            setBranding(await api('/admin/branding/logo', 'DELETE', {}));
            await refresh();
            toast('Telegram logo restored.', 'success');
        } catch (error) { toastError(error); }
        finally { setResetting(false); }
    }

    async function save(event) {
        event.preventDefault();
        let problems = settingsErrors(values);
        if (Object.keys(problems).length) {
            if (!draft) setDraft(settings);
            document.getElementById(problems.siteName ? 'instance-name' : 'setting-' + Object.keys(problems)[0])?.focus();
            toast('Fix the highlighted settings before saving.', 'warning');
            return;
        }
        setSaving(true);
        try {
            await api('/admin/settings', 'PATCH', settingsData(settings, values));
            setDraft(null);
            await refresh();
            toast('Workspace settings saved.', 'success');
        } catch (error) { toastError(error); }
        finally { setSaving(false); }
    }

    return <form onSubmit={save} noValidate autoComplete="off" data-1p-ignore className="flex flex-col gap-6">
        <div className="grid gap-6 xl:grid-cols-2">
            <Panel title={<SectionTitle icon={SquaresFourIcon}>General</SectionTitle>} description="Make this workspace your own.">
                <div className="flex flex-col gap-5">
                    <div className="flex items-center gap-4">
                        <img src={logoUrl(branding)} alt={values.siteName} className="size-16 shrink-0 rounded-full object-cover ring ring-kumo-line" />
                        <div className="flex min-w-0 flex-col gap-2">
                            <div className="flex flex-wrap gap-2">
                                <Button type="button" size="sm" variant="secondary" icon={ImageIcon} loading={uploading} onClick={() => file.current?.click()}>Set Instance Photo</Button>
                                <Button type="button" size="sm" variant="ghost" loading={resetting} disabled={!branding?.hasCustomLogo} onClick={resetLogo}>Use Default Logo</Button>
                            </div>
                            <span className="text-xs text-kumo-subtle">PNG, JPEG or WebP, up to 5 MB.</span>
                            <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" hidden onChange={upload} />
                        </div>
                    </div>
                    <Input id="instance-name" name="siteName" label="Instance name" description="Shown in administration and instance branding." error={errors.siteName} value={values.siteName ?? ''} onChange={event => change('siteName', event.target.value)} maxLength={50} required autoComplete="off" passwordManagerIgnore data-1p-ignore data-op-ignore />
                    <div className="flex items-start justify-between gap-6 border-t border-kumo-hairline pt-4">
                        <div className="min-w-0">
                            <span className="block text-sm font-medium text-kumo-default">Allow new registrations</span>
                            <span className="block text-xs text-kumo-subtle">Existing members can still sign in when registration is closed.</span>
                        </div>
                        <Switch aria-label="Allow new registrations" checked={Boolean(values.registrationEnabled)} onCheckedChange={checked => change('registrationEnabled', checked)} />
                    </div>
                </div>
            </Panel>
            {settingSections.map(section => <Panel key={section.title} title={<SectionTitle icon={section.icon}>{section.title}</SectionTitle>} description={section.description}>
                <div className="divide-y divide-kumo-hairline [&>*:first-child]:pt-0 [&>*:last-child]:pb-0">
                    {section.fields.map(field => <NumberSetting key={field[0]} field={field} value={values[field[0]]} error={errors[field[0]]} onChange={change} />)}
                </div>
                {section.note ? <p className="mt-4 rounded-md bg-kumo-recessed px-3 py-2 text-xs text-kumo-subtle">{section.note}</p> : null}
            </Panel>)}
        </div>
        <div className={cn('sticky bottom-4 z-10 flex flex-wrap items-center justify-between gap-3 rounded-lg bg-kumo-base px-4 py-3 shadow-lg ring', dirty ? 'ring-kumo-line' : 'ring-kumo-hairline')}>
            <span className="text-sm text-kumo-default" role="status">{dirty ? 'You have unsaved changes.' : 'Your workspace settings are up to date.'}</span>
            <div className="flex gap-2">
                <Button type="button" variant="secondary" disabled={!draft || saving} onClick={() => setDraft(null)}>Discard changes</Button>
                <Button type="submit" variant="primary" icon={CheckIcon} loading={saving} disabled={!dirty}>Save changes</Button>
            </div>
        </div>
    </form>;
}
