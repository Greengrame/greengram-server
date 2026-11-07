const configuration = JSON.parse(document.querySelector('#fragment-config').textContent);
const table = document.querySelector('#fragment-items');
const status = document.querySelector('#fragment-status');
const dialog = document.querySelector('#fragment-confirm');
const pay = document.querySelector('#fragment-pay');
const searchForm = document.querySelector('form.tm-main-search-form');
const searchInput = document.querySelector('#fragment-search');
const createListingButton = document.querySelector('#fragment-create-listing');
const assignNumberButton = document.querySelector('#fragment-assign-number');
const assignmentForm = document.querySelector('#fragment-assignment-form');
const detailAction = document.querySelector('#fragment-detail-action');
let catalog = configuration.catalog;
let category = ['number', 'owned'].includes(location.hash?.slice(1)) ? location.hash.slice(1) : 'username';
let quote;
let busy = false;
let popupOverflow;

async function api(path, data, method) {
    let response = await fetch('/api/fragment/' + path + '?account=' + configuration.slot, { method: method || (data ? 'POST' : 'GET'), headers: data ? { 'Content-Type': 'application/json' } : {}, body: data ? JSON.stringify(data) : undefined });
    let result = await response.json();
    if (!response.ok) throw new Error(result.error || 'Please try again.');
    return result;
}

function readableNumber(title) {
    let digits = title.replace(/\D/g, '');
    return digits.length === 11 && digits.startsWith('888') ? '+' + digits.slice(0, 3) + ' ' + digits.slice(3, 7) + ' ' + digits.slice(7) : title;
}

function readableAmount(value) {
    let parts = String(value).split('.');
    return parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (parts[1] ? '.' + parts[1] : '');
}

function itemRoute(item) {
    return '/fragment/' + item.kind + '/' + item.title.replace(/^[@+]/, '').replace(/\s/g, '') + '?miniapp=1&account=' + configuration.slot;
}

function render() {
    let search = searchInput?.value.toLowerCase() || '';
    let items = (category === 'owned' ? catalog.ownedAssets || catalog.items.filter(item => item.isOwned) : catalog.items).filter(item => (category === 'owned' ? item.isOwned : item.kind === category && item.listed) && item.title.toLowerCase().includes(search));
    let template = document.querySelector('#fragment-row-template');
    if (!table) return;
    table.replaceChildren();
    document.querySelector('#fragment-balance').textContent = catalog.balance + ' GRAM';
    items.forEach(item => {
        let row = template.content.firstElementChild.cloneNode(true);
        let cells = row.querySelectorAll('td');
        let name = cells[0].querySelector('.table-cell-value');
        let price = cells[1].querySelector('.table-cell-value');
        let resale = cells[0].querySelector('.table-cell-status-thin');
        let fiat = cells[1].querySelector('.table-cell-desc.wide-only');
        let thinStatus = cells[1].querySelector('.table-cell-desc.thin-only');
        name.textContent = item.kind === 'number' ? item.displayTitle || readableNumber(item.title) : item.title;
        price.textContent = readableAmount(item.amount);
        if (resale) { if (item.ownerId) resale.textContent = 'Resale'; else resale.remove(); }
        if (fiat) fiat.textContent = item.usdAmount ? '~ $' + readableAmount(item.usdAmount) : '';
        if (thinStatus) thinStatus.textContent = item.listed ? 'For sale' : 'Owned';
        cells[2].querySelector('.table-cell-value').textContent = item.isPrimary ? 'Primary' : item.listed ? 'For sale' : 'Owned';
        cells[2].querySelector('.table-cell-desc').textContent = item.channelTitle || (item.isOwned ? 'Your collectible' : 'Fixed price');
        row.querySelectorAll('a.table-cell').forEach(link => { link.href = itemRoute(item); });
        table.append(row);
    });
    document.querySelector('#fragment-heading').textContent = category === 'owned' ? 'My Assets' : category === 'number' ? 'Collectible Numbers' : 'Usernames';
    table.closest('table').querySelector('th').textContent = category === 'number' ? 'Collectible Number' : category === 'owned' ? 'Collectible' : 'Username';
    status.textContent = items.length ? '' : 'No listings found.';
}

async function choose(itemId) {
    if (busy) return;
    busy = true;
    render();
    try {
        quote = await api('quote', { itemId });
        document.querySelector('#fragment-confirm-text').textContent = quote.title + ' for ' + quote.amount + ' GRAM from Wallet. This collectible belongs to this instance.';
        document.querySelector('#fragment-confirm-error').textContent = '';
        pay.disabled = false;
        openPopup('fragment-confirm');
    } catch (error) { status.textContent = error.message; }
    finally { busy = false; renderAfterChoice(); }
}

function renderAfterChoice() {
    table?.querySelectorAll('button').forEach(button => { if (button.textContent === 'Buy') button.disabled = false; });
}

pay.onclick = async () => {
    let result;
    if (busy || !quote) return;
    busy = true;
    pay.disabled = true;
    try {
        result = await api('purchase', { quoteId: quote.id });
        catalog = await api('catalog');
        closePopup('fragment-confirm');
        render();
        status.textContent = result.title + ' purchased. View it in My assets.';
        if (configuration.selectedItem) location.reload();
    } catch (error) { document.querySelector('#fragment-confirm-error').textContent = error.message; }
    finally { busy = false; pay.disabled = false; }
};
if (searchForm) searchForm.onsubmit = event => event.preventDefault();
if (searchInput) searchInput.oninput = render;
document.querySelectorAll('[data-category]').forEach(button => {
    button.onclick = event => {
        event.preventDefault();
        category = button.dataset.category;
        document.querySelectorAll('[data-category]').forEach(tab => tab.classList.toggle('tab-active', tab === button));
        document.querySelector('#fragment-heading').textContent = button.textContent;
        render();
    };
});
window.addEventListener('message', event => {
    let message;
    if (event.origin !== location.origin || event.source !== window.parent) return;
    try { message = typeof event.data === 'string' ? JSON.parse(event.data) : event.data; } catch { return; }
    if (message?.eventType !== 'back_button_pressed') return;
    if (document.querySelector('.popup-container:not(.hide)')) document.querySelectorAll('.popup-container:not(.hide)').forEach(popup => closePopup(popup.id));
    else if (configuration.selectedItem) location.href = '/fragment?miniapp=1&account=' + configuration.slot;
});
window.parent.postMessage(JSON.stringify({ eventType: 'web_app_setup_back_button', eventData: { is_visible: Boolean(configuration.selectedItem) } }), location.origin);
window.parent.postMessage(JSON.stringify({ eventType: 'web_app_ready', eventData: {} }), location.origin);
render();

async function manage(item) {
    if (busy) return;
    if (!item.listed) {
        openPopup('fragment-selling');
        document.querySelector('#fragment-listing-form [name=kind]').value = item.kind;
        document.querySelector('#fragment-listing-form [name=username]').value = item.title;
        document.querySelector('#fragment-listing-form [name=sell_value]').value = item.amount;
        document.querySelector('#fragment-selling .js-username').textContent = item.title;
        document.querySelector('#fragment-selling .js-bid_value').textContent = item.amount;
        updateSaleSelection();
        document.querySelector('#fragment-number-options').hidden = item.kind !== 'number';
        document.querySelector('#fragment-listing-form [name=countryCode]').value = item.countryCode || '';
        document.querySelector('#fragment-listing-form [name=displayFormat]').value = item.displayTitle || item.title;
        return;
    }
    busy = true;
    try {
        await api('listing', { itemId: item.id }, 'DELETE');
        catalog = await api('catalog');
        render();
        if (configuration.selectedItem) location.reload();
    } catch (error) { status.textContent = error.message; }
    finally { busy = false; }
}

document.querySelector('#fragment-listing-form').onsubmit = async event => {
    let form = event.currentTarget;
    let fields = Object.fromEntries(new FormData(form));
    let data = { kind: fields.kind, value: fields.username, amount: fields.sell_value, countryCode: fields.countryCode, displayFormat: fields.displayFormat, alternativeId: fields.alternativeId, channelUsername: fields.channelUsername, password: fields.password };
    event.preventDefault();
    if (busy) return;
    busy = true;
    try {
        await api('listing', data);
        catalog = await api('catalog');
        render();
        status.textContent = 'Listing published.';
        if (configuration.selectedItem) location.reload();
        closePopup('fragment-selling');
    } catch (error) { document.querySelector('#fragment-selling-error').textContent = error.message; }
    finally { busy = false; }
};

function openPopup(id) {
    let popup = document.getElementById(id);
    if (!document.querySelector('.popup-container:not(.hide)')) popupOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    popup.classList.remove('hide');
    popup.setAttribute('aria-hidden', 'false');
    popup.querySelector('input,button,select')?.focus({ preventScroll: true });
}

function closePopup(id) {
    let popup = document.getElementById(id);
    popup.classList.add('hide');
    popup.setAttribute('aria-hidden', 'true');
    if (!document.querySelector('.popup-container:not(.hide)')) document.body.style.overflow = popupOverflow || '';
}

document.querySelectorAll('[data-popup-close]').forEach(button => { button.onclick = () => closePopup(button.dataset.popupClose); });
if (createListingButton) createListingButton.onclick = () => {
    document.querySelector('#fragment-listing-form').reset();
    document.querySelector('#fragment-selling .js-username').textContent = 'List an asset';
    document.querySelector('#fragment-selling-error').textContent = '';
    updateSaleSelection();
    openPopup('fragment-selling');
};
document.addEventListener('keydown', event => {
    let popup = document.querySelector('.popup-container:not(.hide)');
    let controls;
    let first;
    let last;
    if (!popup) return;
    if (event.key === 'Escape') closePopup(popup.id);
    if (event.key !== 'Tab') return;
    controls = [...popup.querySelectorAll('button:not([disabled]),input:not([disabled]),select:not([disabled])')];
    first = controls[0];
    last = controls.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
});

document.querySelector('#fragment-listing-form [name=sell_value]').oninput = event => { document.querySelector('#fragment-selling .js-bid_value').textContent = event.target.value || '0'; };

if (detailAction) detailAction.onclick = () => configuration.selectedItem.isOwned ? manage(configuration.selectedItem) : choose(configuration.selectedItem.id);

if (assignNumberButton) assignNumberButton.onclick = () => {
    assignmentForm.reset();
    document.querySelector('#fragment-assignment-error').textContent = '';
    openPopup('fragment-assignment');
};
if (assignmentForm) assignmentForm.onsubmit = async event => {
    let password = assignmentForm.querySelector('[name=password]').value;
    event.preventDefault();
    if (busy) return;
    busy = true;
    try {
        await api('assign', { itemId: configuration.selectedItem.id, password, clear: false });
        assignmentForm.reset();
        location.reload();
    } catch (error) { document.querySelector('#fragment-assignment-error').textContent = error.message; }
    finally { busy = false; }
};
requestAnimationFrame(() => { document.body.classList.remove('no-transition'); });

document.querySelector('#fragment-listing-form [name=kind]').onchange = event => { document.querySelector('#fragment-number-options').hidden = event.target.value !== 'number'; };

function updateSaleSelection() {
    let form = document.querySelector('#fragment-listing-form');
    let selected = (catalog.ownedAssets || []).find(asset => asset.title === form.querySelector('[name=username]').value);
    let replacements = (catalog.ownedAssets || []).filter(asset => selected && asset.kind === selected.kind && asset.id !== selected.id && !asset.listed && !asset.channelId);
    let alternatives = form.querySelector('[name=alternativeId]');
    let password = form.querySelector('[name=password]');
    if (!selected) { form.querySelector('[type=submit]').disabled = true; return; }
    form.querySelector('[name=kind]').value = selected.kind;
    document.querySelector('#fragment-selling .js-username').textContent = selected.displayTitle || selected.title;
    document.querySelector('#fragment-number-options').hidden = selected.kind !== 'number';
    document.querySelector('#fragment-primary-options').hidden = !selected.isPrimary && !selected.channelId;
    document.querySelector('#fragment-alternative-group').hidden = !selected.isPrimary;
    document.querySelector('#fragment-channel-name-group').hidden = !selected.channelId;
    form.querySelector('[name=channelUsername]').value = '';
    document.querySelector('label[for=fragment-channel-name]').textContent = 'New username for ' + (selected.channelTitle || 'the channel') + ' (optional)';
    password.required = Boolean(selected.isPrimary || selected.channelId);
    alternatives.required = Boolean(selected.isPrimary);
    alternatives.replaceChildren();
    replacements.forEach(asset => {
        let option = document.createElement('option');
        option.value = asset.id;
        option.textContent = asset.displayTitle || asset.title;
        alternatives.append(option);
    });
    form.querySelector('[name=countryCode]').value = selected.countryCode || '';
    form.querySelector('[name=displayFormat]').value = selected.displayTitle || selected.title;
    form.querySelector('[name=sell_value]').value = selected.amount || '';
    form.querySelector('[type=submit]').disabled = Boolean(selected.isPrimary && !replacements.length);
    document.querySelector('#fragment-selling-error').textContent = selected.isPrimary && !replacements.length ? 'You need another owned identifier of the same type before selling this primary identifier.' : selected.channelId ? selected.channelTitle + ' keeps this link until it sells, then becomes private. Give it a new username here to switch now.' : '';
}

document.querySelector('#fragment-listing-form [name=username]').onchange = updateSaleSelection;
