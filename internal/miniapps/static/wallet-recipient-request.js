// Telegram's own "Select Chat" picker lives in Web A, which announces it when
// asked. A bare tab, the native apps and an older cached Web A have no such
// thing, so they get the sheet below.
const css = `
.greengram-recipient{position:fixed;inset:0;z-index:2147483000;display:flex;flex-direction:column;justify-content:flex-end;background:rgba(0,0,0,.5);color:var(--tg-theme-text-color,#000);font-family:-apple-system,BlinkMacSystemFont,Roboto,'Segoe UI',Helvetica,Arial,sans-serif;font-size:16px;animation:greengram-fade .2s ease}
.greengram-recipient-sheet{display:flex;flex-direction:column;height:88%;width:100%;max-width:520px;margin:0 auto;box-sizing:border-box;border-radius:16px 16px 0 0;background:var(--tg-theme-secondary-bg-color,#f1f1f4);padding-bottom:var(--tg-safe-area-inset-bottom,0px);overflow:hidden;animation:greengram-rise .25s cubic-bezier(.2,.8,.2,1)}
.greengram-recipient-head{position:relative;display:flex;align-items:center;justify-content:center;min-height:56px;font-weight:600;font-size:17px}
.greengram-recipient-head button{position:absolute;right:12px;top:14px;width:28px;height:28px;border:0;border-radius:50%;background:rgba(120,120,128,.24);color:var(--tg-theme-hint-color,#8a8a8f);font-family:inherit;font-size:15px;line-height:1;cursor:pointer}
.greengram-recipient input{margin:0 16px 12px;padding:9px 14px;border:0;border-radius:10px;font-family:inherit;font-size:16px;color:inherit;background:var(--tg-theme-section-bg-color,var(--tg-theme-bg-color,#fff));outline:none}
.greengram-recipient input::-webkit-search-cancel-button{display:none}
.greengram-recipient input::placeholder{color:var(--tg-theme-hint-color,#8a8a8f)}
.greengram-recipient-list{overflow-y:auto;flex:1;margin:0 16px 16px;border-radius:12px}
.greengram-recipient-rows{border-radius:12px;background:var(--tg-theme-section-bg-color,var(--tg-theme-bg-color,#fff));overflow:hidden}
.greengram-recipient-rows button{position:relative;display:flex;align-items:center;gap:12px;width:100%;padding:8px 16px;border:0;background:none;color:inherit;font-family:inherit;text-align:left;cursor:pointer}
.greengram-recipient-rows button:active{background:var(--tg-theme-secondary-bg-color,rgba(120,120,128,.12))}
.greengram-recipient-rows button+button::before{content:"";position:absolute;top:0;left:68px;right:0;height:1px;background:var(--tg-theme-section-separator-color,rgba(120,120,128,.2))}
.greengram-recipient-pic{flex:none;width:40px;height:40px;border-radius:50%;display:flex;align-items:center;justify-content:center;font-weight:600;font-size:16px;color:#fff;background-size:cover;background-position:center;overflow:hidden}
.greengram-recipient-pic img{width:100%;height:100%;object-fit:cover;display:block}
.greengram-recipient-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:16px}
.greengram-recipient-sub{color:var(--tg-theme-hint-color,#8a8a8f);font-size:14px}
.greengram-recipient-empty{padding:24px 16px;text-align:center;color:var(--tg-theme-hint-color,#8a8a8f)}
@keyframes greengram-fade{from{opacity:0}}
@keyframes greengram-rise{from{transform:translateY(100%)}}`;

// Telegram's seven name colours, picked the way its apps pick them
const peerColors = ['linear-gradient(#ff885e,#ff516a)', 'linear-gradient(#ffcd6a,#ffa85c)', 'linear-gradient(#82b1ff,#665fff)', 'linear-gradient(#a0de7e,#54cb68)', 'linear-gradient(#53edd6,#28c9b7)', 'linear-gradient(#72d5fd,#2a9ef1)', 'linear-gradient(#e0a2f3,#d669ed)'];

let webPickerAvailable = false;

function postToWebApp(window, eventType, eventData) {
    window.parent.postMessage(JSON.stringify({ eventType, eventData }), window.location.origin);
}

function readWebAppEvent(window, event, eventType) {
    let message;
    if (event.source !== window.parent) return;
    try { message = JSON.parse(event.data); } catch { return; }
    return message?.eventType === eventType ? message : undefined;
}

export function probeWalletRecipientPicker(window) {
    if (window.parent === window) return;
    let listen = event => {
        if (!readWebAppEvent(window, event, 'greengram_recipient_picker_available')) return;
        webPickerAvailable = true;
        window.removeEventListener('message', listen);
    };
    window.addEventListener('message', listen);
    postToWebApp(window, 'greengram_recipient_picker_probe');
}

function requestFromWebApp(window) {
    return new Promise(resolve => {
        let id = String(Math.random()).slice(2);
        let done = userId => {
            window.removeEventListener('message', listen);
            window.removeEventListener('pagehide', leave);
            resolve(userId);
        };
        let leave = () => done();
        let listen = event => {
            let message = readWebAppEvent(window, event, 'greengram_recipient_picker_closed');
            if (message?.eventData?.req_id === id) done(message.eventData.user_id);
        };
        window.addEventListener('message', listen);
        window.addEventListener('pagehide', leave);
        postToWebApp(window, 'greengram_recipient_picker_open', { req_id: id });
    });
}

export function requestWalletRecipient(window) {
    if (webPickerAvailable) return requestFromWebApp(window);
    let document = window.document;
    return new Promise(resolve => {
        let root = document.createElement('div');
        let style = document.createElement('style');
        let sheet = document.createElement('div');
        let head = document.createElement('div');
        let title = document.createElement('span');
        let cancel = document.createElement('button');
        let input = document.createElement('input');
        let list = document.createElement('div');
        let latest = 0;
        let timer;
        let done = id => {
            clearTimeout(timer);
            window.removeEventListener('pagehide', leave);
            root.remove();
            resolve(id);
        };
        let leave = () => done();
        let note = text => {
            let row = document.createElement('div');
            row.className = 'greengram-recipient-empty';
            row.textContent = text;
            list.replaceChildren(row);
        };
        let row = user => {
            let button = document.createElement('button');
            let pic = document.createElement('span');
            let text = document.createElement('span');
            let name = document.createElement('div');
            let sub = document.createElement('div');
            pic.className = 'greengram-recipient-pic';
            pic.style.background = peerColors[Math.abs(Number(user.id) || 0) % peerColors.length];
            pic.textContent = Array.from(user.tg_full_name || user.mention || '?')[0].toUpperCase();
            if (user.tg_photo_url) {
                let photo = document.createElement('img');
                photo.alt = '';
                photo.addEventListener('load', () => pic.replaceChildren(photo));
                photo.src = user.tg_photo_url;
            }
            text.style.minWidth = '0';
            name.className = 'greengram-recipient-name';
            name.textContent = user.tg_full_name || '@' + user.mention;
            sub.className = 'greengram-recipient-sub';
            sub.textContent = user.mention ? '@' + user.mention : '';
            text.append(name, sub);
            button.type = 'button';
            button.append(pic, text);
            button.addEventListener('click', () => done(String(user.id)));
            return button;
        };
        let load = async () => {
            let turn = ++latest;
            try {
                let response = await window.fetch('/api/walt/telegram/recipients?q=' + encodeURIComponent(input.value));
                let result = await response.json();
                if (turn !== latest) return;
                if (!response.ok) return note(result.error || 'People could not be loaded.');
                if (!result.users.length) return note(input.value.trim() ? 'Nobody found.' : 'Type a username to find someone.');
                let rows = document.createElement('div');
                rows.className = 'greengram-recipient-rows';
                rows.append(...result.users.map(row));
                list.replaceChildren(rows);
            } catch {
                if (turn === latest) note('People could not be loaded.');
            }
        };
        root.className = 'greengram-recipient';
        style.textContent = css;
        sheet.className = 'greengram-recipient-sheet';
        head.className = 'greengram-recipient-head';
        title.textContent = 'Transfer';
        cancel.type = 'button';
        cancel.setAttribute('aria-label', 'Close');
        cancel.textContent = '\u2715';
        input.type = 'search';
        input.placeholder = 'Search';
        input.autocomplete = 'off';
        input.spellcheck = false;
        list.className = 'greengram-recipient-list';
        head.append(title, cancel);
        sheet.append(head, input, list);
        root.append(style, sheet);
        cancel.addEventListener('click', leave);
        root.addEventListener('click', event => { if (event.target === root) leave(); });
        input.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(load, 200); });
        window.addEventListener('pagehide', leave);
        document.body.append(root);
        load();
    });
}
