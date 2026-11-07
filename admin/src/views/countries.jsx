import { useState } from 'react';
import { Badge, Button, Input, Switch, Table } from '@cloudflare/kumo';
import { CaretRightIcon, FlagIcon, TrashIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { matches, number, paged } from '../lib/format.js';
import { toast, useAdmin } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Status, Toolbar } from '../components/ui.jsx';

export function filterCountries(entries, query, filter) {
    return entries.filter(entry => matches(query, entry.name, entry.country_code, entry.iso2) && (filter === 'all' || Boolean(entry.enabled) === (filter === 'enabled')));
}

export function countryBody(entry, fields) {
    return { id: entry?.id, name: fields.name, countryCode: fields.countryCode, iso2: fields.iso2, enabled: fields.enabled };
}

function SwitchRow({ label, description, checked, onCheckedChange }) {
    return <div className="flex items-center justify-between gap-4 rounded-lg px-3 py-3 ring ring-kumo-line">
        <span className="min-w-0">
            <span className="block text-sm font-medium text-kumo-default">{label}</span>
            <span className="block text-xs text-kumo-subtle">{description}</span>
        </span>
        <Switch aria-label={label} checked={checked} onCheckedChange={onCheckedChange} />
    </div>;
}

function CountryEditor({ entry, removing, onRemove, onClose }) {
    let { refresh } = useAdmin();
    let [fields, setFields] = useState(() => ({ name: entry?.name || '', countryCode: entry?.country_code || '', iso2: entry?.iso2 || '', enabled: !entry || Boolean(entry.enabled) }));
    let set = key => event => setFields(current => ({ ...current, [key]: event.target.value }));
    return <ActionDialog open onClose={onClose} icon={removing ? TrashIcon : FlagIcon} destructive={removing} title={removing ? 'Remove country override?' : entry ? 'Edit country' : 'Add country'} description={removing ? 'The original country entry, if any, will return to the phone picker.' : 'Flag code is optional. Use an existing two-letter code for its flag, or leave it empty. Prefixes longer than three digits support existing-account sign-in only.'} confirmLabel={removing ? 'Remove' : 'Save'} onConfirm={async () => {
        await api('/admin/countries', removing ? 'DELETE' : 'POST', removing ? { id: entry.id } : countryBody(entry, fields));
        onClose();
        await refresh();
        toast(removing ? 'Country override removed.' : 'Country saved.', 'success');
    }}>
        <Input label="Name" className="w-full" maxLength={80} required readOnly={removing} value={fields.name} onChange={set('name')} autoComplete="off" passwordManagerIgnore />
        <Input label="Dialing prefix" className="w-full" inputMode="numeric" maxLength={127} required readOnly={Boolean(entry)} value={fields.countryCode} onChange={set('countryCode')} autoComplete="off" passwordManagerIgnore />
        <Input label="Flag code" className="w-full" maxLength={2} readOnly={Boolean(entry)} value={fields.iso2} onChange={set('iso2')} autoComplete="off" passwordManagerIgnore />
        {removing ? null : <SwitchRow label="Show in phone picker" description="Hiding an entry does not disable existing accounts." checked={fields.enabled} onCheckedChange={value => setFields(current => ({ ...current, enabled: value }))} />}
        {entry && !removing ? <div className="flex items-center justify-between gap-4 border-t border-kumo-hairline pt-4">
            <span className="text-xs text-kumo-subtle">Removing the override brings back the original entry, if any.</span>
            <Button type="button" size="sm" variant="secondary-destructive" icon={TrashIcon} onClick={onRemove}>Remove override</Button>
        </div> : null}
    </ActionDialog>;
}

export default function Countries() {
    let { data } = useAdmin();
    let [query, setQuery] = useState('');
    let [filter, setFilter] = useState('all');
    let [page, setPage] = useState(1);
    let [editing, setEditing] = useState(null);
    let all = data.customCountries || [];
    let view = paged(filterCountries(all, query, filter), page, 12);
    return <>
        <Panel flush title={<>Countries & codes <Badge variant="secondary" className="ml-1 align-middle">{number(all.length)}</Badge></>} description="Custom entries appear in the phone picker. Manage local names, dialing prefixes, and picker visibility." actions={<Button size="sm" variant="primary" onClick={() => setEditing({ entry: null, removing: false })}>Add country</Button>}>
            <Toolbar query={query} onQuery={value => { setQuery(value); setPage(1); }} placeholder="Search countries or dialing codes…" filter={filter} onFilter={value => { setFilter(value); setPage(1); }} filters={[['all', 'All entries'], ['enabled', 'Enabled'], ['disabled', 'Disabled']]} />
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Name</Table.Head>
                        <Table.Head>Code</Table.Head>
                        <Table.Head>Flag</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(entry => <Table.Row key={entry.id}>
                        <Table.Cell className="text-sm font-medium text-kumo-default">{entry.name}</Table.Cell>
                        <Table.Cell className="font-mono text-sm tabular-nums text-kumo-default">+{entry.country_code}</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{entry.iso2 || 'None'}</Table.Cell>
                        <Table.Cell><Status>{entry.enabled ? 'Enabled' : 'Disabled'}</Status></Table.Cell>
                        <Table.Cell className="text-right"><Button size="sm" variant="ghost" onClick={() => setEditing({ entry, removing: false })}>Manage <CaretRightIcon size={13} /></Button></Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={FlagIcon} title="No matching custom countries" description="Try another search or filter, or add a country." />}
            <Pager view={view} setPage={setPage} noun="entries" />
        </Panel>
        {editing ? <CountryEditor key={editing.entry?.id || 'new'} entry={editing.entry} removing={editing.removing} onRemove={() => setEditing(current => ({ ...current, removing: true }))} onClose={() => setEditing(null)} /> : null}
    </>;
}
