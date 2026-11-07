// The Stars top-up page. It holds a price in one Wallet asset, then hands
// the quote to the app that opened it (payment_form_submit), and the app
// pays with payments.sendPaymentForm. The token in the address is the key.

const token = location.pathname.split('/').filter(Boolean).pop() || '';
const $ = (id) => document.getElementById(id);
const coin = (code) => (code === 'TON' ? 'GRAM' : code);
const num = (n) => Number(n).toLocaleString('en-US');
const starsText = (n) => `${num(n)} ${Number(n) === 1 ? 'Star' : 'Stars'}`;
// coin badge colours, by asset code
const tint = {
  TON: '#0098ea', USDT: '#26a17b', BTC: '#f7931a', XAUT: '#c9a227', ETH: '#627eea', XRP: '#23292f',
  TRX: '#eb0029', BNB: '#e5a50a', SOL: '#9945ff', LTC: '#345d9d', SUI: '#4da2ff', DOGE: '#c2a633',
  DASH: '#008de4', PI: '#6b3fa0', AAVE: '#b6509e',
};

if (location.hash === '#dark' || location.hash === '#light') {
  document.documentElement.dataset.theme = location.hash.slice(1);
}
$('host').textContent = location.host;

async function api(path, body) {
  const init = body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {};
  let res;
  try {
    res = await fetch(`/api/pay/stars/${encodeURIComponent(token)}${path}`, init);
  } catch {
    throw new Error("Can't reach the server. Check the connection and try again.");
  }
  const out = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(out.error || 'Something went wrong. Try again.');
    err.status = res.status;
    throw err;
  }
  return out;
}

function status(text, kind) {
  const el = $('status');
  el.textContent = text;
  el.className = `status${kind ? ` ${kind}` : ''}`;
}

function finish(title, tag, text) {
  $('title').textContent = title;
  $('tag').textContent = tag;
  $('pick').hidden = true;
  status(text, 'ok');
}

// the apps' payment browsers each listen their own way
function submit(quote) {
  const title = `${quote.amount} ${coin(quote.asset)} · Wallet`;
  const data = { credentials: { quote: quote.quoteId }, title };
  if (window.TelegramWebviewProxy && window.TelegramWebviewProxy.postEvent) {
    window.TelegramWebviewProxy.postEvent('payment_form_submit', JSON.stringify(data));
    return true;
  }
  if (window.external && 'notify' in window.external) {
    window.external.notify(JSON.stringify({ eventType: 'payment_form_submit', eventData: data }));
    return true;
  }
  if (window.parent !== window) {
    window.parent.postMessage(JSON.stringify({ eventType: 'payment_form_submit', eventData: data }), location.origin);
    return true;
  }
  return false;
}

let picked = '';

// The Wallet's own logos: each is a chunk of its webpack build holding an
// SVG component. The server hands out the chunk for an asset; it runs here
// against a stand-in for React that builds DOM nodes.
const svgNS = 'http://www.w3.org/2000/svg';
const camel = new Set(['viewBox', 'gradientUnits', 'gradientTransform', 'patternUnits', 'patternContentUnits',
  'patternTransform', 'clipPathUnits', 'maskUnits', 'maskContentUnits', 'preserveAspectRatio', 'stdDeviation']);
let uid = 0;

function attr(k) {
  if (k === 'className') return 'class';
  if (k === 'xlinkHref') return 'href';
  if (k === 'xmlnsXlink' || k === 'xmlSpace') return '';
  return camel.has(k) ? k : k.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);
}

const react = {
  createElement(type, props, ...kids) {
    if (typeof type === 'function') return type({ ...props, children: kids }, null);
    const node = type === 'div' || type === 'span' ? document.createElement(type) : document.createElementNS(svgNS, type);
    for (const [k, v] of Object.entries(props || {})) {
      if (v == null || k === 'ref' || k === 'children' || k === 'xmlns') continue;
      if (k === 'style' && typeof v === 'object') {
        for (const [sk, sv] of Object.entries(v)) node.style.setProperty(attr(sk), String(sv));
        continue;
      }
      const name = attr(k);
      if (name) node.setAttribute(name, String(v));
    }
    for (const kid of kids.flat()) {
      if (kid != null && kid !== false) node.append(kid instanceof Node ? kid : String(kid));
    }
    return node;
  },
  forwardRef: (render) => (props) => render(props, null),
  memo: (c) => c,
  useId: () => `pay${++uid}`,
};

function webpackRequire() { return react; }
webpackRequire.d = (target, defs) => {
  if (Array.isArray(defs)) {
    for (let i = 0; i < defs.length;) {
      const name = defs[i++]; const get = defs[i++];
      target[name] = get === 0 ? defs[i++] : get();
    }
    return;
  }
  for (const name of Object.keys(defs)) target[name] = defs[name]();
};

async function logo(code) {
  const mod = await import(`/__miniapps/walt/coin/${encodeURIComponent(code)}`);
  const exports = {};
  for (const factory of Object.values(mod.__webpack_esm_modules__ || {})) {
    factory({ exports }, exports, webpackRequire);
  }
  if (typeof exports.ReactComponent !== 'function') throw new Error('no logo');
  const node = exports.ReactComponent({ width: 40, height: 40 });
  node.setAttribute('width', '40');
  node.setAttribute('height', '40');
  return node;
}

// the letter stays when the wallet isn't installed or has no logo for it
function putLogo(badge, code) {
  logo(code).then((node) => {
    badge.textContent = '';
    badge.classList.add('art');
    badge.append(node);
  }, () => {});
}

function el(cls, text) {
  const span = document.createElement('span');
  span.className = cls;
  if (text) span.textContent = text;
  return span;
}

function render(page) {
  $('title').textContent = starsText(page.stars);
  $('tag').textContent = `Paid from your wallet at ${num(page.starsPerUSD)} Stars per USD`;
  $('sum-stars').textContent = starsText(page.stars);
  // what can pay goes first, then what has a balance; gram is always there
  const list = page.assets
    .filter((a) => a.asset === 'TON' || Number(a.balance) > 0)
    .sort((a, b) => (b.enough - a.enough) || ((b.asset === 'TON') - (a.asset === 'TON')));
  const box = $('assets');
  box.textContent = '';
  for (const a of list) {
    const row = document.createElement('button');
    row.type = 'button';
    row.className = 'row';
    row.setAttribute('role', 'radio');
    row.dataset.asset = a.asset;
    row.disabled = !a.enough;
    const badge = el('coin', coin(a.asset).slice(0, 1));
    badge.style.setProperty('--c', tint[a.asset] || '');
    badge.setAttribute('aria-hidden', 'true');
    putLogo(badge, a.asset);
    const txt = el('txt');
    const bal = el('b', `${a.amount !== null && !a.enough ? 'Not enough, you have' : 'You have'} `);
    bal.append(el('amt', `${a.balance} ${coin(a.asset)}`));
    txt.append(el('n', a.name), bal);
    row.append(badge, txt, el('p', a.amount === null ? 'No price now' : `${a.amount} ${coin(a.asset)}`), el('radio'));
    row.addEventListener('click', () => pick(a.asset));
    box.append(row);
  }
  const first = list.find((a) => a.enough);
  pick(first ? first.asset : '');
  $('pick').hidden = false;
  if (!first) {
    status('Nothing in your wallet covers this. Top it up in @wallet, then open this again from the app.', 'warn');
  }
}

function pick(code) {
  picked = code;
  for (const row of $('assets').children) {
    row.setAttribute('aria-checked', String(row.dataset.asset === code));
  }
  const row = [...$('assets').children].find((r) => r.dataset.asset === code);
  const price = row ? row.querySelector('.p').textContent : '–';
  $('sum-price').textContent = price;
  $('sum-total').textContent = price;
  $('use').disabled = !code;
  $('use').textContent = code ? `Continue with ${price}` : 'Continue';
}

$('use').addEventListener('click', async () => {
  if (!picked) return;
  const btn = $('use');
  btn.disabled = true;
  status('Holding the price…');
  try {
    const quote = await api('/quote', { asset: picked });
    const until = new Date(quote.expiresAt * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    if (!submit(quote)) {
      status('Open this page from the app to finish paying.', 'err');
      btn.disabled = false;
      return;
    }
    finish(starsText(quote.stars), `${quote.amount} ${coin(quote.asset)} from your wallet`,
      `Price held until ${until}. Back in the app, tap Pay to finish.`);
  } catch (err) {
    status(err.message, 'err');
    btn.disabled = false;
    if (err.status === 409 || err.status === 410) load();
  }
});

async function load() {
  try {
    const page = await api('');
    if (page.paid) {
      finish(starsText(page.stars), 'Paid', 'These Stars are on your balance. You can close this.');
      return;
    }
    render(page);
  } catch (err) {
    $('title').textContent = err.status === 410 ? 'Payment expired' : "Can't open this";
    $('tag').textContent = 'Stars top-up';
    $('pick').hidden = true;
    status(err.message, 'err');
  }
}

load();
