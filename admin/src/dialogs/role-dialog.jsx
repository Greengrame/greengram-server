import { useState } from 'react';
import { Select } from '@cloudflare/kumo';
import { ShieldCheckIcon } from '@phosphor-icons/react';
import { api } from '../lib/api.js';
import { toast, useAdmin } from '../lib/store.jsx';
import { ActionDialog } from '../components/ui.jsx';

export default function RoleDialog({ id, onClose }) {
    let { data, refresh } = useAdmin();
    let user = data.users.find(member => member.id === id);
    let [role, setRole] = useState(user?.role === 'admin' ? 'admin' : 'member');
    let reference = useState(() => crypto.randomUUID())[0];
    return <ActionDialog open onClose={onClose} icon={ShieldCheckIcon} title="Change member role" description={user ? `${user.name} · @${user.username}` : ''} confirmLabel="Apply change" confirmDisabled={!user} onConfirm={async () => {
        try { await api('/admin', 'PATCH', { action: 'role', id, reference: reference + '-' + id, role }); }
        catch (error) {
            await refresh().catch(() => {});
            throw error;
        }
        onClose();
        await refresh();
        toast('Changes saved.', 'success');
    }}>
        <Select label="Account role" hideLabel={false} description="Administrators can manage accounts, balances, and workspace settings." value={role} onValueChange={value => setRole(value ?? 'member')} items={{ member: 'Member', admin: 'Administrator' }} />
    </ActionDialog>;
}
