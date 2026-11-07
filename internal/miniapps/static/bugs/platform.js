const parameters = new URLSearchParams(location.search);
const launchParameters = new URLSearchParams(location.hash.slice(1));
const launchData = launchParameters.get('tgWebAppData') || sessionStorage.getItem('greengram-bugs-launch') || '';
const slot = parameters.get('account') || '1';
const startParameter = new URLSearchParams(launchData).get('start_param') || launchParameters.get('tgWebAppStartParam');
const results = document.querySelector('#bugs-cards');
const layer = document.querySelector('#layer-popup-container');
const detailTemplate = layer.cloneNode(true);
const search = document.querySelector('.bt-main-search-form');
const themeButton = document.querySelector('.bt-theme-toggle');
const login = document.querySelector('.login-link');
const status = document.createElement('p');
const statuses = [['open', 'Open', 'c-cyan'], ['closed', 'Closed', 'c-orange'], ['fix coming', 'Fix coming', 'c-lightgreen'], ['fixed', 'Fixed', 'c-green']];
const statusLabels = Object.fromEntries(statuses.map(([value, label]) => [value, label]));
const statusColors = Object.fromEntries(statuses.map(([value, , color]) => [value, color]));
let user = { id: 0, name: '', isAdmin: false };
let generation = 0;
let detailGeneration = 0;
let cards = [];
let currentCard;
let isBusy = false;

if (!parameters.has('type') && startParameter === 'suggestions') parameters.set('type', 'suggestions');
if (window.parent !== window) {
    window.parent.postMessage(JSON.stringify({ eventType: 'web_app_ready', eventData: {} }), location.origin);
    window.parent.postMessage(JSON.stringify({ eventType: 'web_app_expand', eventData: {} }), location.origin);
}
if (launchData) sessionStorage.setItem('greengram-bugs-launch', launchData);
status.className = 'bt-issue-row-empty';
status.setAttribute('role', 'alert');
status.hidden = true;
results.before(status);
search.querySelector('[name="query"]').value = parameters.get('query') || '';
search.querySelector('[name="type"]').value = parameters.get('type') || '';
search.querySelector('[name="sort"]').value = parameters.get('sort') || 'rate';
search.querySelectorAll('input[type="date"]').forEach(input => { input.value = parameters.get(input.name) || ''; });

function makeElement(tag, className, text) {
    const element = document.createElement(tag);
    element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
}

function showError(error) {
    const message = makeElement('p', 'help-block bugs-error', error.message || 'Something went wrong. Try again.');
    status.textContent = error.message || 'Something went wrong. Try again.';
    status.hidden = false;
    layer.querySelectorAll('.bugs-error').forEach(node => node.remove());
    if (!layer.classList.contains('hide')) { message.setAttribute('role', 'alert'); layer.querySelector('.bt-layer-block').append(message); }
}

async function request(data, query = new URLSearchParams(location.search)) {
    const headers = { 'X-Greengram-Bugs': '1', 'X-Telegram-Init-Data': launchData };
    let response;
    let result;
    query.set('account', slot);
    if (data) headers['Content-Type'] = 'application/json';
    response = await fetch('/bugs/api?' + query, { method: data ? 'POST' : 'GET', credentials: 'same-origin', headers, body: data ? JSON.stringify(data) : undefined });
    result = await response.json();
    if (!response.ok) throw new Error(result.error || 'The request failed.');
    return result;
}

function formatDate(value) {
    const date = new Date(value);
    const options = { month: 'short', day: 'numeric' };
    if (date.getFullYear() !== new Date().getFullYear()) options.year = 'numeric';
    return date.toLocaleDateString('en-US', options);
}

function formatTime(value) {
    return new Date(value).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
}

// "Nov 7 at 18:44", or "Dec 23, 2020 at 14:57" for older years
function formatDateTime(value) {
    return formatDate(value) + ' at ' + formatTime(value);
}

// cards list and comments: time for today, date otherwise
function formatShort(value) {
    const date = new Date(value);
    return date.toDateString() === new Date().toDateString() ? formatTime(value) : formatDate(value);
}

function makeTime(value, text) {
    const time = makeElement('time', 'short', text);
    time.dateTime = new Date(value).toISOString();
    time.title = formatDateTime(value);
    return time;
}

function renderDescription(container, text) {
    const lines = text.split('\n');
    container.replaceChildren();
    lines.forEach((line, index) => { if (index) container.append(document.createElement('br')); container.append(document.createTextNode(line)); });
}

function renderFiles(files) {
    const container = layer.querySelector('.bt-issue-files');
    container.replaceChildren();
    files.forEach(file => {
        const link = makeElement('a', 'cd-issue-file file-loaded');
        const thumb = makeElement('div', 'cd-issue-file-thumb');
        const title = makeElement('div', 'cd-issue-file-title', file.name);
        const label = makeElement('div', 'bt-issue-file-label', Math.ceil(file.size / 1024) + ' KB');
        link.href = '/bugs/files/' + encodeURIComponent(file.id);
        link.target = '_blank';
        link.rel = 'noopener';
        if (['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(file.mime)) thumb.style.backgroundImage = 'url("' + link.href + '?thumb=1")';
        link.append(thumb, title, label);
        container.append(link);
    });
}

function readAttachment(file) {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve({ name: file.name, data: String(reader.result).split(',')[1] });
        reader.onerror = () => reject(new Error('Could not read ' + file.name));
        reader.readAsDataURL(file);
    });
}

function updateUser(value) {
    user = value;
    login.textContent = user.id ? user.name : 'Login';
    login.href = '/?account=' + slot;
}

function getLabel(card) {
    return card.kind === 'suggestion' ? 'Suggestion' : 'Issue';
}

function renderCard(card) {
    const row = document.querySelector('#bugs-card-template').content.firstElementChild.cloneNode(true);
    row.dataset.cardId = card.id;
    row.href = '/bugs/c/' + card.id + '?account=' + slot;
    row.querySelector('.bt-card-title').textContent = card.title;
    row.querySelector('.bt-card-preview').textContent = card.text;
    if (card.kind === 'bug') row.querySelector('.bt-card-thumb').className = 'bt-card-thumb thumb-animal thumb-icon cock thumb-color2';
    row.querySelector('.bt-card-thumb-status').textContent = card.kind === 'suggestion' ? 'Added' : 'Fixed';
    row.querySelector('.bt-card-thumb').classList.toggle('fixed', card.status === 'fixed');
    const cardStatus = makeElement('span', 'bt-card-status');
    if (card.status !== 'open' && card.status !== 'fixed') cardStatus.append(makeElement('span', 'add-info', statusLabels[card.status] || card.status));
    row.querySelector('.bt-issue-footer').append(makeTime(card.createdAt, formatShort(card.createdAt)), cardStatus, makeElement('span', 'add-info', getLabel(card)));
    row.querySelector('.cd-issue-replies .value').textContent = card.comments;
    row.querySelector('.cd-issue-like .value').textContent = card.likes - card.dislikes;
    row.addEventListener('click', event => { event.preventDefault(); openCard(card.id); });
    return row;
}

async function loadCards(append = false) {
    const active = ++generation;
    const query = new URLSearchParams(parameters);
    let result;
    let more;
    if (append) query.set('offset', cards.length);
    status.hidden = true;
    try {
        result = await request(undefined, query);
        if (active !== generation) return;
        updateUser(result.user);
        cards = append ? [...cards, ...result.cards] : result.cards;
        results.querySelectorAll('.bt-card-row, .bugs-more').forEach(row => row.remove());
        document.querySelector('h3.bt-header').textContent = cards.length ? cards.length + (cards.length === 1 ? ' Card' : ' Cards') : 'Cards';
        results.querySelector('.bt-issue-row-empty').hidden = cards.length > 0;
        cards.forEach(card => results.insertBefore(renderCard(card), results.querySelector('.bt-issue-row-empty')));
        document.querySelectorAll('.bt-sort-item').forEach(item => item.classList.toggle('selected', item.dataset.value === (query.get('sort') || 'rate')));
        document.querySelectorAll('.bt-search-tabs > .bt-search-tab').forEach(item => item.classList.toggle('selected', (item.dataset.value ?? new URL(item.href || '/bugs', location.origin).searchParams.get('type') ?? '') === (query.get('type') || '')));
        if (result.cards.length === 50) {
            more = makeElement('button', 'btn btn-default bugs-more', 'Show more');
            more.type = 'button';
            more.addEventListener('click', () => loadCards(true));
            results.append(more);
        }
    } catch (error) { if (active === generation) showError(error); }
}

function showLayer() {
    layer.classList.remove('hide');
    layer.setAttribute('role', 'dialog');
    layer.setAttribute('aria-modal', 'true');
    document.body.classList.add('modal-open');
}

function closeLayer() {
    detailGeneration++;
    layer.classList.add('hide');
    document.body.classList.remove('modal-open');
    currentCard = undefined;
    history.replaceState({}, '', '/bugs?' + new URLSearchParams({ ...Object.fromEntries(parameters), account: slot }));
}

function restoreDetail() {
    layer.replaceChildren(...[...detailTemplate.childNodes].map(node => node.cloneNode(true)));
    layer.querySelector('.bt-layer-close').addEventListener('click', event => { event.preventDefault(); closeLayer(); });
}

function renderComments(comments) {
    const container = layer.querySelector('.bt-comments');
    container.replaceChildren();
    comments.forEach(comment => {
        const node = document.querySelector('#bugs-comment-template').content.firstElementChild.cloneNode(true);
        node.dataset.commentId = comment.id;
        node.querySelector('.bt-comment-photo').replaceChildren(makeElement('span', 'photo-char', (comment.author.trim()[0] || '?').toUpperCase()));
        node.querySelector('.bt-comment-author-name').replaceChildren(makeElement('span', '', comment.author));
        node.querySelector('.bt-comment-author-name span').dir = 'auto';
        node.querySelector('.bt-comment-text').textContent = comment.text;
        node.querySelector('.bt-comment-date').replaceChildren(makeTime(comment.createdAt, formatShort(comment.createdAt)));
        node.querySelector('.bt-comment-date').href = '#comment-' + comment.id;
        node.id = 'comment-' + comment.id;
        container.append(node);
    });
    layer.querySelector('.bt-header-cnt').textContent = comments.length;
}

async function mutate(data, button) {
    let result;
    if (isBusy) return;
    if (!user.id) { showError(new Error('Sign in to Greengram to continue.')); return; }
    isBusy = true;
    if (button) button.disabled = true;
    status.hidden = true;
    try {
        result = await request(data);
        if (data.action === 'create') await openCard(result.id);
        else if (data.action === 'edit') await openCard(data.id);
        else if (data.action === 'delete') closeLayer();
        else if (currentCard) await openCard(currentCard.id);
        await loadCards();
    } catch (error) { showError(error); }
    finally {
        isBusy = false;
        if (button) button.disabled = false;
    }
}

function renderCommentForm(card) {
    const footer = layer.querySelector('.bt-comments-footer');
    const form = layer.querySelector('.bt-comments-footer-form');
    const buttons = makeElement('div', 'bt-comments-footer-buttons');
    const toggleWrap = makeElement('div', 'bt-toggle-comment-form-wrap');
    const toggle = makeElement('a', 'bt-toggle-comment-form', 'Add info about this suggestion/issue');
    const group = makeElement('div', 'form-group');
    const field = makeElement('div', 'bt-form-input');
    const input = makeElement('div', 'input form-control cd-form-control cd-issue-input empty');
    const submit = makeElement('button', 'btn btn-primary cd-submit-issue-btn', 'Comment');
    const clear = makeElement('div', 'clearfix');
    toggle.href = '#';
    toggle.addEventListener('click', event => {
        event.preventDefault();
        if (!user.id) { showError(new Error('Sign in to Greengram to comment.')); return; }
        footer.classList.remove('collapsed');
        input.focus();
    });
    toggleWrap.append(toggle);
    buttons.append(toggleWrap);
    input.contentEditable = 'true';
    input.dataset.placeholder = 'Add info about this suggestion/issue';
    input.setAttribute('role', 'textbox');
    input.setAttribute('aria-multiline', 'true');
    input.setAttribute('aria-label', 'Comment');
    input.addEventListener('input', () => input.classList.toggle('empty', !input.textContent.trim()));
    input.addEventListener('keydown', event => { if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); form.requestSubmit(); } });
    submit.type = 'submit';
    field.append(input);
    group.append(field);
    form.append(group, submit, clear);
    footer.classList.add('collapsed');
    footer.prepend(buttons);
    form.addEventListener('submit', event => {
        event.preventDefault();
        if (!input.innerText.trim()) { input.focus(); return; }
        mutate({ action: 'comment', id: card.id, text: input.innerText }, submit);
    });
}

function renderIssueLabel(card) {
    const label = layer.querySelector('.bt-issue-label');
    const author = makeElement('span', 'bt-issue-author');
    const name = makeElement('span', '', card.author);
    const time = makeElement('time', '', formatDateTime(card.createdAt));
    name.dir = 'auto';
    time.dateTime = new Date(card.createdAt).toISOString();
    author.append(name);
    label.replaceChildren(author, ', ', time);
}

function renderTypeTag(card) {
    const tag = makeElement('a', 'bt-tag', getLabel(card));
    const type = card.kind === 'suggestion' ? 'suggestions' : 'issues';
    tag.href = '/bugs?' + new URLSearchParams({ type, account: slot });
    tag.addEventListener('click', event => { event.preventDefault(); closeLayer(); updateFilters({ type }); });
    layer.querySelector('.bt-issue-tags').append(tag);
}

function renderStatus(card) {
    const tags = layer.querySelector('.bt-issue-tags');
    const color = statusColors[card.status] || 'c-cyan';
    const label = statusLabels[card.status] || card.status;
    let link;
    if (!user.isAdmin) {
        link = makeElement('a', 'bt-status ' + color, label);
        link.href = '/bugs?' + new URLSearchParams({ status: card.status, account: slot });
        link.addEventListener('click', event => { event.preventDefault(); closeLayer(); updateFilters({ status: card.status }); });
        tags.append(link);
        return;
    }
    const wrap = makeElement('span', 'bt-status-dd-wrap bt-dropdown-wrap');
    const toggle = makeElement('span', 'bt-status ' + color + ' bt-dropdown dropdown-toggle', label);
    const menu = makeElement('span', 'dropdown-menu');
    const list = makeElement('ul', 'dropdown-menu');
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    svg.setAttribute('class', 'dd');
    use.setAttributeNS('http://www.w3.org/1999/xlink', 'xlink:href', '#icon-dd');
    svg.append(use);
    toggle.append(svg);
    toggle.tabIndex = 0;
    toggle.setAttribute('role', 'button');
    toggle.setAttribute('aria-haspopup', 'menu');
    toggle.setAttribute('aria-label', 'Status: ' + label);
    list.setAttribute('role', 'menu');
    const setOpen = open => { wrap.classList.toggle('open', open); toggle.setAttribute('aria-expanded', String(open)); };
    toggle.addEventListener('click', () => setOpen(!wrap.classList.contains('open')));
    toggle.addEventListener('keydown', event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); setOpen(!wrap.classList.contains('open')); } });
    statuses.forEach(([value, text]) => {
        const item = makeElement('li', value === card.status ? 'selected' : '');
        const option = makeElement('a', 'bt-dropdown-item bt-set-status-btn', text);
        option.href = '#';
        option.dataset.status = value;
        option.setAttribute('role', 'menuitemradio');
        option.setAttribute('aria-checked', String(value === card.status));
        option.addEventListener('click', event => {
            event.preventDefault();
            setOpen(false);
            if (value === card.status || toggle.classList.contains('disabled')) return;
            toggle.classList.add('disabled');
            mutate({ action: 'status', id: card.id, status: value }).finally(() => toggle.classList.remove('disabled'));
        });
        item.append(option);
        list.append(item);
    });
    menu.append(list);
    wrap.append(toggle, menu);
    setOpen(false);
    tags.append(wrap);
}

async function openCard(id) {
    const active = ++detailGeneration;
    let result;
    let card;
    try {
        result = await request(undefined, new URLSearchParams({ id }));
        if (active !== detailGeneration) return;
        card = result.cards[0];
        updateUser(result.user);
        currentCard = { ...card, files: result.files || [] };
        restoreDetail();
        layer.querySelector('.bt-issue-title').replaceChildren(makeElement('span', 'cd-author', card.title));
        renderIssueLabel(card);
        renderDescription(layer.querySelector('.bt-issue-text'), card.text);
        renderFiles(result.files || []);
        renderStatus(card);
        renderTypeTag(card);
        layer.querySelector('.cd-issue-like .value').textContent = card.likes;
        layer.querySelector('.cd-issue-dislike .value').textContent = card.dislikes;
        layer.querySelector('.cd-issue-like').classList.toggle('selected', card.vote === 1);
        layer.querySelector('.cd-issue-dislike').classList.toggle('selected', card.vote === -1);
        layer.querySelector('.cd-issue-like').addEventListener('click', () => mutate({ action: 'vote', id: card.id, vote: card.vote === 1 ? 0 : 1 }));
        layer.querySelector('.cd-issue-dislike').addEventListener('click', () => mutate({ action: 'vote', id: card.id, vote: card.vote === -1 ? 0 : -1 }));
        layer.querySelector('.bt-issue-copy-link').href = location.origin + '/bugs/c/' + card.id;
        layer.querySelector('.bt-issue-copy-link').addEventListener('click', event => { event.preventDefault(); navigator.clipboard.writeText(location.origin + '/bugs/c/' + card.id).catch(showError); });
        renderComments(result.comments || []);
        renderCommentForm(card);
        renderCardMenu(currentCard);
        showLayer();
        history.replaceState({}, '', '/bugs/c/' + card.id + '?account=' + slot);
    } catch (error) { if (active === detailGeneration) showError(error); }
}

function openCreate(kind, existing) {
    const heading = existing ? 'Edit card' : kind === 'suggestion' ? 'Suggest a feature' : 'Report a problem';
    const form = makeElement('form', 'cd-form cd-issue-form');
    const titleGroup = makeElement('div', 'form-group');
    const titleWrap = makeElement('div', 'bt-form-input');
    const title = makeElement('input', 'form-control cd-form-control');
    const bodyGroup = makeElement('div', 'form-group');
    const bodyWrap = makeElement('div', 'bt-form-input');
    const body = makeElement('textarea', 'form-control cd-form-control');
    const notice = makeElement('p', 'help-block', 'Cards and comments are public. Do not include passwords, phone numbers or other private information.');
    const submit = makeElement('button', 'btn btn-primary', 'Submit');
    const attachmentInput = makeElement('input', 'hide');
    const attachmentButton = makeElement('label', 'btn btn-default bt-attach-btn', 'Attach files');
    const attachmentList = makeElement('div', 'bt-issue-files');
    let selectedFiles = [];
    let isPreparing = false;
    if (!user.id) { showError(new Error('Sign in to Greengram to submit a card.')); return; }
    currentCard = undefined;
    detailGeneration++;
    restoreDetail();
    layer.querySelector('.bt-issue-title').textContent = heading;
    layer.querySelector('.bt-issue-label').textContent = user.name;
    layer.querySelectorAll('.bt-issue-tags, .bt-issue-actions, .bt-layer-block:nth-child(2)').forEach(node => node.remove());
    title.name = 'title';
    title.placeholder = 'Title';
    title.setAttribute('aria-label', 'Title');
    title.required = true;
    title.minLength = 3;
    title.maxLength = 200;
    title.value = existing?.title || parameters.get('query') || search.querySelector('[name="query"]').value;
    body.name = 'description';
    body.placeholder = kind === 'suggestion' ? 'Describe your idea and how it would help.' : 'Steps to reproduce, what happened, and what you expected instead.';
    body.setAttribute('aria-label', 'Description');
    body.required = true;
    body.maxLength = 12000;
    body.rows = 8;
    body.value = existing?.text || '';
    submit.type = 'submit';
    titleWrap.append(title);
    titleGroup.append(titleWrap);
    bodyWrap.append(body);
    bodyGroup.append(bodyWrap);
    attachmentInput.type = 'file';
    attachmentInput.multiple = true;
    attachmentInput.id = 'bugs-attachments';
    attachmentButton.htmlFor = attachmentInput.id;
    attachmentInput.addEventListener('change', () => {
        const files = [...(attachmentInput.files || [])];
        if (files.length > 5 || files.some(file => file.size > 1048576 || file.size === 0)) { showError(new Error('Attach at most five files, up to 1 MB each.')); return; }
        selectedFiles = files;
        attachmentList.replaceChildren();
        files.forEach(file => attachmentList.append(makeElement('div', 'cd-issue-file-title', file.name)));
    });
    form.append(titleGroup, bodyGroup, notice);
    if (!existing) form.append(attachmentButton, attachmentInput, attachmentList);
    form.append(submit);
    form.addEventListener('submit', async event => {
        let files;
        event.preventDefault();
        if (isBusy || isPreparing) return;
        isPreparing = true;
        submit.disabled = true;
        try {
            files = await Promise.all(selectedFiles.map(readAttachment));
            await mutate({ action: existing ? 'edit' : 'create', id: existing?.id, kind, title: title.value, text: body.value, files }, submit);
        } catch (error) { showError(error); }
        finally { isPreparing = false; submit.disabled = false; }
    });
    layer.querySelector('.bt-issue-text').replaceChildren(form);
    showLayer();
    title.focus();
}

function updateFilters(values) {
    Object.entries(values).forEach(([key, value]) => { if (value) parameters.set(key, value); else parameters.delete(key); });
    parameters.set('account', slot);
    document.querySelectorAll('.bt-main-search-form[data-bugs-filters]').forEach(renderFilterChips);
    document.querySelectorAll('.bt-main-search-form [name="type"]').forEach(input => { input.value = parameters.get('type') || ''; });
    document.querySelectorAll('.bt-main-search-form [name="sort"]').forEach(input => { input.value = parameters.get('sort') || 'rate'; });
    history.replaceState({}, '', '/bugs?' + parameters);
    loadCards();
}

function setTheme(dark) {
    document.documentElement.classList.toggle('bt-theme-dark', dark);
    themeButton.setAttribute('aria-pressed', String(dark));
    themeButton.setAttribute('aria-label', dark ? 'Switch to light theme' : 'Switch to dark theme');
    localStorage.setItem('greengram-bugs-theme', dark ? 'dark' : 'light');
}

setTheme(localStorage.getItem('greengram-bugs-theme') ? localStorage.getItem('greengram-bugs-theme') === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches);
document.querySelector('.bt-view-toggle').addEventListener('click', event => {
    const query = new URLSearchParams(parameters);
    event.preventDefault();
    query.set('account', slot);
    if (query.get('view') === 'board') query.delete('view'); else query.set('view', 'board');
    location.assign('/bugs?' + query);
});
themeButton.addEventListener('click', () => setTheme(!document.documentElement.classList.contains('bt-theme-dark')));
search.addEventListener('submit', event => { event.preventDefault(); updateFilters(Object.fromEntries(new FormData(search))); });
document.querySelectorAll('.bt-init-dialog-btn').forEach(button => button.addEventListener('click', () => openCreate(button.dataset.mode === 'suggestion' ? 'suggestion' : 'bug')));
document.querySelectorAll('.bt-sort-item').forEach(item => item.addEventListener('click', event => { event.preventDefault(); updateFilters({ sort: item.dataset.value }); }));
document.querySelectorAll('.bt-search-tabs > .bt-search-tab').forEach(item => {
    const type = new URL(item.href || '/bugs', location.origin).searchParams.get('type') || '';
    item.tabIndex = 0;
    item.setAttribute('role', 'button');
    item.addEventListener('click', event => { event.preventDefault(); updateFilters({ type }); });
    item.addEventListener('keydown', event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); updateFilters({ type }); } });
});
layer.addEventListener('click', event => { if (event.target === layer) closeLayer(); });
// one open dropdown at a time, any click outside closes it
document.addEventListener('click', event => {
    layer.querySelectorAll('.bt-dropdown-wrap.open').forEach(wrap => { if (!wrap.contains(event.target)) { wrap.classList.remove('open'); wrap.querySelector('.dropdown-toggle')?.setAttribute('aria-expanded', 'false'); } });
});
document.addEventListener('keydown', event => { if (event.key === 'Escape' && !layer.classList.contains('hide')) closeLayer(); });
await loadCards();
if (/^\/bugs\/c\/\d+$/.test(location.pathname)) openCard(Number(location.pathname.split('/').pop()));

function setupBoard() {
    const originalHeader = search.closest('.bt-search-header');
    const main = results.parentElement;
    const board = makeElement('div', 'bt-board');
    const columns = makeElement('div', 'bt-board-columns');
    const column = document.querySelector('#bt-board-column-template').content.firstElementChild.cloneNode(true);
    const boardSearch = column.querySelector('.bt-main-search-form');
    const sort = results.querySelector('.bt-sort-wrap');
    const count = results.querySelector('.bt-header');
    const close = column.querySelector('.bt-board-close');
    const add = column.querySelector('.bt-board-add');
    document.querySelector('.bt-container').classList.add('bt-board-mode');
    originalHeader.hidden = true;
    column.querySelector('.bt-sort-wrap').replaceWith(sort);
    count.classList.add('bt-board-count');
    column.querySelector('.bt-header').replaceWith(count);
    results.className = 'bt-board-results';
    column.querySelector('.bt-board-results').replaceWith(results);
    main.replaceChildren(board);
    board.append(columns);
    columns.append(column);
    boardSearch.querySelector('[name="type"]').value = parameters.get('type') || '';
    boardSearch.querySelector('[name="sort"]').value = parameters.get('sort') || 'rate';
    boardSearch.querySelector('[name="query"]').value = parameters.get('query') || '';
    boardSearch.querySelectorAll('[name="date_from"], [name="date_to"]').forEach(input => { input.value = parameters.get(input.name) || ''; });
    boardSearch.addEventListener('submit', event => { event.preventDefault(); updateFilters(Object.fromEntries(new FormData(boardSearch))); });
    boardSearch.querySelectorAll('.bt-board-type').forEach(item => item.addEventListener('click', event => { event.preventDefault(); updateFilters({ type: item.dataset.value }); }));
    close.addEventListener('click', () => { parameters.delete('view'); location.assign('/bugs?' + parameters); });
    add.remove();
    document.querySelector('.bt-view-toggle').setAttribute('aria-label', 'Switch to list view');
    document.querySelector('.bt-view-toggle').href = '/bugs?' + new URLSearchParams([...parameters].filter(([key]) => key !== 'view'));
}

if (parameters.get('view') === 'board') setupBoard();

function renderCardMenu(card) {
    const wrap = makeElement('span', 'bt-layer-side-button bt-menu-wrap bt-dropdown-wrap');
    const toggle = makeElement('span', 'bt-menu-btn bt-dropdown dropdown-toggle');
    const menu = makeElement('span', 'dropdown-menu');
    const list = makeElement('ul', 'dropdown-menu');
    toggle.tabIndex = 0;
    toggle.setAttribute('role', 'button');
    toggle.setAttribute('aria-label', 'Card actions');
    toggle.addEventListener('click', () => wrap.classList.toggle('open'));
    toggle.addEventListener('keydown', event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); wrap.classList.toggle('open'); } });
    const actions = [['Copy Link', () => navigator.clipboard.writeText(location.origin + '/bugs/c/' + card.id).catch(showError)]];
    if (user.id && (user.id === card.userId || user.isAdmin)) actions.push(['Edit', () => openCreate(card.kind, card)], ['Delete', () => askDelete(card)]);
    actions.forEach(([label, action]) => {
        const item = makeElement('li', '');
        const link = makeElement('a', 'bt-dropdown-item', label);
        link.href = '#';
        link.addEventListener('click', event => { event.preventDefault(); wrap.classList.remove('open'); action(); });
        item.append(link);
        list.append(item);
    });
    menu.append(list);
    wrap.append(toggle, menu);
    layer.querySelector('.bt-layer-side-buttons').append(wrap);
}

function askDelete(card) {
    const overlay = makeElement('div', 'popup-container');
    const popup = makeElement('div', 'popup');
    const body = makeElement('section', 'popup-body');
    const heading = makeElement('h4', '', 'Delete card?');
    const text = makeElement('p', 'popup-text', 'This card and its attachments will no longer be public.');
    const buttons = makeElement('div', 'popup-buttons');
    const cancel = makeElement('button', 'btn btn-default', 'Cancel');
    const remove = makeElement('button', 'btn btn-primary', 'Delete');
    overlay.setAttribute('role', 'alertdialog');
    overlay.setAttribute('aria-label', 'Delete card');
    cancel.addEventListener('click', () => overlay.remove());
    remove.addEventListener('click', async () => { await mutate({ action: 'delete', id: card.id }, remove); overlay.remove(); });
    buttons.append(cancel, remove);
    body.append(heading, text, buttons);
    popup.append(body);
    overlay.append(popup);
    document.body.append(overlay);
    cancel.focus();
}

function bindFilterMenu(form) {
    const select = form.querySelector('.bt-select-query .select');
    const input = form.querySelector('[name="query"]');
    const list = select.querySelector('.items-list');
    const parent = select.closest('.bt-form-input');
    const dates = form.querySelectorAll('input[type="date"]');
    if (form.dataset.bugsFilters) return;
    form.dataset.bugsFilters = 'true';
    statuses.forEach(([value, label, color]) => {
        const item = makeElement('div', 'search-item');
        const option = makeElement('span', 'select-list-item ' + color, label);
        option.tabIndex = 0;
        option.setAttribute('role', 'button');
        option.addEventListener('click', () => { select.classList.remove('open'); updateFilters({ status: value }); });
        option.addEventListener('keydown', event => { if (event.key === 'Enter') option.click(); });
        item.append(option);
        list.append(item);
    });
    dates.forEach(date => {
        const item = makeElement('div', 'search-item');
        const option = makeElement('span', 'select-list-item', date.name === 'date_from' ? 'From date' : 'To date');
        date.value = parameters.get(date.name) || '';
        date.max = new Date().toISOString().slice(0, 10);
        option.tabIndex = 0;
        option.setAttribute('role', 'button');
        option.addEventListener('click', () => { if (date.showPicker) date.showPicker(); else date.focus(); });
        option.addEventListener('keydown', event => { if (event.key === 'Enter') option.click(); });
        date.addEventListener('change', () => { select.classList.remove('open'); updateFilters({ [date.name]: date.value }); });
        item.append(option);
        list.append(item);
    });
    input.addEventListener('focus', () => { select.classList.add('open'); parent.classList.add('field-focused'); });
    input.addEventListener('keydown', event => { if (event.key === 'Escape') select.classList.remove('open'); });
    select.querySelector('.select-enter').addEventListener('click', () => form.requestSubmit());
    select.querySelector('.select-clear').addEventListener('click', () => { input.value = ''; updateFilters({ query: '', status: '', date_from: '', date_to: '' }); });
    document.addEventListener('click', event => { if (!select.contains(event.target)) { select.classList.remove('open'); parent.classList.remove('field-focused'); } });
    renderFilterChips(form);
}

function renderFilterChips(form) {
    const selected = form.querySelector('.selected-items');
    const input = form.querySelector('[name="query"]');
    selected.querySelectorAll('[data-bugs-filter]').forEach(chip => chip.remove());
    ['status', 'date_from', 'date_to'].forEach(key => {
        const value = parameters.get(key);
        let chip;
        let label;
        let close;
        if (!value) return;
        label = key === 'status' ? statusLabels[value] || value : (key === 'date_from' ? 'From ' : 'To ') + value;
        chip = makeElement('span', 'selected-item ' + (key === 'status' ? statusColors[value] || '' : ''), label);
        chip.dataset.bugsFilter = key;
        close = makeElement('span', 'close');
        close.tabIndex = 0;
        close.setAttribute('role', 'button');
        close.setAttribute('aria-label', 'Remove ' + label);
        close.addEventListener('click', () => updateFilters({ [key]: '' }));
        close.addEventListener('keydown', event => { if (event.key === 'Enter') close.click(); });
        chip.append(close);
        selected.insertBefore(chip, input);
    });
    form.querySelector('.bt-form-input').classList.toggle('field-has-value', Boolean(input.value || parameters.get('status') || parameters.get('date_from') || parameters.get('date_to')));
}

bindFilterMenu(search);
if (parameters.get('view') === 'board') bindFilterMenu(document.querySelector('.bt-board-search-form'));
