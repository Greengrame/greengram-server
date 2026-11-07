export function number(value) { return Number(value || 0).toLocaleString(); }

export function date(value) {
    let parsed = typeof value === 'number' || typeof value === 'string' && /^\d+$/.test(value) ? new Date(Number(value) * 1000) : new Date(value);
    return value && Number.isFinite(parsed.getTime()) ? parsed.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' }) : '—';
}

export function dateTime(value) {
    let parsed = new Date(Number(value) * 1000);
    return value && Number.isFinite(parsed.getTime()) ? parsed.toLocaleString([], { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';
}

export function premiumDateTime(value) {
    let parsed = new Date(Number(value) * 1000);
    return value && Number.isFinite(parsed.getTime()) ? parsed.toLocaleString([], { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' }) : '—';
}

export function ago(seconds, now = Date.now() / 1000) {
    let elapsed = Math.floor(now - Number(seconds || 0));
    if (!seconds) return '—';
    if (elapsed < 60) return 'just now';
    if (elapsed < 3600) return Math.floor(elapsed / 60) + 'm ago';
    if (elapsed < 86400) return Math.floor(elapsed / 3600) + 'h ago';
    if (elapsed < 30 * 86400) return Math.floor(elapsed / 86400) + 'd ago';
    return date(seconds);
}

export function duration(seconds) {
    let value = Math.max(0, Math.floor(Number(seconds) || 0));
    let days = Math.floor(value / 86400);
    let hours = Math.floor(value % 86400 / 3600);
    let minutes = Math.floor(value % 3600 / 60);
    return days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : minutes ? `${minutes}m` : `${value}s`;
}

export function bytes(value) {
    let size = Number(value || 0);
    let unit = 0;
    while (size >= 1024 && unit < 4) { size /= 1024; unit++; }
    return (unit ? size.toFixed(size < 10 ? 1 : 0) : size) + ' ' + ['B', 'KB', 'MB', 'GB', 'TB'][unit];
}

// null means there was nothing to compare against
export function percentChange(current, before) {
    if (!before) return current ? null : 0;
    return Math.round((current - before) / before * 100);
}

export function shortDay(day) {
    return new Date(day + 'T00:00:00Z').toLocaleDateString([], { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

export function initials(name) {
    return String(name || '?').split(' ').map(word => word[0]).slice(0, 2).join('');
}

export function deviceInfo(agent) {
    let text = String(agent || '');
    let os = /iPhone|iPad/.test(text) ? 'iOS' : /Android/.test(text) ? 'Android' : /Windows/.test(text) ? 'Windows' : /Mac OS X|Macintosh/.test(text) ? 'macOS' : /Linux|X11/.test(text) ? 'Linux' : '';
    let browser = /Edg\//.test(text) ? 'Edge' : /OPR\//.test(text) ? 'Opera' : /Firefox\//.test(text) ? 'Firefox' : /Chrome\//.test(text) ? 'Chrome' : /Safari\//.test(text) ? 'Safari' : /^Bot|curl|bun/i.test(text) ? 'Script' : 'Browser';
    return { name: os ? browser + ' on ' + os : browser, os, browser };
}

export function premiumExpiry(user, prefix = '') { return user.premiumUnlimited ? 'Unlimited' : prefix + premiumDateTime(user.premiumUntil); }

export function subscriptionStatus(user, now = Math.floor(Date.now() / 1000)) {
    if (user.isPremium && user.premiumUnlimited) return 'Unlimited';
    if (user.premiumStartsAt > now && user.premiumUntil > user.premiumStartsAt) return 'Scheduled';
    if (user.isPremium && user.premiumUntil <= now + 7 * 86400) return 'Expiring Soon';
    if (user.isPremium) return 'Active';
    return user.premiumUntil > 0 ? 'Expired' : 'Free';
}

export function premiumRemaining(user, now = Math.floor(Date.now() / 1000)) {
    let remaining = Math.max(0, Math.ceil((user.premiumUntil - now) / 60));
    let days = Math.floor(remaining / 1440);
    let hours = Math.floor(remaining % 1440 / 60);
    let minutes = remaining % 60;
    if (user.premiumUnlimited && user.isPremium) return 'Unlimited';
    if (user.premiumStartsAt > now && user.premiumUntil > user.premiumStartsAt) return 'Starts ' + premiumDateTime(user.premiumStartsAt);
    if (!user.isPremium) return '—';
    return [days ? days + 'd' : '', hours ? hours + 'h' : '', minutes ? minutes + 'm' : ''].filter(Boolean).join(' ') || 'Less than 1m';
}

export function premiumDateValue(seconds) {
    let parsed = new Date(Number(seconds) * 1000);
    return Number.isFinite(parsed.getTime()) ? new Date(parsed.getTime() - parsed.getTimezoneOffset() * 60000).toISOString().slice(0, 16) : '';
}

// shapes the payload premiumWindow() on the server accepts: unlimited, exact dates, or a duration from now
export function premiumActionData(values) {
    let starts;
    let ends;
    let days;
    let hours;
    let minutes;
    if (values.unlimited) return { unlimited: true };
    if (values.mode === 'dates') {
        starts = Math.floor(new Date(String(values.startsAt || '')).getTime() / 1000);
        ends = Math.floor(new Date(String(values.endsAt || '')).getTime() / 1000);
        if (!Number.isSafeInteger(starts) || !Number.isSafeInteger(ends) || starts < 0 || starts >= ends) throw new Error('Choose valid start and end dates, with the end after the start.');
        return { startsAt: starts, endsAt: ends };
    }
    days = Number(values.days);
    hours = Number(values.hours || 0);
    minutes = Number(values.minutes || 0);
    if (!Number.isSafeInteger(days) || days < 0 || !Number.isSafeInteger(hours) || hours < 0 || hours > 23 || !Number.isSafeInteger(minutes) || minutes < 0 || minutes > 59) throw new Error('Choose whole days, hours from 0 to 23, and minutes from 0 to 59.');
    return { days, ...(hours ? { hours } : {}), ...(minutes ? { minutes } : {}) };
}

// leading = + - @ would turn into formulas in spreadsheet apps
export function csvCell(value) {
    return '"' + String(value ?? '').replace(/^[=+@\-\t\r]/, character => "'" + character).replaceAll('"', '""') + '"';
}

export function paged(items, page, size) {
    let pages = Math.max(1, Math.ceil(items.length / size));
    let current = Math.min(Math.max(1, page), pages);
    return { items: items.slice((current - 1) * size, current * size), page: current, pages, total: items.length, from: items.length ? (current - 1) * size + 1 : 0, to: Math.min(current * size, items.length) };
}

export function matches(query, ...fields) {
    return fields.join(' ').toLowerCase().includes(String(query || '').toLowerCase());
}

export const auditLabels = { delete_sticker_pack: 'deleted a sticker or emoji pack', simulated_crypto_transaction: 'created a crypto transaction', crypto_transfer: 'transferred crypto', disable: 'changed an account status', premium: 'updated a Premium subscription', stars: 'adjusted a Stars balance', role: 'changed a member role', broadcast: 'sent an announcement', appeal_rejected: 'rejected an appeal', reports_resolved: 'acted on reports', reports_dismissed: 'dismissed reports', report_delete_messages: 'deleted reported messages', report_ban: 'banned a reported member from a chat', admin_sessions_revoked: 'signed out sessions', delete_message: 'removed a message', delete_conversation: 'removed a conversation', update_settings: 'updated workspace settings' };

export function auditLabel(action) {
    return auditLabels[action] || String(action || '').replaceAll('_', ' ');
}

export function auditTarget(entry) {
    return ({ delete_message: 'Message', delete_conversation: 'Conversation', update_settings: 'Workspace', delete_sticker_pack: 'Pack', broadcast: 'Announcement account' }[entry.action] || 'Account') + ' #' + entry.target_id;
}

// drop internal simulation markers before showing audit details
export function auditDetails(details) {
    let parsed;
    try { parsed = JSON.parse(details); } catch { return String(details || ''); }
    return JSON.stringify(parsed, (key, value) => ['simulation', 'simulation_label', 'monetaryValue', 'monetary_value'].includes(key) ? undefined : value, 2);
}
