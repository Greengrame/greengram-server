const miniappConfiguration = JSON.parse(document.querySelector('#greengram-miniapp-config').textContent);
const miniappTheme = { bg_color: '#212d3b', text_color: '#ffffff', hint_color: '#7d8b99', link_color: '#5eabe1', button_color: '#229af0', button_text_color: '#ffffff', secondary_bg_color: '#151e27', header_bg_color: '#242d39', section_bg_color: '#1d2733', bottom_bar_bg_color: '#151e27' };
// Greengram: the client signs the launch data and passes it in the hash;
// telegram-web-app.js keeps it in sessionStorage across page loads, so
// read it from both and only fill in what a bare browser tab lacks. Every
// app open in this tab shares that storage (Web A keeps them as tabs), so
// each keeps its own copy and puts it back for telegram-web-app.js, or the
// next page of one app would load with another's launch and get a 401.
const miniappLaunchKey = 'greengram-launch-' + miniappConfiguration.app;
const miniappParams = miniappLaunchParams();
const miniappInitData = miniappParams.tgWebAppData || '';
const miniappFetch = window.fetch.bind(window);
const miniappXhrSend = XMLHttpRequest.prototype.send;
const miniappXhrOpen = XMLHttpRequest.prototype.open;
let miniappMainButton;
let miniappButtonState;
let miniappDocumentActive = true;

function miniappLaunchParams() {
    let stored = {};
    let hash = {};
    try { stored = JSON.parse(sessionStorage.getItem(miniappLaunchKey) || '{}') || {}; } catch { stored = {}; }
    new URLSearchParams(location.hash.slice(1)).forEach((value, key) => { if (key.startsWith('tgWebApp')) hash[key] = value; });
    return { tgWebAppVersion: '9.0', tgWebAppPlatform: 'weba', tgWebAppThemeParams: JSON.stringify(miniappTheme), ...stored, ...hash };
}

function miniappUser() {
    let user;
    try { user = JSON.parse(new URLSearchParams(miniappInitData).get('user') || 'null'); } catch { user = null; }
    return user ? { id: user.id, name: [user.first_name, user.last_name].filter(Boolean).join(' '), username: user.username || '', isPremium: Boolean(user.is_premium) } : null;
}

// jQuery 1.11 probes focusin support by setting an onfocusin attribute,
// which the CSP reports in Firefox; focusin bubbles everywhere now.
if (!('onfocusin' in window)) window.onfocusin = null;

miniappConfiguration.user = miniappUser();
miniappConfiguration.initData = miniappInitData;
window.GreengramMiniapp = miniappConfiguration;
window.__WALT__ = { appVersion: miniappConfiguration.version || '', gitCommit: miniappConfiguration.commit || '' };
sessionStorage.setItem(miniappLaunchKey, JSON.stringify(miniappParams));
sessionStorage.setItem('__telegram__initParams', JSON.stringify(miniappParams));
window.TelegramWebviewProxy = {
    postEvent(name, data) {
        let payload;
        let choice;
        if (!miniappDocumentActive) return;
        try { payload = typeof data === 'string' ? JSON.parse(data) : data; } catch { payload = {}; }
        if (miniappConfiguration.app === 'walt' && window.GreengramWalletTransferConfirming && name === 'web_app_setup_main_button') payload = { ...payload, is_visible: false };
        if (window.parent !== window) { window.parent.postMessage(JSON.stringify({ eventType: name, eventData: payload }), location.origin); return; }
        if (name === 'web_app_setup_main_button') {
            miniappButtonState = payload;
            renderMiniappButton();
        }
        if (name === 'web_app_open_popup') {
            choice = (payload.buttons || []).find(button => !['cancel', 'close'].includes(button.type));
            queueMicrotask(() => window.Telegram.WebView.receiveEvent('popup_closed', { button_id: choice && window.confirm([payload.title, payload.message].filter(Boolean).join('\n\n')) ? choice.id : '' }));
        }
        if (name === 'web_app_request_theme') queueMicrotask(() => window.Telegram.WebView.receiveEvent('theme_changed', { theme_params: miniappTheme }));
        if (name === 'web_app_request_write_access') queueMicrotask(() => window.Telegram.WebView.receiveEvent('write_access_requested', { status: 'allowed' }));
        if (name === 'web_app_request_viewport') queueMicrotask(() => window.Telegram.WebView.receiveEvent('viewport_changed', { height: window.innerHeight, is_state_stable: true, is_expanded: true }));
    }
};

function miniappBackTarget(app, value) {
    let url = new URL(value, location.href);
    let path = url.pathname.replace(/\/+$/, '') || '/';
    let segments = path.split('/').filter(Boolean);
    if (url.origin !== location.origin) return '';
    if (app === 'botfather') {
        if (path === '/botfather') return '';
        if (segments[1] === 'bot' && /^\d+$/.test(segments[2] || '') && segments.length > 3) return '/' + segments.slice(0, -1).join('/');
        return '/botfather';
    }
    if (app === 'stickers') {
        if (path === '/stickers') return '';
        if (['editpack', 'stats', 'publishpack'].includes(segments[1]) && /^\d+$/.test(segments[2] || '')) return '/stickers/pack/' + segments[2];
        return '/stickers';
    }
    return undefined;
}

window.GreengramMiniappBackTarget = miniappBackTarget;
window.addEventListener?.('pagehide', () => { miniappDocumentActive = false; });
window.addEventListener?.('pageshow', () => {
    let app = window.Telegram?.WebApp;
    miniappDocumentActive = true;
    if (!app) return;
    window.TelegramWebviewProxy.postEvent('web_app_setup_back_button', { is_visible: Boolean(app.BackButton?.isVisible) });
    window.TelegramWebviewProxy.postEvent('web_app_setup_main_button', { is_visible: Boolean(app.MainButton?.isVisible), is_active: app.MainButton?.isActive !== false, text: app.MainButton?.text || '' });
});
window.addEventListener?.('message', event => {
    let message;
    try { message = typeof event.data === 'string' ? JSON.parse(event.data) : event.data; } catch { return; }
    if (!message?.eventType) return;
    if (!miniappDocumentActive || event.source !== window.parent || event.origin !== location.origin) event.stopImmediatePropagation();
}, true);

window.TelegramWebviewProxy.postEvent('web_app_setup_back_button', { is_visible: false });
window.TelegramWebviewProxy.postEvent('web_app_setup_main_button', { is_visible: false, is_active: false, text: '' });

function renderMiniappButton() {
    if (!document.body || !miniappButtonState) return;
    if (!miniappMainButton) {
        miniappMainButton = document.createElement('button');
        miniappMainButton.className = 'greengram-miniapp-main-button';
        miniappMainButton.onclick = () => window.Telegram.WebView.receiveEvent('main_button_pressed', {});
        document.body.append(miniappMainButton);
    }
    miniappMainButton.textContent = miniappButtonState.text || 'Continue';
    miniappMainButton.hidden = !miniappButtonState.is_visible;
    miniappMainButton.disabled = miniappButtonState.is_active === false;
}

document.addEventListener('DOMContentLoaded', renderMiniappButton);

function miniappApiUrl(input) {
    let url = new URL(typeof input === 'string' ? input : input.url, location.href);
    if (miniappConfiguration.app !== 'walt') return url.href;
    if (url.pathname.startsWith('/api/walt/')) return location.origin + url.pathname + url.search;
    if (/^\/(?:api\/v\d+|alectryon|user-profile|users|public-api|p2p|v2api|portmone|scwapi|scw-bff|earn|events|notifications)/.test(url.pathname) || url.origin !== location.origin && !/^\/(?:static|public)\//.test(url.pathname)) return location.origin + '/api/walt' + url.pathname + url.search;
    return url.origin === location.origin ? url.href : location.origin + url.pathname + url.search;
}

window.fetch = function (input, options = {}) {
    let headers = new Headers(options.headers || (typeof input !== 'string' ? input.headers : undefined));
    let method = options.method || (typeof input !== 'string' ? input.method : 'GET');
    let body = options.body;
    if (miniappConfiguration.app === 'walt' && ['POST', 'PATCH', 'DELETE'].includes(method) && body == null && (typeof input === 'string' || input.body === null)) { headers.set('Content-Type', 'application/json'); body = '{}'; }
    if (miniappInitData && new URL(miniappApiUrl(input), location.href).origin === location.origin) headers.set('X-Telegram-Init-Data', miniappInitData);
    return miniappFetch(typeof input === 'string' ? miniappApiUrl(input) : new Request(miniappApiUrl(input), input), { ...options, headers, ...(body === undefined ? {} : { body }) });
};
XMLHttpRequest.prototype.open = function (method, url, ...args) {
    let target = miniappApiUrl(String(url));
    this.greengramMethod = method.toUpperCase();
    this.greengramSameOrigin = new URL(target, location.href).origin === location.origin;
    return miniappXhrOpen.call(this, method, target, ...args);
};

XMLHttpRequest.prototype.send = function (body) {
    if (miniappConfiguration.app === 'walt' && ['POST', 'PATCH', 'DELETE'].includes(this.greengramMethod) && body == null) { this.setRequestHeader('Content-Type', 'application/json'); body = '{}'; }
    if (miniappInitData && this.greengramSameOrigin) this.setRequestHeader('X-Telegram-Init-Data', miniappInitData);
    return miniappXhrSend.call(this, body);
};

// A page that couldn't be had. The captures hide .tm-auth-expired until
// their own script shows it, which never runs here, so the page must say
// so itself; a dark screen and no way back is all you'd get otherwise.
window.GreengramMiniappFailed = function (error) {
    let note = document.createElement('div');
    let app = window.Telegram?.WebApp;
    let back = miniappBackTarget(miniappConfiguration.app, location.href);
    note.className = 'tm-auth-expired';
    note.style.cssText = 'display:block;width:auto;padding:64px 24px;text-align:center;color:#fff;font-size:15px';
    note.textContent = error instanceof TypeError ? 'Can’t reach the server. Check your connection and open the app again.' : error.message;
    document.querySelector('#aj_content').replaceChildren(note);
    if (!app) return;
    if (back) {
        app.BackButton.onClick(() => location.assign(back));
        app.BackButton.show();
    }
    app.ready();
};

// Pages that depend on who is looking (sticker packs, bots, Fragment
// assets) are rendered by the server for the signed-in launch: same path,
// asked for as JSON.
window.GreengramMiniappPage = function () {
    let url = new URL(location.href);
    url.hash = '';
    return fetch(url.href, { headers: { 'X-Greengram-Page': '1' }, credentials: 'same-origin' }).then(async response => {
        let result = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(result.error || 'Restart the app to continue.');
        return result;
    });
};
