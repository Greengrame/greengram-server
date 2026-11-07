import { useState } from 'react';
import { Badge, Button, Input, Select, Table } from '@cloudflare/kumo';
import { DiamondIcon, TrashIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { number, paged } from '../lib/format.js';
import { toast, toastError, useAdmin } from '../lib/store.jsx';
import { ActionDialog, EmptyState, Pager, Panel, Status } from '../components/ui.jsx';

// number-only options are left out for usernames; the server ignores them there anyway
export function fragmentListingBody(fields) {
    let body = { kind: fields.kind, value: fields.value, amount: fields.amount };
    if (fields.kind === 'number') Object.assign(body, { countryCode: fields.countryCode, displayFormat: fields.displayFormat });
    return body;
}

export function fragmentOwner(item, users) {
    return item.ownerId ? users.find(user => user.id === item.ownerId)?.name || '#' + item.ownerId : 'Administrator inventory';
}

function ListingDialog({ item, onClose }) {
    let { refresh } = useAdmin();
    let [fields, setFields] = useState(() => ({ kind: item?.kind || 'username', value: item?.title || '', amount: item?.amount || '', countryCode: item?.countryCode || '', displayFormat: item?.displayTitle || '' }));
    let set = key => event => setFields(current => ({ ...current, [key]: event.target.value }));
    return <ActionDialog open onClose={onClose} icon={DiamondIcon} title="Reserve an identifier" description="The identifier stays reserved until purchased. Proceeds are charged from the buyer's Wallet." confirmLabel="Reserve and list" onConfirm={async () => {
        await api('/admin/fragment/listing', 'POST', fragmentListingBody(fields));
        await refresh();
        onClose();
        toast('Identifier reserved and listed.', 'success');
    }}>
        <Select label="Type" hideLabel={false} className="w-full" value={fields.kind} onValueChange={value => setFields(current => ({ ...current, kind: value ?? 'username' }))} items={{ username: 'Username', number: 'Number' }} />
        <Input label="Identifier" className="w-full" required placeholder="@username or +number" description={fields.kind === 'username' ? 'Short and digits-only names like @ab or @777 can only be listed from here.' : undefined} value={fields.value} onChange={set('value')} autoComplete="off" passwordManagerIgnore />
        <Input label="Price in GRAM" className="w-full" required inputMode="decimal" placeholder="1" value={fields.amount} onChange={set('amount')} autoComplete="off" passwordManagerIgnore />
        {fields.kind === 'number' ? <>
            <Input label="Country code" className="w-full" inputMode="numeric" placeholder="888" value={fields.countryCode} onChange={set('countryCode')} autoComplete="off" passwordManagerIgnore />
            <Input label="Display format" className="w-full" placeholder="+888 1234 5678" value={fields.displayFormat} onChange={set('displayFormat')} autoComplete="off" passwordManagerIgnore />
        </> : null}
    </ActionDialog>;
}

export default function Fragment() {
    let { data, refresh } = useAdmin();
    let [page, setPage] = useState(1);
    let [listing, setListing] = useState(null);
    let [deleting, setDeleting] = useState(null);
    let [unlisting, setUnlisting] = useState(null);
    let items = data.fragmentInventory || [];
    let view = paged(items, page, 12);
    async function unlist(item) {
        setUnlisting(item.id);
        try {
            await api('/admin/fragment/listing', 'DELETE', { itemId: item.id, remove: false });
            await refresh();
            toast('Listing removed. The identifier stays reserved.', 'success');
        } catch (error) { toastError(error); }
        finally { setUnlisting(null); }
    }
    return <>
        <Panel flush title={<>Fragment inventory <Badge variant="secondary" className="ml-1 align-middle">{number(items.length)}</Badge></>} description="Reserve available identifiers for sale. Existing account names and sign-in numbers are protected." actions={<Button size="sm" variant="primary" onClick={() => setListing({})}>Reserve identifier</Button>}>
            {view.total ? <Table>
                <Table.Header>
                    <Table.Row>
                        <Table.Head>Identifier</Table.Head>
                        <Table.Head>Price</Table.Head>
                        <Table.Head>Owner</Table.Head>
                        <Table.Head>Status</Table.Head>
                        <Table.Head><span className="sr-only">Actions</span></Table.Head>
                    </Table.Row>
                </Table.Header>
                <Table.Body>
                    {view.items.map(item => <Table.Row key={item.id}>
                        <Table.Cell>
                            <span className="block text-sm font-medium text-kumo-default">{item.title}</span>
                        </Table.Cell>
                        <Table.Cell className="text-sm tabular-nums text-kumo-default">{item.amount} GRAM</Table.Cell>
                        <Table.Cell className="text-sm text-kumo-subtle">{fragmentOwner(item, data.users)}</Table.Cell>
                        <Table.Cell><Status>{item.listed ? 'For Sale' : 'Unlisted'}</Status></Table.Cell>
                        <Table.Cell className="text-right">
                            <span className="inline-flex items-center gap-1">
                                {item.listed ? <Button size="sm" variant="ghost" loading={unlisting === item.id} onClick={() => unlist(item)}>Unlist</Button> : !item.ownerId ? <Button size="sm" variant="ghost" onClick={() => setListing(item)}>Relist</Button> : null}
                                {!item.ownerId ? <Button size="sm" variant="ghost" shape="square" icon={TrashIcon} aria-label={'Delete ' + item.title} className="text-kumo-danger" onClick={() => setDeleting(item)} /> : null}
                            </span>
                        </Table.Cell>
                    </Table.Row>)}
                </Table.Body>
            </Table> : <EmptyState icon={DiamondIcon} title="No reserved identifiers" description="Create a listing with Reserve identifier." />}
            <Pager view={view} setPage={setPage} noun="identifiers" />
        </Panel>
        {listing ? <ListingDialog key={listing.id || 'new'} item={listing.id ? listing : undefined} onClose={() => setListing(null)} /> : null}
        <ActionDialog open={Boolean(deleting)} onClose={() => setDeleting(null)} icon={TrashIcon} destructive title="Delete this reservation?" description={deleting ? `${deleting.title} leaves the administrator inventory and any open quotes for it are cancelled.` : ''} confirmLabel="Delete" onConfirm={async () => {
            await api('/admin/fragment/listing', 'DELETE', { itemId: deleting.id, remove: true });
            setDeleting(null);
            await refresh();
            toast('Reservation deleted.', 'success');
        }} />
    </>;
}
