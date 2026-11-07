import { test, expect, mock } from 'bun:test';
import { fileURLToPath } from 'node:url';
import { createElement } from 'react';
import { renderToString } from 'react-dom/server';

const admin = path => fileURLToPath(new URL('../src/' + path, import.meta.url));
const now = Math.floor(Date.now() / 1000);
const users = [
    { id: 1, name: 'Admin', username: 'admin', role: 'admin', disabled: false, accountState: 'active', isPremium: false, premiumUntil: 0, stars: 0, createdAt: now - 86400 },
    { id: 2, name: '=Alice', username: 'alice', role: 'member', disabled: false, accountState: 'active', isPremium: true, premiumUnlimited: true, premiumUntil: 253402300799, stars: 100, createdAt: now - 3600, isVerified: true },
    { id: 3, name: 'Bob <b>', username: 'bob', role: 'member', disabled: true, accountState: 'locked', isPremium: false, premiumUntil: 1000, stars: 0, createdAt: now - 7200 },
    { id: 4, name: 'Expiring', username: 'expiring', role: 'member', disabled: false, accountState: 'active', isPremium: true, premiumUntil: now + 3600, stars: 0, createdAt: now - 60 }
];
const sessions = [
    { id: 'a'.repeat(64), userId: 2, name: '<Alice>', username: 'alice', userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) Version/18.1 Mobile Safari/604.1', lastActive: now - 10 * 86400, createdAt: now - 20 * 86400, expiresAt: now + 86400, online: false, isCurrent: false },
    { id: 'b'.repeat(64), userId: 1, name: 'Admin', username: 'admin', userAgent: 'Mozilla/5.0 (Windows NT 10.0) Chrome/141.0 Safari/537.36', lastActive: now, createdAt: now, expiresAt: now + 86400, online: true, isCurrent: true }
];
const data = {
    users,
    bots: [{ id: 8, name: 'Wallet', username: 'wallet', kind: 'wallet', ownerId: null }, { id: 9, name: 'Managed', username: 'managed_bot', kind: 'custom', ownerId: 2, disabled: true }],
    conversations: [{ id: 20, title: 'Lab', kind: 'group', member_count: 3, message_count: 7, visibility: 'public', owner: 'Admin', isVerified: true }],
    recent: [{ id: 61, user_id: 2, author: '=Alice', body: 'Moderation body 61', created_at: now, chat_title: 'Lab' }],
    audit: [{ id: 1, actor: 'Admin', action: 'simulated_crypto_transaction', target_id: 2, created_at: now, details: JSON.stringify({ simulation: true, amount: '1' }) }],
    starTransactions: [{ id: 5, user_id: 2, name: '=Alice', username: 'alice', amount: 50, reason: 'admin_credit', title: 'Admin', note: 'Reward', transactionId: 'tx-5', created_at: now }],
    fragmentInventory: [], customCountries: [{ id: 1, name: 'Testland', country_code: '999', iso2: 'TL', enabled: 1 }],
    settings: { siteName: '<Own & Instance>', registrationEnabled: true, messageMaxLength: 4096, starsAdjustmentLimit: 1000, sessionDays: 7 },
    chats: 4, messageCount: 25
};
const live = {
    '/admin/sessions': { sessions },
    '/admin/broadcasts?audience=all': { audience: 'all', recipients: 3 },
    '/admin/reports?status=open': {
        open: 2,
        reasons: [{ key: 'spam', label: 'Spam or scam' }, { key: 'violence', label: 'Violence' }],
        groups: [
            { key: 'u3', target: { key: 'u3', kind: 'user', id: 3, name: 'Bob <b>', username: 'bob', state: 'active', peerId: '3' }, open: 2, reporters: 2, latestAt: now, reasons: ['Spam or scam › Phishing'], canDelete: true, canFreeze: true, canBan: true, reports: [
                { id: 7, kind: 'message', status: 'open', reporter: { id: 2, name: '=Alice', username: 'alice' }, chat: { kind: 'group', id: 20, title: 'Lab' }, msgIds: [5], reason: ['spam', 'phishing'], reasonLabel: 'Spam or scam › Phishing', comment: '<script>card</script>', snapshot: { messages: [{ id: 5, from: 3, fromName: 'Bob <b>', date: now, text: 'buy <i>pills</i>', media: { type: 'video', id: '9007199254740993', mime: 'video/mp4', size: 2048 } }] }, actions: [], closedBy: '', closedAt: null, createdAt: now },
                { id: 6, kind: 'peer', status: 'open', reporter: { id: 4, name: 'Expiring', username: 'expiring' }, chat: { kind: 'user', id: 3, title: 'Bob <b>' }, msgIds: [], reason: ['other'], reasonLabel: 'Other', comment: '', snapshot: { peer: { name: 'Bob <b>', username: 'bob', about: 'about me' } }, actions: [], closedBy: '', closedAt: null, createdAt: now - 60 }
            ] },
            { key: 'c20', target: { key: 'c20', kind: 'channel', id: 20, name: 'News', username: '', peerId: '-1000000000020' }, open: 1, reporters: 1, latestAt: now - 3600, reasons: ['Copyright'], canDelete: false, canFreeze: false, canBan: false, reports: [
                { id: 5, kind: 'peer', status: 'open', reporter: { id: 2, name: '=Alice', username: 'alice' }, chat: { kind: 'channel', id: 20, title: 'News' }, msgIds: [], reason: ['copyright'], reasonLabel: 'Copyright', comment: 'pirated', snapshot: {}, actions: [], closedBy: '', closedAt: null, createdAt: now - 3600 }
            ] }
        ]
    },
    '/admin/broadcasts': { broadcasts: [{ id: 1, audience: 'admins', recipients: 1, actor: 'Admin', createdAt: now, text: '<b>hi</b>' }] }
};

mock.module(admin('lib/store.jsx'), () => ({ useAdmin: () => ({ user: users[0], data, branding: { logoUrl: '/branding/logo?v=revision', hasCustomLogo: true }, refresh: async () => data, setBranding() {}, version: 0 }), useLive: path => ({ data: live[path] || null, error: null, loading: false, reload: async () => {} }), toast() {}, toastError() {}, logoUrl: branding => branding?.logoUrl || '/branding/logo', toasts: {} }));
mock.module(admin('lib/dialogs.jsx'), () => ({ useDialogs: () => ({ open() {}, close() {} }) }));

const format = await import(admin('lib/format.js'));
const members = await import(admin('views/members.jsx'));
const premium = await import(admin('views/premium.jsx'));
const sessionsView = await import(admin('views/sessions.jsx'));
const broadcast = await import(admin('views/broadcast.jsx'));
const settings = await import(admin('views/settings.jsx'));
const packs = await import(admin('views/packs.jsx'));
const audit = await import(admin('views/audit.jsx'));
const moderation = await import(admin('views/moderation.jsx'));
const bots = await import(admin('views/bots.jsx'));
const countries = await import(admin('views/countries.jsx'));
const stars = await import(admin('views/stars.jsx'));
const fragment = await import(admin('views/fragment.jsx'));
const gifts = await import(admin('views/gifts.jsx'));
const reportsView = await import(admin('views/reports.jsx'));
const memberDialog = await import(admin('dialogs/member-dialog.jsx'));
const premiumDialog = await import(admin('dialogs/premium-dialog.jsx'));
const starsDialog = await import(admin('dialogs/stars-dialog.jsx'));

const render = component => renderToString(createElement(component));

test('premium payloads accept durations, exact dates and unlimited access and reject invalid ranges', () => {
    expect(format.premiumActionData({ unlimited: true, days: 5 })).toEqual({ unlimited: true });
    expect(format.premiumActionData({ mode: 'duration', days: '0', hours: '2', minutes: '30' })).toEqual({ days: 0, hours: 2, minutes: 30 });
    expect(format.premiumActionData({ mode: 'duration', days: '30', hours: '0', minutes: '0' })).toEqual({ days: 30 });
    expect(() => format.premiumActionData({ mode: 'duration', days: '1', hours: '24' })).toThrow('hours from 0 to 23');
    expect(format.premiumActionData({ mode: 'dates', startsAt: '2026-01-01T10:00', endsAt: '2026-02-01T10:00' }).endsAt).toBeGreaterThan(format.premiumActionData({ mode: 'dates', startsAt: '2026-01-01T10:00', endsAt: '2026-02-01T10:00' }).startsAt);
    expect(() => format.premiumActionData({ mode: 'dates', startsAt: '2026-02-01T10:00', endsAt: '2026-01-01T10:00' })).toThrow('end after the start');
    expect(() => format.premiumActionData({ mode: 'dates', startsAt: 'Invalid Date', endsAt: '' })).toThrow();
    expect(premiumDialog.premiumDefaults({ premiumStartsAt: now, premiumUntil: now + 86400 }).endsAt).toBe(format.premiumDateValue(now + 86400));
    expect(premiumDialog.premiumDefaults({ premiumUnlimited: true }).unlimited).toBe(true);
    expect(format.subscriptionStatus(users[3], now)).toBe('Expiring Soon');
    expect(format.premiumRemaining(users[1], now)).toBe('Unlimited');
});

test('member list filters, exports formula-safe CSV and never lets admins be bulk-selected', () => {
    expect(members.filterMembers(users, '', 'disabled').map(user => user.id)).toEqual([3]);
    expect(members.filterMembers(users, 'ali', 'all').map(user => user.id)).toEqual([2]);
    expect(members.filterMembers(users, '', 'admin').map(user => user.id)).toEqual([1]);
    expect(members.membersCsv(users)).toContain(`"'=Alice"`);
    expect(members.membersCsv(users).split('\r\n')[0]).toBe('"ID","Name","Username","Role","Status","Premium until","Stars"');
    let html = render(members.default);
    expect(html).toContain('Revoke verification');
    expect(html).toContain('Bob &lt;b&gt;');
    expect(html).not.toContain('Bob <b>');
});

test('premium view filters subscriptions and labels unlimited access', () => {
    expect(premium.filterPremium(users, '', 'expiring', now).map(user => user.id)).toEqual([4]);
    expect(premium.filterPremium(users, '', 'expired', now).map(user => user.id)).toEqual([3]);
    expect(premium.filterPremium(users, '', 'free', now).map(user => user.id)).toEqual([1]);
    let html = render(premium.default);
    expect(html).toContain('Premium Subscriptions');
    expect(html).toContain('Time Remaining');
    expect(html).toContain('Never');
});

test('session termination never targets the current session or a malformed id', () => {
    expect(memberDialog.canTerminate('all', [])).toBe(true);
    expect(memberDialog.canTerminate('not-a-session', [])).toBe(false);
    expect(memberDialog.canTerminate('a'.repeat(64), [{ id: 'a'.repeat(64), isCurrent: true }])).toBe(false);
    expect(memberDialog.canTerminate('b'.repeat(64), [{ id: 'a'.repeat(64), isCurrent: true }])).toBe(true);
    expect(sessionsView.filterSessions(sessions, '', 'online', now)).toHaveLength(1);
    expect(sessionsView.filterSessions(sessions, '', 'idle', now).map(session => session.userId)).toEqual([2]);
    expect(sessionsView.filterSessions(sessions, 'chrome on windows', 'all', now)).toHaveLength(1);
    let html = render(sessionsView.default);
    expect(html).toContain('&lt;Alice&gt;');
    expect(html).toContain('Safari on iOS');
    expect(html).toContain('This session');
    expect(html.match(/>Terminate</g)).toHaveLength(1);
});

test('stars review validates locally, blocks overdrafts and keeps the request id for retries', () => {
    let people = users.filter(user => !user.disabled);
    let first = starsDialog.reviewStarsTransaction({ people, recipientId: '2', direction: 'credit', amount: '50', note: ' Community reward ', requestId: 'request-1', limit: 1000 });
    expect(first.reviewed).toEqual({ recipientId: 2, amount: 50, note: 'Community reward', requestId: 'request-1' });
    expect(starsDialog.signedStars(first.reviewed.amount)).toBe('+50 Stars');
    expect(() => starsDialog.reviewStarsTransaction({ people, recipientId: '2', direction: 'debit', amount: '101', note: 'x', requestId: 'r', limit: 1000 })).toThrow('balance cannot become negative');
    expect(() => starsDialog.reviewStarsTransaction({ people, recipientId: '2', direction: 'credit', amount: '1001', note: 'x', requestId: 'r', limit: 1000 })).toThrow();
    expect(() => starsDialog.reviewStarsTransaction({ people, recipientId: '2', direction: 'credit', amount: '5', note: '  ', requestId: 'r', limit: 1000 })).toThrow();
    expect(stars.totalStars(users)).toBe(100);
    expect(stars.filterStarTransactions(data.starTransactions, 'tx-5', 'incoming')).toHaveLength(1);
    expect(stars.filterStarTransactions(data.starTransactions, '', 'outgoing')).toHaveLength(0);
});

test('sticker packs use server paging, static covers only and the account slot', () => {
    expect(packs.packQuery('duck', 'emoji', 2)).toBe('q=duck&page=2&kind=emoji');
    expect(packs.packQuery('', 'all', 1)).toBe('q=&page=1');
    expect(packs.packCover({ cover: { mime: 'image/webp', url: '/api/sticker-media/1' } }, 1)).toEqual({ src: '/api/sticker-media/1?account=1' });
    expect(packs.packCover({ cover: { mime: 'image/webp', url: 'https://evil.example/x' } }, 1)).toEqual({ label: 'PACK' });
    expect(packs.packCover({ cover: { mime: 'application/x-tgsticker', url: '/api/sticker-media/2' } }, 1)).toEqual({ label: 'TGS' });
    expect(packs.packPage({ packs: [], total: 45, pageSize: 20, page: 2 })).toMatchObject({ from: 21, to: 40, pages: 3 });
});

test('feeds filter by sender and action and audit details drop simulation markers', () => {
    expect(moderation.filterMessages(data.recent, 'body 61', 'all')).toHaveLength(1);
    expect(moderation.filterMessages(data.recent, '', moderation.senderValue(3))).toHaveLength(0);
    expect(audit.filterAudit(data.audit, '', 'simulated_crypto_transaction')).toHaveLength(1);
    expect(format.auditLabel('simulated_crypto_transaction')).toBe('created a crypto transaction');
    expect(format.auditLabel('telegram_sticker_import')).toBe('telegram sticker import');
    expect(format.auditDetails(data.audit[0].details)).not.toContain('simulation');
    expect(format.auditDetails(data.audit[0].details)).toContain('"amount": "1"');
});

test('gifts send the exact payloads the server expects', () => {
    let list = [{ title: 'Heart', emoji: '❤️', enabled: true }, { title: 'Crown', emoji: '👑', enabled: false }];
    expect(gifts.filterGifts(list, 'cro', 'all')).toHaveLength(1);
    expect(gifts.filterGifts(list, '', 'enabled').map(gift => gift.title)).toEqual(['Heart']);
    expect(gifts.giftBody({ title: ' Moon ', emoji: ' 🌕 ', stars: '40', convertStars: '', stock: '', premiumOnly: false, enabled: true })).toEqual({ title: 'Moon', emoji: '🌕', stickerId: null, stars: 40, convertStars: 0, stock: null, premiumOnly: false, enabled: true, upgrade: null });
    expect(gifts.giftBody({ title: 'Moon', emoji: '🌕', stars: '40', convertStars: '30', stock: '0', premiumOnly: true, enabled: false })).toEqual({ title: 'Moon', emoji: '🌕', stickerId: null, stars: 40, convertStars: 30, stock: 0, premiumOnly: true, enabled: false, upgrade: null });
    // a custom emoji can stand in for the emoji, and its id must not pass through a number
    expect(gifts.giftBody({ title: 'Moon', emoji: ' ', stickerId: ' 5368741932475418251 ', stars: '40', convertStars: '', stock: '', premiumOnly: false, enabled: true })).toEqual({ title: 'Moon', emoji: '', stickerId: '5368741932475418251', stars: 40, convertStars: 0, stock: null, premiumOnly: false, enabled: true, upgrade: null });
    let picked = gifts.pickSticker({ title: 'Moon', emoji: '', stickerId: '' }, { id: '5368741932475418251', emoji: '🦊' });
    expect(picked.stickerId).toBe('5368741932475418251');
    expect(picked.emoji).toBe('🦊');
    expect(gifts.pickSticker({ title: 'Moon', emoji: '🌕', stickerId: '' }, { id: '5368741932475418251', emoji: '🦊' }).emoji).toBe('🌕');
});

test('an upgradable gift sends its collection, prices and supply', () => {
    let fields = { title: 'Pepe', emoji: '🐸', stars: '100', convertStars: '', stock: '', premiumOnly: false, enabled: true, upgradable: true, setId: '3', upgradeStars: '50', transferStars: '', supply: '1000' };
    expect(gifts.giftBody(fields).upgrade).toEqual({ setId: 3, stars: 50, transferStars: 0, supply: 1000 });
    expect(gifts.giftBody({ ...fields, upgradable: false }).upgrade).toBeNull();
    // an edit starts from what the gift has; a limited gift's stock is a fair supply
    expect(gifts.upgradeFields({ upgrade: { setId: 3, stars: 50, transferStars: 25, supply: 900, issued: 4 } })).toEqual({ upgradable: true, setId: '3', upgradeStars: '50', transferStars: '25', supply: '900' });
    expect(gifts.upgradeFields({ upgrade: null, stockTotal: 5000 })).toEqual({ upgradable: false, setId: '', upgradeStars: '', transferStars: '25', supply: '5000' });
    expect(gifts.upgradeFields(null).upgradable).toBe(false);
    // a blank supply is unlimited both ways
    expect(gifts.giftBody({ ...fields, supply: '' }).upgrade.supply).toBeNull();
    expect(gifts.upgradeFields({ upgrade: { setId: 3, stars: 50, transferStars: 25, supply: 0, issued: 12000 }, stockTotal: 5000 }).supply).toBe('');
    // another gift's collection is offered but can't be picked
    let items = gifts.collectionItems([{ id: 3, title: 'Plush Pepe', models: 50, issued: 4, supply: 900, giftId: 7 }, { id: 4, title: 'Durov\'s Cap', models: 55, issued: 0, supply: 0, giftId: null }], 9);
    expect(items['3'].disabled).toBe(true);
    expect(items['4'].disabled).toBe(false);
    expect(items['3'].label).toContain('Plush Pepe');
    expect(items['3'].label).toContain('4 of 900 issued');
    expect(items['4'].label).toContain('0 issued, unlimited');
    expect(gifts.collectionItems([{ id: 3, title: 'Plush Pepe', models: 50, issued: 4, supply: 900, giftId: 7 }], 7)['3'].disabled).toBe(false);
});

test('a gift sold as variants sends its market, and the commission is a percent', () => {
    let fields = { title: 'Pepe', emoji: '🐸', stars: '100', convertStars: '', stock: '', premiumOnly: false, enabled: true, upgradable: true, setId: '3', upgradeStars: '50', transferStars: '', supply: '', variants: true, baseStars: '', rarityMultiplier: '2.5' };
    // a blank base is the gift's price plus its upgrade, which the server takes as null
    expect(gifts.giftBody(fields).market).toEqual({ enabled: true, baseStars: null, rarityMultiplier: 2.5 });
    expect(gifts.giftBody({ ...fields, baseStars: '300', variants: false }).market).toEqual({ enabled: false, baseStars: 300, rarityMultiplier: 2.5 });
    // a plain gift has no collection to sell from
    expect(gifts.giftBody({ ...fields, upgradable: false }).market).toBeUndefined();
    expect(gifts.marketFields({ market: { enabled: true, baseStars: 300, rarityMultiplier: 2.5 } })).toEqual({ variants: true, baseStars: '300', rarityMultiplier: '2.5' });
    expect(gifts.marketFields({ market: { enabled: false, baseStars: null, rarityMultiplier: 3 } })).toEqual({ variants: false, baseStars: '', rarityMultiplier: '3' });
    expect(gifts.marketFields(null)).toEqual({ variants: false, baseStars: '', rarityMultiplier: '3' });
    expect(gifts.marketText({ enabled: true, offers: 12000, listed: 3 })).toBe('12,000 variants · 3 listed');
    expect(gifts.marketText({ enabled: true, offers: 0, listed: 0 })).toBe('Variants sold out');
    expect(gifts.marketText({ enabled: false, offers: -1, listed: 2 })).toBe('2 listed');
    expect(gifts.marketText({ enabled: false, offers: -1, listed: 0 })).toBe('—');
    expect(gifts.marketText(null)).toBe('—');
    expect(gifts.commissionPercent(100)).toBe('10');
    expect(gifts.commissionPercent(25)).toBe('2.5');
    expect(gifts.commissionBody('12.5')).toEqual({ commissionPermille: 125 });
});

test('bots, countries and fragment send the exact payloads the server expects', () => {
    expect(bots.filterBots(data.bots, '', 'builtin').map(bot => bot.id)).toEqual([8]);
    expect(bots.filterBots(data.bots, '', 'owned').map(bot => bot.id)).toEqual([9]);
    expect(bots.botPatch({ name: ' Bot ', username: ' bot_name ', bio: 'a', description: 'b', enabled: false })).toEqual({ name: 'Bot', username: 'bot_name', bio: 'a', description: 'b', disabled: true });
    expect(countries.filterCountries(data.customCountries, '999', 'enabled')).toHaveLength(1);
    expect(countries.filterCountries(data.customCountries, '', 'disabled')).toHaveLength(0);
    expect(countries.countryBody({ id: 1 }, { name: 'X', countryCode: '999', iso2: 'TL', enabled: false })).toEqual({ id: 1, name: 'X', countryCode: '999', iso2: 'TL', enabled: false });
    expect(fragment.fragmentListingBody({ kind: 'username', value: '@x', amount: '1', countryCode: '888', displayFormat: 'y' })).toEqual({ kind: 'username', value: '@x', amount: '1' });
    expect(fragment.fragmentListingBody({ kind: 'number', value: '+888', amount: '1', countryCode: '888', displayFormat: '+888 1' })).toMatchObject({ countryCode: '888', displayFormat: '+888 1' });
    expect(fragment.fragmentOwner({ ownerId: 2 }, users)).toBe('=Alice');
    expect(fragment.fragmentOwner({ ownerId: null }, users)).toBe('Administrator inventory');
});

test('settings send numbers as numbers and validate bounds before saving', () => {
    let payload = settings.settingsData(data.settings, { ...data.settings, messageMaxLength: '2048', registrationEnabled: false });
    expect(payload.messageMaxLength).toBe(2048);
    expect(payload.registrationEnabled).toBe(false);
    expect(payload.siteName).toBe('<Own & Instance>');
    expect(Object.keys(settings.settingsErrors({ ...data.settings, siteName: ' ', messageMaxLength: '0' }))).toEqual(expect.arrayContaining(['siteName', 'messageMaxLength']));
    expect(settings.settingsDirty(data.settings, null)).toBe(false);
    let html = render(settings.default);
    expect(html).toContain('Set Instance Photo');
    expect(html).toContain('Use Default Logo');
    expect(html).toContain('/branding/logo?v=revision');
});

test('broadcast shows reach, keeps sending disabled until there is text and escapes history', () => {
    let html = render(broadcast.default);
    expect(html).toContain('Reaches <strong');
    expect(html).toContain('3 people');
    expect(html).toContain('&lt;b&gt;hi&lt;/b&gt;');
    expect(html).toMatch(/<button[^>]*disabled[^>]*>.*Review and send/s);
});

test('formatting helpers', () => {
    expect(format.deviceInfo('Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) Version/18.0 Safari/605.1.15').name).toBe('Safari on macOS');
    expect(format.deviceInfo('curl/8.2').name).toBe('Script');
    expect(format.bytes(1048576)).toBe('1.0 MB');
    expect(format.duration(90061)).toBe('1d 1h');
    expect(format.percentChange(3, 2)).toBe(50);
    expect(format.percentChange(3, 0)).toBe(null);
    expect(format.paged([1, 2, 3, 4, 5], 9, 2)).toMatchObject({ items: [5], page: 3, from: 5, to: 5 });
    expect(format.csvCell('+cmd')).toBe(`"'+cmd"`);
});

test('reports group by target, search what was reported and act only on what is on screen', () => {
    let groups = live['/admin/reports?status=open'].groups;
    expect(reportsView.reportsPath('open', 'all', 'all')).toBe('/admin/reports?status=open');
    expect(reportsView.reportsPath('closed', 'spam', 'group')).toBe('/admin/reports?status=closed&reason=spam&target=group');
    expect(reportsView.filterGroups(groups, 'pills').map(group => group.key)).toEqual(['u3']);
    expect(reportsView.filterGroups(groups, 'pirated').map(group => group.key)).toEqual(['c20']);
    expect(reportsView.resolveBody(groups[0], { delete: true, freeze: true }, ' wave ')).toEqual({ reports: [7, 6], delete: true, freeze: true, ban: false, note: 'wave' });
    expect(reportsView.mediaText({ type: 'video', size: 2048 })).toBe('Video · 2.0 KB');
    expect(reportsView.messengerLink('-1000000000020')).toBe('/#-1000000000020');
    let html = render(reportsView.default);
    expect(html).toContain('Bob &lt;b&gt;');
    expect(html).not.toContain('Bob <b>');
    expect(html).toContain('2 open');
    expect(html).toContain('2 people');
    expect(html).toContain('&lt;script&gt;card&lt;/script&gt;');
    expect(html).toContain('Spam or scam › Phishing');
    expect(format.auditLabel('reports_resolved')).toBe('acted on reports');
});
