// the pages are dark whatever the client's theme, in Telegram too: its server
// writes this class, and the graphs read it to pick their colors
document.documentElement.classList.add('dark');

const stickersConfiguration = window.GreengramMiniapp;
let stickersPage = stickersConfiguration.page;
const stickersSlot = stickersConfiguration.slot || 1;
let stickersBusy = false;
let stickersZipBusy = false;

// tgsticker.js is a pinned capture and starts its worker at /js/..., the root
// of telegram.org. Here that's a 404 and every TGS preview stays blank.
const StickersNativeWorker = window.Worker;
window.Worker = function (url, options) {
    return new StickersNativeWorker(String(url).replace(/^\/js\//, '/__miniapps/stickers/js/'), options);
};

function stickersError(error) {
    let status = document.querySelector('#stickers-status');
    if (status) status.textContent = error.message || String(error);
    else if (window.TWebApp) TWebApp.showErrorToast(error.message || String(error));
}

function stickersLocation(value) {
    let url = new URL(value, location.href);
    if (url.origin !== location.origin || !url.pathname.startsWith('/stickers')) throw new Error('Open packs on this instance.');
    return url.href;
}

async function stickersRequest(method, data = {}) {
    let response;
    let result;
    data = { ...data, account: stickersSlot };
    response = await fetch('/__miniapps/stickers/api', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...data, method }) });
    result = await response.json();
    if (!response.ok || result.error) throw new Error(result.error || 'Request failed.');
    if (method === 'deleteStickerSet' && !result.msg) result.msg = 'Pack deleted.';
    return result;
}

async function uploadPackSticker(method, file, data, callback) {
    let response;
    let result;
    try {
        // a pack icon is emoji sized in any pack, and bot-stickers.js fits it to 100
        let emoji = stickersPage.isEmojis || data?.target === 'stickers_thumb';
        response = await fetch('/__miniapps/stickers/upload?kind=' + (emoji ? 'emoji' : 'sticker'), { method: 'POST', headers: { 'Content-Type': file.type || 'application/octet-stream' }, body: file });
        result = await response.json();
        if (!response.ok) throw new Error(result.error);
        result.doc_id = result.doc;
        result.media = { doc_id: result.doc };
        callback?.(result);
    } catch (error) { callback?.({ ok: false, error: error.message }); stickersError(error); }
}

// Whole packs as a ZIP. The server unpacks and checks it; what comes back is
// already stored, so it only has to look like a finished upload.
function stickersZipPick() {
    let input = document.createElement('input');
    input.type = 'file';
    input.accept = '.zip,application/zip,application/x-zip-compressed';
    input.style.display = 'none';
    input.addEventListener('change', () => { if (input.files[0]) stickersImportZip(input.files[0]); input.remove(); });
    input.addEventListener('cancel', () => input.remove());
    document.body.append(input);
    input.click();
}

function isStickersZip(file) {
    return /\.zip$/i.test(file.name) || ['application/zip', 'application/x-zip-compressed'].includes(file.type);
}

function stickersMediaNode(item, classes) {
    let node;
    if (item.mime === 'application/x-tgsticker') {
        node = document.createElement('picture');
        let source = document.createElement('source');
        source.type = 'application/x-tgsticker';
        source.srcset = item.thumb;
        node.append(source);
    } else if (item.mime === 'video/webm') {
        node = document.createElement('video');
        node.muted = true;
        node.src = item.thumb;
    } else {
        node = document.createElement('img');
        node.src = item.thumb;
    }
    if (classes) node.className = classes;
    return node;
}

function stickersAddImported(item) {
    let type = item.mime === 'application/x-tgsticker' ? 'tgs' : item.mime === 'video/webm' ? 'webm' : 'webp';
    if (stickersPage.page === 'edit') {
        let row = $(`<div class="tm-row tm-sticker-row tm-sticker-row-edit js-sticker-row"><span class="tm-icon js-sortable-handle ui-sortable-handle" style="--icon-s: var(--image-url-rearrange)"></span><span class="js-zip-pic"></span><input type="text" class="form-control tm-input" name="emoji" autocomplete="off" spellcheck="false"><span class="tm-icon tm-icon-end tm-toggle-emoji js-toggle-panel"></span><span class="tm-icon tm-icon-end tm-icon-close js-delete-sticker"></span></div>`);
        let pic = stickersMediaNode(item, 'tm-row-pic');
        row.attr({ 'data-doc-id': item.doc, id: 'i' + item.doc });
        row.find('input').attr('placeholder', l('WEB_EDITPACK_EMOJI_PLACEHOLDER')).val(item.emoji);
        row.find('.js-zip-pic').replaceWith(pic);
        $('.js-sortable-table').append(row);
        if (type === 'tgs') RLottie.init(pic, { noAutoPlay: true });
        return;
    }
    let cell = stickersMediaNode(item, 'tm-grid-item tm-grid-item-sticker' + (type === 'webp' ? '' : ' tm-grid-item-sticker-animated'));
    if (type !== 'tgs') {
        // same shape as NewPack.addFile: the grid styles a wrapper around these
        let wrap = document.createElement('div');
        wrap.className = cell.className;
        cell.removeAttribute('class');
        wrap.append(cell);
        cell = wrap;
    }
    $(cell).attr({ 'data-doc': item.doc, 'data-blob': item.thumb, 'data-type': type, 'data-emoji': item.emoji || '' });
    $('.tm-grid-area').append(cell);
    if (type === 'tgs') RLottie.init(cell, { noAutoPlay: true });
}

function stickersZipReport(name, result) {
    let box = document.querySelector('.js-zip-report');
    let skipped = result.skipped || [];
    let lines = [`${result.items.length} imported from ${name}.`];
    box.textContent = '';
    if (result.left_out) lines.push(`${result.left_out} left out, the pack is full.`);
    box.append(lines.join(' '));
    if (!skipped.length) return;
    let details = document.createElement('details');
    let summary = document.createElement('summary');
    let list = document.createElement('ul');
    summary.textContent = `${skipped.length + (result.skipped_more || 0)} skipped`;
    for (let entry of skipped) {
        let line = document.createElement('li');
        line.textContent = `${entry.name}: ${entry.reason}`;
        list.append(line);
    }
    details.append(summary, list);
    box.append(details);
}

async function stickersImportZip(file) {
    let box = document.querySelector('.js-zip-report');
    let have = stickersPage.page === 'edit' ? document.querySelectorAll('.js-sticker-row').length : document.querySelectorAll('.tm-grid-item[data-doc]').length;
    if (stickersZipBusy || !box) return;
    if (file.size > 64 * 1024 * 1024) { TWebApp.showErrorToast('Upload a ZIP of up to 64 MB.'); return; }
    stickersZipBusy = true;
    box.textContent = `Importing ${file.name}…`;
    try {
        let response = await fetch(`/__miniapps/stickers/import?kind=${stickersPage.isEmojis ? 'emoji' : 'sticker'}&have=${have}`, { method: 'POST', headers: { 'Content-Type': 'application/zip' }, body: file });
        let result = await response.json();
        if (!response.ok || !result.ok) throw new Error(result.error || 'Import failed.');
        result.items.forEach(stickersAddImported);
        let title = document.querySelector('input[name=title]');
        if (title && !title.value.trim() && result.items.length) title.value = file.name.replace(/\.zip$/i, '').slice(0, 64);
        stickersZipReport(file.name, result);
        if (result.items.length) TWebApp.showSuccessToast(`${result.items.length} imported.`);
        else TWebApp.showErrorToast('Nothing in this ZIP could be used.');
    } catch (error) {
        box.textContent = error.message || String(error);
        TWebApp.showErrorToast(error.message || String(error));
    } finally {
        stickersZipBusy = false;
    }
}

// What this server takes is wider than the original's checks, which allow one
// square canvas and re-encode everything onto it. Pictures keep their shape and
// only shrink to fit; the server's checks stay the ones that count.
const STICKER_MAX_BYTES = 1 << 20;

function stickersFitImage(file, callback, target) {
    let limit = target === 'emoji' || target === 'stickers_thumb' ? 100 : 512;
    let url = URL.createObjectURL(file);
    let image = new Image();
    image.onerror = () => { URL.revokeObjectURL(url); TWebApp.showErrorToast(l('WEB_UPLOAD_UNSUPPORTED_FORMAT')); };
    image.onload = () => {
        let w = image.naturalWidth, h = image.naturalHeight;
        if (['image/png', 'image/webp'].includes(file.type) && w <= limit && h <= limit && file.size <= STICKER_MAX_BYTES) {
            URL.revokeObjectURL(url);
            callback(file);
            return;
        }
        let ratio = Math.min(1, limit / Math.max(w, h));
        let canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(w * ratio));
        canvas.height = Math.max(1, Math.round(h * ratio));
        canvas.getContext('2d').drawImage(image, 0, 0, canvas.width, canvas.height);
        URL.revokeObjectURL(url);
        let encode = quality => canvas.toBlob(blob => {
            if (blob && blob.size > STICKER_MAX_BYTES && quality > 0.5) return encode(quality - 0.2);
            if (!blob || blob.size > STICKER_MAX_BYTES) return TWebApp.showErrorToast('Upload a file between 1 byte and 1 MB.');
            // Safari answers with a PNG when asked for WEBP; the server takes both
            callback(new File([blob], file.name + (blob.type === 'image/webp' ? '.webp' : '.png'), { type: blob.type }));
        }, 'image/webp', quality);
        encode(0.92);
    };
    image.src = url;
}

window.processImage = stickersFitImage;

function stickersInputFile(file, callback) {
    let name = file.name.toLowerCase();
    if (isStickersZip(file)) return stickersImportZip(file);
    if (file.type.startsWith('image/')) return stickersFitImage(file, callback, stickersPage.isEmojis ? 'emoji' : 'sticker');
    if (file.type === 'video/webm' || name.endsWith('.webm')) {
        let limit = stickersPage.isEmojis ? 100 : 512;
        let video = document.createElement('video');
        video.addEventListener('loadedmetadata', () => {
            if (Math.max(video.videoWidth, video.videoHeight) > limit) return TWebApp.showErrorToast(l('WEB_UPLOAD_BAD_DIMENSIONS'));
            if (video.duration > 3.05) return TWebApp.showErrorToast(l('WEB_UPLOAD_LONG_VIDEO'));
            callback(file.type ? file : new File([file], file.name, { type: 'video/webm' }));
        });
        video.addEventListener('error', () => TWebApp.showErrorToast(l('WEB_UPLOAD_UNSUPPORTED_FORMAT')));
        video.src = URL.createObjectURL(file);
        return;
    }
    if (file.type.startsWith('video/')) return TWebApp.showErrorToast(l('WEB_UPLOAD_UNSUPPORTED_FORMAT'));
    callback(new File([file], file.name, { type: 'application/x-tgsticker' }));
}

function stickersPickFiles(add) {
    let input = document.createElement('input');
    input.type = 'file';
    input.multiple = true;
    input.accept = 'image/*,video/webm,.webm,.tgs,.zip';
    input.style.display = 'none';
    input.addEventListener('change', () => { [...input.files].forEach(file => stickersInputFile(file, add)); input.remove(); });
    input.addEventListener('cancel', () => input.remove());
    document.body.append(input);
    input.click();
}

// tgsticker.js draws one frame of each of these; a pack can hold hundreds, so
// only the ones coming into view.
const stickersLottieWatch = new IntersectionObserver(entries => {
    for (let entry of entries) {
        if (!entry.isIntersecting) continue;
        stickersLottieWatch.unobserve(entry.target);
        RLottie.init(entry.target, { noAutoPlay: true });
    }
}, { rootMargin: '200px' });

function stickersDrawThumbs(root = document) {
    root.querySelectorAll('picture.js-lottie-thumb:not(.js-lottie-seen)').forEach(element => {
        element.classList.add('js-lottie-seen');
        stickersLottieWatch.observe(element);
    });
}

// the picker and its search results arrive after the page
const stickersNewThumbs = new MutationObserver(() => stickersDrawThumbs());

// The picker hands its choice back as an image link, which isn't enough for an
// animated sticker, so the original's <img> is swapped for what the picker showed.
function stickersPickerMedia(event) {
    for (let chosen of event.originalEvent.detail) {
        let shown = document.querySelector(`.js-sticker-picker-item[data-raw="${chosen.stickerRaw}"] .tm-sticker-picker-item-img`);
        if (!shown || shown.tagName === 'IMG') continue;
        document.querySelectorAll(`img[src="${chosen.thumb}"]`).forEach(image => {
            let copy = shown.cloneNode(true);
            copy.className = image.className + (shown.tagName === 'PICTURE' ? ' js-lottie-thumb' : '');
            copy.replaceChildren(...[...shown.querySelectorAll('source')].map(source => source.cloneNode()));
            image.replaceWith(copy);
        });
    }
    stickersDrawThumbs();
}

function initStickersUploads(add) {
    StickerUpload.inputFile = stickersInputFile;
    $('.js-upload-btn').off('click').on('click', () => stickersPickFiles(add));
    $('.js-import-zip').on('click', stickersZipPick);
    $(document).on('sticker-picker-input.curPage', stickersPickerMedia);
    document.querySelectorAll('.js-drop-area').forEach(area => area.addEventListener('dragover', event => event.preventDefault()));
    let submit = Aj.state.onMainButton;
    Aj.state.onMainButton = function (event) {
        if (stickersZipBusy) { TWebApp.showErrorToast('Wait for the import to finish.'); return; }
        return submit.call(this, event);
    };
}

async function submitStickerPack(event) {
    if (event) event.preventDefault();
    if (stickersBusy) return;
    if (document.querySelector('.tm-sticker-loading')) { stickersError(new Error('Wait for the uploads to finish.')); return; }
    NewPack.submit();
}

function stickersRenderCharts() {
    StickerChart.init();
    for (let chart of stickersPage.charts || []) {
        // the original's source strings would go through eval, and the CSP stops it
        renderGraph(chart.id, { ...chart.json, xTickFormatter: statsFormat('day'), xTooltipFormatter: statsTooltipFormat('day'), xRangeFormatter: statsFormatEmpty, yTickFormatter: null, yTooltipFormatter: statsFormatTooltipValue }, true);
    }
}

async function initStickersEdit() {
    // the emoji panel's data comes with each page on Telegram; here it's one file
    let groups = await fetch('/__greengram/stickers-emoji.json').then(response => response.json()).catch(() => []);
    Aj.state.emojiGroupedList = groups;
    Aj.state.emojiRE = '(?!)';
    EditPack.init();
    initStickersUploads(EditPack.addFile);
}

function initStickersList() {
    let stored = document.cookie.split(';').map(value => value.trim()).find(value => value.startsWith('stel_bot_stickers_settings='));
    let settings = {};
    let display;
    let type;
    let query = new URL(location.href).searchParams;
    try { settings = stored ? JSON.parse(decodeURIComponent(stored.slice(stored.indexOf('=') + 1))) : {}; } catch { settings = {}; }
    display = settings.d === 'grid' ? 'grid' : 'list';
    type = query.get('emoji') === '1' || ['emoji', 'emojis'].includes(query.get('type')) || settings.t === 'emojis' ? 'emojis' : 'stickers';
    Aj.state.display = display;
    Aj.state.type = type;
    $('main').toggleClass('tm-mode-grid', display === 'grid');
    $('[data-scope=display]').each((index, element) => element.classList.toggle('active', element.dataset.tab === display));
    $('[data-scope=type]').each((index, element) => element.classList.toggle('active', element.dataset.tab === type));
    $('.js-tab-content[data-tab]').each((index, element) => element.classList.toggle('hidden', element.dataset.tab !== type));
    document.querySelector('.tm-main-search-form').addEventListener('submit', event => event.preventDefault());
    MainPage.init();
}

async function initStickers() {
    if (stickersPage.page === 'redirect') { location.replace(stickersPage.redirect); return; }
    let openTelegramLink = WebApp.openTelegramLink.bind(WebApp);
    WebApp.openTelegramLink = function (value) {
        let url = new URL(value, location.href);
        if (url.origin === location.origin) {
            window.open(url.href, '_blank', 'noopener');
            return;
        }
        return openTelegramLink(value);
    };
    ajInit({ version: 650, apiUrl: '/__miniapps/stickers/api', state: { ...stickersPage } });
    Aj.apiRequest = function (method, params, callback) {
        let saving = method === 'createStickerSet' || method === 'updateStickerSet' || method === 'publishStickerSet';
        if (saving && stickersBusy) return;
        if (saving) stickersBusy = true;
        stickersRequest(method, params).then(result => callback?.(result)).catch(error => { callback?.({ ok: false, error: error.message }); stickersError(error); }).finally(() => { if (saving) stickersBusy = false; });
    };
    Aj.uploadRequest = uploadPackSticker;
    Aj.location = value => value === undefined ? new URL(location.href) : location.assign(stickersLocation(value));
    // the original's strings, from its pages' l.add calls
    l.add({ WEB_UPLOAD_UNSUPPORTED_FORMAT: 'File format is not supported', WEB_UPLOAD_LONG_VIDEO: 'Video is too long', WEB_UPLOAD_BAD_DIMENSIONS: 'Wrong dimensions', WEB_STICKER_PICKER_ADD: 'Add', WEB_MAIN_NO_RESULTS_INFO: 'There were no results for “{query}”', WEB_POPUP_DELETE_TITLE: 'Delete Pack', WEB_POPUP_DELETE: 'Are you sure you want to delete this pack?', WEB_POPUP_DELETE_BTN: 'Delete', WEB_LINK_COPIED: 'Link copied.', WEB_NEWPACK_STICKERS_REQUIRED: stickersPage.isEmojis ? 'Upload at least one emoji.' : 'Upload at least one sticker.', WEB_NEWPACK_LINK_LOADING: 'Checking the link', WEB_NEWPACK_CREATE: 'Create', WEB_SAFARI_WEBM_ISSUE: 'This browser may have limited webm support', WEB_POPUP_DELETE_STICKER_TITLE: 'Delete Sticker', WEB_POPUP_DELETE_STICKER: 'Are you sure you want to delete this sticker?', WEB_POPUP_DELETE_EMOJI_TITLE: 'Delete Emoji', WEB_POPUP_DELETE_EMOJI: 'Are you sure you want to delete this emoji?', WEB_NEWPACK_NAME_REQUIRED: 'Name is required.', WEB_THUMB_UPDATE_SUCCESS: 'Pack Icon updated successfully.', WEB_EDITPACK_SAVE: 'Save', WEB_EDITPACK_EMOJI_PLACEHOLDER: 'Enter Emoji', WEB_PUBLISHPACK_PUBLISH: 'Publish' });
    TWebApp.init('/stickers');
    if (stickersPage.page === 'list') initStickersList();
    if (stickersPage.page === 'editor') {
        NewPack.init();
        Aj.state.onMainButton = submitStickerPack;
        initStickersUploads(NewPack.addFile);
    }
    if (stickersPage.page === 'edit') await initStickersEdit();
    if (stickersPage.page === 'publish') PublishPack.init();
    if (stickersPage.page === 'stats') stickersRenderCharts();
    if (stickersPage.page === 'pack') {
        PackPage.init();
        if (!stickersPage.own) {
            $('.tm-grid-area').sortable('disable');
            $('.js-show-stat,.js-delete-sticker,.js-set-pack-icon,.js-edit-emoji').remove();
            WebApp.MainButton.hide();
        }
    }
    stickersDrawThumbs();
    stickersNewThumbs.observe(document.body, { childList: true, subtree: true });
    Aj.pageLoaded();
}

document.addEventListener('click', function (event) {
    let anchor = event.target instanceof Element ? event.target.closest('a[href]') : undefined;
    let url = anchor ? new URL(anchor.href) : undefined;
    if (!url || url.origin !== location.origin || !url.pathname.startsWith('/stickers')) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    location.assign(stickersLocation(url.href));
}, true);

// Greengram: who is looking decides the page, so it comes from the server
// for this launch before the captured scripts start.
window.GreengramMiniappPage().then(function (page) {
    stickersPage = stickersConfiguration.page = page.state;
    document.querySelector('#aj_content').innerHTML = page.markup;
    document.title = page.title || document.title;
    return initStickers();
}).catch(window.GreengramMiniappFailed);
