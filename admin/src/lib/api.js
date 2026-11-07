// the messenger can hold several signed-in accounts; ?account=N picks the slot
export function accountSlot() {
    return Number(new URLSearchParams(globalThis.location?.search || '').get('account')) || 1;
}

export async function api(path, method = 'GET', body) {
    let response = await fetch('/api' + path, { method, headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), 'X-Greengram-Account': String(accountSlot()) }, body: body ? JSON.stringify(body) : undefined });
    let data = await response.json();
    if (response.status === 401) location.replace('/login?next=admin');
    if (!response.ok) throw new Error(data.error);
    return data;
}

export async function uploadLogo(file) {
    let response;
    let data;
    if (file.size > 5242880) throw new Error('Instance logos can be up to 5 MB.');
    response = await fetch('/api/admin/branding/logo', { method: 'POST', headers: { 'Content-Type': file.type || 'application/octet-stream', 'X-Greengram-Account': String(accountSlot()) }, body: file });
    data = await response.json();
    if (!response.ok) throw new Error(data.error || 'Logo upload failed.');
    return data;
}
