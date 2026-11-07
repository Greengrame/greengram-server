const botfatherConfiguration = window.GreengramMiniapp;
let botfatherPage = botfatherConfiguration.page;
const botfatherStatus = document.createElement('div');
const botfatherToastKey = 'greengram-botfather-toast';
const botfatherUnload = [];
let botfatherBeforeUnload;

function botfatherError(error) {
    botfatherStatus.textContent = error.message || String(error);
}

async function botfatherRequest(method, data) {
    let response = await fetch('/__miniapps/botfather/api', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method, ...data }) });
    let result = await response.json();
    if (!response.ok) throw new Error(result.error || 'Request failed.');
    return result;
}

// The original navigates inside the page and runs its unload callbacks, which is
// where it shows "saved" toasts. Here every page is a fresh load with this
// launch's markup, so run them on the way out and carry the toast across.
function botfatherLeave(href, replace) {
    setTimeout(() => {
        let toast = TWebApp.showSuccessToast;
        TWebApp.showSuccessToast = text => sessionStorage.setItem(botfatherToastKey, text);
        botfatherUnload.forEach(callback => callback());
        TWebApp.showSuccessToast = toast;
        return replace ? location.replace(href) : location.assign(href);
    });
}

function botfatherGo(href, replace) {
    let message = botfatherBeforeUnload?.();
    if (message) return window.showConfirm(message, () => { botfatherBeforeUnload = null; botfatherLeave(href, replace); });
    botfatherLeave(href, replace);
}

function initBotfather() {
    ajInit({ version: 575, apiUrl: '/__miniapps/botfather/api', state: botfatherPage });
    Aj.apiRequest = function (method, params, callback) {
        botfatherRequest(method, params).then(result => {
            if (result.token) document.querySelector('.tm-api-token-actions .tm-active-button')?.removeAttribute('data-gg-off');
            callback?.(result);
            if (result.error && !callback) botfatherError(new Error(result.error));
        }, error => {
            if (callback) callback({ ok: false, error: error.message });
            else botfatherError(error);
        });
    };
    Aj.location = function (href, replace) {
        let url;
        if (href === undefined) return new URL(location.href);
        url = new URL(href, location.href);
        if (url.origin !== location.origin) return botfatherError(new Error('This menu is not available on this instance.'));
        if (Number(botfatherConfiguration.slot) > 1) url.searchParams.set('account', String(botfatherConfiguration.slot));
        return botfatherGo(url.href, replace);
    };
    let onUnload = Aj.onUnload;
    Aj.onUnload = function (callback) {
        botfatherUnload.push(callback);
        onUnload(callback);
    };
    Aj.onBeforeUnload = function (callback) { botfatherBeforeUnload = callback; };
    Aj.uploadRequest = async function (method, file, params, callback) {
        let result;
        try {
            let response = await fetch('/__miniapps/botfather/upload?target=' + encodeURIComponent(params?.target || (method === 'uploadIcon' ? 'launch_icon' : '')), { method: 'POST', headers: { 'Content-Type': file.type || 'image/jpeg' }, body: file });
            result = await response.json();
            if (!response.ok && !result.error) result = { ok: false, error: 'Upload failed.' };
        } catch (error) {
            result = { ok: false, error: error.message };
        }
        if (callback) callback(result);
        else if (result.error) botfatherError(new Error(result.error));
    };
    // the original's strings; each page brought its own with it
    l.add({
        WEB_BOTFATHER_NO_RESULTS_INFO: 'There were no results for “{query}”',
        WEB_USERNAME_CHECKING: 'Checking username', WEB_USERNAME_AVAILABLE: '{username} is available.',
        WEB_API_TOKEN_REVOKE_TITLE: 'Revoke API Token', WEB_API_TOKEN_REVOKE_TEXT: 'Do you want to revoke the API token?', WEB_API_TOKEN_REVOKE_BTN: 'Revoke', WEB_TOKEN_REVOKE_SUCCESS: 'Bot token has been revoked.', WEB_TOKEN_COPY_SUCCESS: 'Bot token copied to clipboard.',
        WEB_PROFILE_UPDATE: 'Update', WEB_UNSAVED_CHANGES: 'Changes that you made may not be saved.',
        WEB_COMMANDS_EDIT_BTN: 'Edit', WEB_COMMANDS_DONE_BTN: 'Done', WEB_COMMANDS_ADD: 'Add', WEB_COMMANDS_SAVE: 'Save',
        WEB_USERNAMES_LAST_ACTIVE: 'Sorry, you can&#39;t hide the bot&#39;s only active username.',
        WEB_MENUBTN_DISABLE_TITLE: 'Are you sure?', WEB_MENUBTN_DISABLE_CONFIRM: 'Are you sure you want to disable the Menu Button?', WEB_MAINAPP_DISABLE_CONFIRM: 'Are you sure you want to disable the Main App?', WEB_MENUBTN_POPUP_DISABLE: 'Disable',
        WEB_GENERIC_COPY_SUCCESS: 'Copied to clipboard.', WEB_LINK_COPIED: 'Link copied.',
        WEB_GAMES_DELETE_CONFIRM_TITLE: 'Delete Direct Link', WEB_GAMES_DELETE_APP_CONFIRM_BODY: 'The link stops working for everyone who has it.', WEB_GAMES_DELETE: 'Delete'
    });
    TWebApp.init('/botfather');
    initCapturedBotfather();
    Aj.pageLoaded();
    let toast = sessionStorage.getItem(botfatherToastKey);
    if (toast) {
        sessionStorage.removeItem(botfatherToastKey);
        TWebApp.showSuccessToast(toast);
    }
}

function initCapturedBotfather() {
    let allowed = new Set(['BotList', 'CreateBot', 'BotGeneral', 'BotProfile', 'BotCommandsList', 'BotCommandEdit', 'BotApps', 'BotSettings', 'BotSettingsInline', 'BotAppEdit', 'BotLaunchScreen', 'BotUsernames', 'BotMainApp', 'BotMenuApp']);
    let openLink = WebApp.openTelegramLink.bind(WebApp);
    WebApp.openTelegramLink = function (value) {
        let url;
        if (!value) return;
        url = new URL(value, location.origin);
        // Web A opens t.me links itself; a bare tab goes to this site's /u/ page
        if (window.parent !== window) return openLink(url.href);
        if (['t.me', 'telegram.me', 'telegram.dog'].includes(url.hostname)) url = new URL('/u' + url.pathname + url.search, location.origin);
        if (url.origin !== location.origin) return botfatherError(new Error('This link is not available on this instance.'));
        if (Number(botfatherConfiguration.slot) > 1) url.searchParams.set('account', String(botfatherConfiguration.slot));
        return window.open(url.href, '_blank', 'noopener');
    };
    // the original hands deleting to a chat with BotFather; here the page asks
    BotGeneral.eDeleteClick = function () {
        WebApp.showPopup({
            title: 'Delete Bot',
            message: 'Delete @' + botfatherPage.username + '? Its token stops working and the username becomes free. This can’t be undone.',
            buttons: [{ id: 'delete', text: 'Delete', type: 'destructive' }, { type: 'cancel' }]
        }, button => {
            if (button !== 'delete') return;
            Aj.apiRequest('requestDeleteBot', { bid: botfatherPage.botId }, result => {
                if (!result.ok) return TWebApp.showErrorToast(result.error);
                Aj.onUnload(() => TWebApp.showSuccessToast(result.msg));
                Aj.location('/botfather');
            });
        });
    };
    (botfatherPage.initializers || []).forEach(function (name) {
        let object = name.replace(/\.init$/, '');
        if (allowed.has(object) && typeof window[object]?.init === 'function') window[object].init();
    });
    if (botfatherPage.welcomePic) BotProfile.setWelcomePic(botfatherPage.welcomePic);
    initBotfatherAccess();
    document.querySelectorAll('form').forEach(form => form.addEventListener('submit', event => event.preventDefault()));
}

// Restrict bot usage's list. The original picks people with WebApp.requestChat,
// which Web A doesn't have, so this takes a username.
function initBotfatherAccess() {
    let box = document.querySelector('.gg-access');
    if (!box) return;
    let input = box.querySelector('input');
    let list = box.querySelector('.gg-access-list');
    let row = user => {
        let item = document.createElement('div');
        item.className = 'tm-row gg-app-row';
        item.dataset.id = user.id;
        let name = document.createElement('span');
        name.className = 'gg-app-main';
        name.textContent = user.name;
        let remove = document.createElement('span');
        remove.className = 'gg-app-action icon-before icon-delete js-gg-access-remove';
        remove.title = 'Remove';
        item.append(name, remove);
        return item;
    };
    let change = params => Aj.apiRequest('setAccessUsers', { bid: Aj.state.botId, ...params }, result => {
        if (!result.ok) return TWebApp.showErrorToast(result.error);
        list.replaceChildren(...result.users.map(row));
        input.value = '';
    });
    document.querySelector('[data-field=access]')?.addEventListener('click', event => {
        box.classList.toggle('hidden', !event.currentTarget.querySelector('.tm-toggle').classList.contains('tm-toggle-on'));
    });
    input.addEventListener('change', () => { if (input.value.trim()) change({ add: input.value }); });
    list.addEventListener('click', event => {
        let item = event.target.closest('.js-gg-access-remove')?.closest('.gg-app-row');
        if (item) change({ remove: item.dataset.id });
    });
}

// Controls with nothing behind them on this server say so instead of acting.
document.addEventListener('click', function (event) {
    let off = event.target instanceof Element ? event.target.closest('[data-gg-off]') : undefined;
    if (!off) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    document.querySelectorAll('.btn-group.open').forEach(group => group.classList.remove('open'));
    TWebApp.showErrorToast(off.dataset.ggOff || 'This isn’t available on this server.');
}, true);

document.addEventListener('click', function (event) {
    let anchor = event.target instanceof Element ? event.target.closest('a[href]') : undefined;
    let url = anchor ? new URL(anchor.href) : undefined;
    if (!url || url.origin !== location.origin || !url.pathname.startsWith('/botfather')) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    botfatherGo(url.href);
}, true);


// Greengram: the page depends on who is looking, so it comes from the
// server for this launch before the captured scripts start.
window.GreengramMiniappPage().then(function (page) {
    let content = document.querySelector('#aj_content');
    botfatherPage = botfatherConfiguration.page = page.state;
    content.innerHTML = page.markup;
    document.title = page.title || document.title;
    botfatherStatus.className = 'greengram-bot-error';
    botfatherStatus.setAttribute('role', 'status');
    content.prepend(botfatherStatus);
    initBotfather();
}, window.GreengramMiniappFailed);
