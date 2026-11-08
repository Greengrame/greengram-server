// Greengram sign-in: the /api/auth/* flow over HTTPS. Web A and the admin
// panel send people here; Web A then picks the session up by itself
// (/api/auth/webtoken), so this page only has to end with the cookie set.

const query = new URLSearchParams(location.search);
const slot = Number(query.get('account')) || 1;
const destinations = { admin: '/admin', chat: '/', link: '/', wallet: '/', botfather: '/' };
const next = destination();
const $ = id => document.getElementById(id);
let intent = 'login';
let busy = false;
let countries = [];
let country;
let filter;
let active = -1;
let siteName = 'Sign in';
// the instance makes sign-up numbers up itself (signupGeneratedNumbers)
let generated = false;
let madeNumber = '';

// next is one of the places above, or a path on this site: a link into
// the app opened while signed out, which the server sent here.
function destination() {
    let value = query.get('next') || '';
    let url = new URL(destinations[value] || '/', location.origin);
    if (value.startsWith('/')) {
        let asked = new URL(value, location.origin);
        if (asked.origin === location.origin && asked.pathname !== '/login') url = asked;
    }
    if (slot > 1) url.searchParams.set('account', slot);
    return url.pathname + url.search + url.hash;
}

async function api(path, body) {
    let response = await fetch('/api' + path, {
        method: body === undefined ? 'GET' : 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Greengram-Account': String(slot) },
        body: body === undefined ? undefined : JSON.stringify(body)
    });
    let data = await response.json().catch(() => ({}));
    if (!response.ok) throw Object.assign(new Error(data.error || 'Something went wrong. Try again.'), { status: response.status });
    return data;
}

const eyeOpen = 'M16 6.667c7.573 0 12.216 5.397 13.923 7.826.498.71.516 1.625.05 2.356-1.646 2.589-6.246 8.484-13.973 8.484S3.673 19.437 2.026 16.85c-.465-.731-.447-1.647.05-2.356C3.784 12.064 8.427 6.667 16 6.667m0 2.666c-3.682 0-6.667 2.985-6.667 6.667s2.985 6.666 6.667 6.667 6.667-2.985 6.667-6.667S19.682 9.333 16 9.333m.206 2.672C18.319 12.112 20 13.86 20 16l-.005.206C19.888 18.319 18.14 20 16 20l-.206-.005c-2.045-.104-3.685-1.744-3.789-3.79L12 16q0-.177.016-.349c.388.222.838.349 1.317.349C14.806 16 16 14.806 16 13.333c0-.48-.128-.929-.349-1.317Q15.823 12 16 12z';
const eyeCrossed = 'M4.33 5.789a1.334 1.334 0 0 1 1.749-.228l.133.102L27.545 24.33a1.333 1.333 0 0 1-1.624 2.109l-.133-.102-3.551-3.107c-1.821.863-3.926 1.437-6.232 1.437-7.767 0-13.338-6.612-13.338-8.667 0-1.153 1.769-3.743 4.687-5.793L4.455 7.67a1.333 1.333 0 0 1-.125-1.881zM10.667 16a5.333 5.333 0 0 0 8.341 4.404l-2.766-2.419A2 2 0 0 1 14 16.023l-2.766-2.42A5.315 5.315 0 0 0 10.666 16zm5.338-8.667c7.796 0 13.328 6.612 13.328 8.667 0 .848-.937 2.472-2.588 4.087l-5.456-4.775a5.335 5.335 0 0 0-5.02-4.639l-.291-.007-3.333-2.916a13.815 13.815 0 0 1 3.359-.418z';

function eye(path) {
    return '<svg viewBox="0 0 32 32" aria-hidden="true"><path d="' + path + '"/></svg>';
}

// Web A shows an error in place of the field's label, which can't wrap: only
// what fits goes there.
function say(text, form) {
    clearErrors();
    let group = form?.querySelector('[data-errors]');
    if (text && group && text.length <= 44) {
        let label = group.querySelector('label');
        label.dataset.label = label.textContent;
        label.textContent = text;
        group.classList.add('error');
        return;
    }
    $('status').textContent = text || '';
}

function clearErrors() {
    $('status').textContent = '';
    for (let group of document.querySelectorAll('.input-group.error')) {
        let label = group.querySelector('label');
        label.textContent = label.dataset.label;
        group.classList.remove('error');
    }
}

// Web A leaves out the submit button until there's something to send, and
// floats a field's label away once it has a value.
const canSend = {
    phone: () => skipsNumber() || $('phone-number').value.replace(/\D/g, '').length >= 4,
    password: () => $('password-value').value !== '',
    register: () => $('first-name').value.trim() !== '',
    username: () => $('username-value').value !== '' && $('username-password').value !== ''
};

function syncFields() {
    for (let input of document.querySelectorAll('.form-control')) input.closest('.input-group').classList.toggle('touched', input.value !== '');
    for (let [id, ready] of Object.entries(canSend)) $(id).querySelector('[data-submit]').hidden = !ready();
}

function skipsNumber() {
    return generated && intent === 'register';
}

// sign-up takes any number, so nobody should hand over their real one
function lead(step) {
    if (step === 'register') return madeNumber ? `Pick a name, a username and a password. Your number is ${madeNumber.replace(/^\+888(\d{4})(\d{4})$/, '+888 $1 $2')}: sign in with it or with your username.` : 'Pick a name, a username and a password. No SMS is sent.';
    if (step !== 'phone') return '';
    if (skipsNumber()) return 'You get a made-up number to sign in with, so your real one is never asked for.';
    if (intent === 'register') return "Don't use your real phone number: any number with 4–16 digits works, and no SMS is sent.";
    return 'Use the phone number and password of your account. No SMS is sent.';
}

// with a made-up number coming there is nothing to type on the phone step
function numberField(step) {
    let skip = step === 'phone' && skipsNumber();
    $('country-box').hidden = skip;
    $('phone-number').closest('.input-group').hidden = skip;
    $('phone-number').required = !skip;
}

function show(step) {
    for (let form of document.querySelectorAll('.step')) form.hidden = form.id !== step;
    let titles = { phone: intent === 'register' ? 'Create account' : siteName, password: 'Enter Password', register: 'Your Name', username: siteName };
    $('title').textContent = titles[step];
    $('lead').textContent = lead(step);
    numberField(step);
    $('register').querySelector('[data-back]').textContent = madeNumber ? 'Back' : 'Use another number';
    say('');
    syncFields();
    // like Web A, not on touch screens: the keyboard would cover the form
    if (matchMedia('(hover: hover)').matches) (step === 'phone' ? $('phone-number') : document.querySelector('#' + step + ' input'))?.focus();
}

// Web A keeps its auth key in localStorage; a key from another account
// would win over the new cookie, so drop it before going back. Every slot
// has its own; the keys without a number are slot 1's from before slots.
function forgetWebSession() {
    localStorage.removeItem('account' + slot);
    if (slot !== 1) return;
    for (let key of Object.keys(localStorage)) {
        if (/^(user_auth|dc\d*_(auth_key|server_salt|hash)|dc|tt-global-state)$/.test(key)) localStorage.removeItem(key);
    }
}

function done() {
    forgetWebSession();
    location.replace(next);
}

async function run(form, fn) {
    if (busy) return;
    busy = true;
    let submit = form.querySelector('[data-submit]');
    let label = submit.textContent;
    form.querySelectorAll('button').forEach(button => button.disabled = true);
    submit.classList.add('loading');
    submit.textContent = 'Please wait...';
    say('');
    try { await fn(); }
    catch (error) { say(error.message, form); }
    finally {
        busy = false;
        submit.classList.remove('loading');
        submit.textContent = label;
        form.querySelectorAll('button').forEach(button => button.disabled = false);
    }
}

function setIntent(value) {
    intent = value;
    $('switch-intent').textContent = value === 'register' ? 'I have an account' : 'Create account';
    show('phone');
}

function phoneNumber() {
    let digits = $('phone-number').value.replace(/\D/g, '');
    let code = country?.countryCode;
    if (code && !digits.startsWith(code)) digits = code + digits;
    return '+' + digits;
}

// The country picker: Web A's CountryCodeInput without Web A. Same list order,
// same word search, and picking one starts the number with its code.
function flagImage(iso2) {
    let flag = document.createElement('img');
    let code = iso2 === 'FT' ? '1f3f4-200d-2620-fe0f' : [...iso2.toUpperCase()].map(c => (c.codePointAt(0) + 0x1f1a5).toString(16)).join('-');
    flag.src = '/img-apple-64/' + code + '.png';
    // codes like +882/+883 (international networks) have no flag: show a globe
    flag.onerror = () => { flag.onerror = null; flag.src = '/img-apple-64/1f310.png'; };
    flag.alt = '';
    flag.draggable = false;
    flag.loading = 'lazy';
    return flag;
}

function matchesFilter(item, filter) {
    let words = filter.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean);
    let name = item.name.toLowerCase().split(/[^\p{L}\p{N}]+/u);
    return words.every(word => name.some(part => part.startsWith(word)));
}

function renderCountries() {
    let menu = $('country-menu');
    let list = filter ? countries.filter(item => matchesFilter(item, filter)) : countries;
    menu.textContent = '';
    active = -1;
    list.forEach(item => {
        let row = document.createElement('button');
        row.type = 'button';
        row.tabIndex = -1;
        row.className = 'MenuItem' + (country && country.iso2 === item.iso2 && country.countryCode === item.countryCode ? ' selected' : '');
        row.setAttribute('role', 'option');
        let flag = document.createElement('span');
        flag.className = 'country-flag';
        flag.append(flagImage(item.iso2));
        let name = document.createElement('span');
        name.className = 'country-name';
        name.textContent = item.name;
        let code = document.createElement('span');
        code.className = 'country-code';
        code.textContent = '+' + item.countryCode;
        row.append(flag, name, code);
        // on mousedown, or the input's blur closes the menu before the click lands
        row.addEventListener('mousedown', event => { event.preventDefault(); pickCountry(item); });
        menu.append(row);
    });
    if (!list.length) {
        let none = document.createElement('div');
        none.className = 'MenuItem no-results';
        none.textContent = 'Country not found';
        menu.append(none);
    }
}

function showCountry() {
    $('country').value = filter ?? (country ? country.name : '');
    syncFields();
}

function openCountries() {
    let box = $('country-box');
    if (box.classList.contains('open') || box.classList.contains('loading')) return;
    renderCountries();
    $('country-menu').hidden = false;
    $('country').setAttribute('aria-expanded', 'true');
    // the bubble scales in from hidden, so it needs a frame to start from
    requestAnimationFrame(() => {
        box.classList.add('open');
        $('country-menu').querySelector('.selected')?.scrollIntoView({ block: 'center' });
    });
}

function closeCountries() {
    filter = undefined;
    showCountry();
    $('country-box').classList.remove('open');
    $('country-menu').hidden = true;
    $('country').setAttribute('aria-expanded', 'false');
}

function pickCountry(item) {
    country = item;
    closeCountries();
    let number = $('phone-number');
    number.value = '+' + item.countryCode + ' ';
    syncFields();
    number.focus();
}

function moveActive(step) {
    let rows = [...$('country-menu').querySelectorAll('button')];
    if (!rows.length) return;
    rows[active]?.classList.remove('active');
    active = (active + step + rows.length) % rows.length;
    rows[active].classList.add('active');
    rows[active].scrollIntoView({ block: 'nearest' });
}

// The longest code wins, the last of a shared one like Web A (+1 is USA), but a
// country already picked with that code stays.
function countryOfNumber(value) {
    let digits = value.replace(/\D/g, '');
    let best;
    for (let item of countries) {
        if (digits.startsWith(item.countryCode) && (!best || item.countryCode.length >= best.countryCode.length)) best = item;
    }
    if (best && country && country.countryCode === best.countryCode) return country;
    return best;
}

function initCountries() {
    let input = $('country');
    input.addEventListener('focus', () => { openCountries(); input.select(); });
    input.addEventListener('click', openCountries);
    input.addEventListener('blur', closeCountries);
    input.addEventListener('input', () => {
        filter = input.value;
        openCountries();
        renderCountries();
    });
    input.addEventListener('keydown', event => {
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault();
            openCountries();
            moveActive(event.key === 'ArrowDown' ? 1 : -1);
        } else if (event.key === 'Enter') {
            event.preventDefault();
            let rows = [...$('country-menu').querySelectorAll('button')];
            rows[Math.max(active, 0)]?.dispatchEvent(new MouseEvent('mousedown', { cancelable: true }));
        } else if (event.key === 'Escape') {
            input.blur();
        } else if (event.key === 'Backspace' && country && filter === undefined) {
            // the whole name is selected; the first backspace starts a new search
            input.value = '';
            filter = '';
            renderCountries();
            event.preventDefault();
        }
    });
    $('phone-number').addEventListener('input', event => {
        let value = event.target.value;
        // a number with no code yet keeps the picked country: it gets added on submit
        if (country && !value.trim().startsWith('+')) return;
        country = countryOfNumber(value);
        showCountry();
    });
}

async function loadCountries() {
    initCountries();
    $('country-box').classList.add('loading');
    try {
        let data = await api('/countries');
        countries = data.phoneCodes.sort((a, b) => a.name.localeCompare(b.name));
    } catch {}
    $('country-box').classList.remove('loading');
    // someone was faster than the list: "not found" would have been a lie
    if (document.activeElement === $('country')) openCountries();
}

async function branding() {
    try {
        let data = await api('/branding');
        document.title = 'Sign in · ' + data.name;
        $('logo').src = data.logoUrl;
        siteName = data.name;
        generated = data.generatedNumbers === true;
        if (!$('phone').hidden) {
            $('lead').textContent = lead('phone');
            numberField('phone');
            syncFields();
        }
        if (!$('phone').hidden && intent === 'login') $('title').textContent = siteName;
        if (!$('username').hidden) $('title').textContent = siteName;
    } catch {}
}

$('phone').addEventListener('submit', event => {
    event.preventDefault();
    run($('phone'), async function () {
        let phone = skipsNumber() ? '' : phoneNumber();
        if (!skipsNumber() && (!/^\+[1-9][0-9]*$/.test(phone) || intent === 'register' && (phone.length < 5 || phone.length > 17))) throw new Error(intent === 'register' ? 'Enter a phone number with 4–16 digits including the country code.' : 'Enter your phone number, including its country code.');
        await api('/auth/start', { mode: 'phone' });
        await api('/auth/intent', { intent });
        let result = await api('/auth/code', { phone });
        madeNumber = result.phone || '';
        show(result.state === 'registration' ? 'register' : 'password');
    });
});

$('password').addEventListener('submit', event => {
    event.preventDefault();
    run($('password'), async function () {
        let result = await api('/auth/password', { password: $('password-value').value });
        if (result.state === 'ready') done();
    });
});

$('register').addEventListener('submit', event => {
    event.preventDefault();
    run($('register'), async function () {
        let result = await api('/auth/register', { firstName: $('first-name').value, lastName: $('last-name').value, username: $('new-username').value, password: $('new-password').value });
        if (result.state === 'ready') done();
    });
});

$('username').addEventListener('submit', event => {
    event.preventDefault();
    run($('username'), async function () {
        await api('/auth/start', { mode: 'phone' });
        let result = await api('/auth/username', { username: $('username-value').value, password: $('username-password').value });
        if (result.state === 'ready') done();
    });
});

document.addEventListener('input', () => { clearErrors(); syncFields(); });
for (let input of document.querySelectorAll('input[type=password]')) {
    let toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.tabIndex = -1;
    toggle.className = 'toggle-password';
    toggle.setAttribute('aria-label', 'Show or hide the password');
    toggle.innerHTML = eye(eyeCrossed);
    toggle.addEventListener('click', () => {
        let show = input.type === 'password';
        input.type = show ? 'text' : 'password';
        toggle.innerHTML = eye(show ? eyeOpen : eyeCrossed);
    });
    input.after(toggle);
}
$('switch-intent').addEventListener('click', () => setIntent(intent === 'register' ? 'login' : 'register'));
for (let back of document.querySelectorAll('[data-back]')) back.addEventListener('click', () => show('phone'));
$('use-username').addEventListener('click', () => show('username'));
// Add Account: the other accounts are still there to go back to
$('cancel').hidden = slot === 1;
// the home page's "create account"
if (query.get('intent') === 'register') setIntent('register');

branding();
loadCountries();
// already signed in in this slot: nothing to do here
api('/me').then(() => location.replace(next)).catch(() => show('phone'));
