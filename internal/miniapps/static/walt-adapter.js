import { createWalletSecurity } from '/__greengram/walt-security.js';
import { installWalletModuleDecorators, fundedExchangeOptions, exchangeCurrencyOptions } from '/__greengram/walt-modules.js';
import { installWalletTransfers } from '/__greengram/walt-transfers.js';
import { installWalletLocal } from '/__greengram/walt-local.js';
const configuration = window.GreengramMiniapp;
const entry = configuration.entries;
const runtimeModule = await import('/__miniapps/walt/' + entry.runtime);
const requireModule = runtimeModule.__webpack_require__;
const registerModules = requireModule.C;
const chunkFilename = requireModule.u;
const cssChunkFilename = requireModule.miniCssF;
const decorateModule = installWalletModuleDecorators(requireModule);
const replaceState = window.history.replaceState.bind(window.history);
const pushState = window.history.pushState.bind(window.history);
let module;
let stopMarketRefresh;
const walletSecurity = createWalletSecurity(requireModule);
const registerTransferModules = installWalletTransfers(requireModule, decorateModule);
// the gate around the whole app covers these pages too
const registerLocalModules = installWalletLocal(requireModule, decorateModule, ({ children }) => children);
window.GreengramWalletGate = walletSecurity.Boundary;

function walletLocation(value) {
    let url = new URL(String(value || '/'), location.origin);
    if (url.origin !== location.origin) throw new Error('External Wallet navigation is unavailable on this instance.');
    if (configuration.slot) url.searchParams.set('account', String(configuration.slot));
    return (url.pathname.startsWith('/wallet') ? url.pathname : '/wallet' + (url.pathname === '/' ? '' : url.pathname)) + url.search + url.hash;
}

async function localWalletAuth() {
    let response = await fetch('/api/walt/session');
    let data = await response.json();
    let photoUrl = data.user_info?.photo_url ? new URL(data.user_info.photo_url, location.origin) : undefined;
    if (!response.ok) throw new Error(data.error || 'Sign in to use Wallet.');
    if (photoUrl) { photoUrl.searchParams.set('account', String(configuration.slot || 1)); data.user_info.photo_url = photoUrl.href; }
    requireModule(660072).W.setAuthData(data);
    requireModule(862153).vd(data);
    requireModule(115097).f.set(data.user_id);
    window.WalletUserId = data.user_id;
    return data;
}

function unknownWalletAmount(value) {
    return value === null || value === undefined || value === 'NaN' || typeof value === 'number' && !Number.isFinite(value) || typeof value?.isFinite === 'function' && !value.isFinite();
}

function preserveUnknownAssets(normalized) {
    let unknown = false;
    let assets;
    if (!normalized?.assets) return normalized;
    assets = normalized.assets.map(asset => {
        let unpriced = asset.rate == null && (unknownWalletAmount(asset.fiatBalance) || Number(asset.fiatBalance) === 0) && asset.balance !== '0' && /^0*(?:\.0*)?$/.test(String(asset.balance)) === false;
        let fiat = unpriced || unknownWalletAmount(asset.fiatBalance);
        let usd = unpriced || unknownWalletAmount(asset.usdBalance);
        if (fiat || usd) unknown = true;
        return { ...asset, fiatBalance: fiat ? 'NaN' : asset.fiatBalance, usdBalance: usd ? 'NaN' : asset.usdBalance };
    });
    return { ...normalized, assets, availableBalanceTotalFiatAmount: unknown || unknownWalletAmount(normalized.availableBalanceTotalFiatAmount) ? 'NaN' : normalized.availableBalanceTotalFiatAmount, availableBalanceTotalUsdAmount: unknown || unknownWalletAmount(normalized.availableBalanceTotalUsdAmount) ? 'NaN' : normalized.availableBalanceTotalUsdAmount };
}

function preserveUnknownBalances(response, original) {
    let normalized = original(response);
    let accounts = response.accounts.filter(item => item.account_type !== 'earn_v2');
    normalized.assets.forEach((asset, index) => {
        let account = accounts[index];
        if (account.available_balance_fiat_amount === null) asset.fiatBalance = 'NaN';
        if (account.available_balance_usd_amount === null) asset.usdBalance = 'NaN';
    });
    return preserveUnknownAssets(normalized);
}

function installWalletMarketRefresh(options) {
    let document = options.document;
    let window = options.window;
    let refresh = function () {
        let keys;
        if (document.visibilityState !== 'visible') return Promise.resolve([]);
        keys = options.queryKeys();
        return Promise.allSettled(keys.map(queryKey => options.client.invalidateQueries({ queryKey, refetchType: 'active' }, { cancelRefetch: false })));
    };
    let timer = options.schedule.call(window, refresh, 60000);
    let stop = function () {
        options.cancel.call(window, timer);
        document.removeEventListener('visibilitychange', refresh);
        window.removeEventListener('focus', refresh);
    };
    document.addEventListener('visibilitychange', refresh);
    window.addEventListener('focus', refresh);
    return stop;
}

function installLocalModules() {
    registerTransferModules();
    registerLocalModules();
    // no biometry here: the server keeps no tokens for it, and the native clients would offer it
    decorateModule(542084, descriptors => { descriptors.X = { enumerable: true, value: () => ({ available: false, inited: true, deviceId: requireModule(704755).zg() }) }; });
    decorateModule(106988, (descriptors, exports) => {
        let original = exports.VC;
        descriptors.VC = { enumerable: true, value: error => original(error) || error?.message === 'CancelledError' };
    });
    decorateModule(29874, (descriptors, exports) => {
        let original = exports.f;
        descriptors.f = { enumerable: true, value: () => {
            let native = original();
            return { ...native, startRegionVerification: async options => {
                let response;
                let result;
                // there are no identity checks here, so the real flow has nothing to ask for
                if (options?.source !== 'send_internal' || options.permissions?.length !== 1 || options.permissions[0] !== 'canWithdrawInner') return true;
                response = await fetch('/api/walt/users/public-api/v2/user/get-kyc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
                result = await response.json();
                if (!response.ok) throw new Error(result.error || 'Wallet transfer access could not be checked.');
                return result.data?.kycStatus?.permissions?.can_withdraw_inner === true;
            } };
        } };
    });
    decorateModule(167870, descriptors => {
        descriptors.J = { enumerable: true, value: () => ({ verifyTradeAccess: async () => {
            let response = await fetch('/api/walt/users/public-api/v2/user/get-kyc', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
            let result = await response.json();
            if (!response.ok) throw new Error(result.error || 'Wallet access could not be checked.');
            return result.data?.kycStatus?.permissions?.can_exchange === true;
        } }) };
    });
    decorateModule(516478, (descriptors, exports) => {
        let original = exports.SZ;
        descriptors.SZ = { enumerable: true, value: options => exchangeCurrencyOptions(options, original(options)) };
    });
    decorateModule(234817, (descriptors, exports) => {
        let original = exports.a;
        descriptors.a = { enumerable: true, value: options => original(fundedExchangeOptions(options)) };
    });
    requireModule.m[589956] = function (module, exports) { exports.m = localWalletAuth; };
    requireModule.m[236832] = function (module, exports) { exports.initTelemetry = () => undefined; exports.m = () => null; };
    requireModule.m[74797] = function (module, exports) { exports.T = ''; };
    requireModule.m[49245] = function (module, exports) { exports.O = () => undefined; };
    decorateModule(25797, (descriptors, exports) => {
        let original = exports.x;
        descriptors.x = { enumerable: true, value: response => preserveUnknownBalances(response, original) };
    });
    decorateModule(985331, (descriptors, exports) => {
        let original = exports.dU;
        descriptors.dU = { enumerable: true, value: (options = {}) => {
            let select = options.select;
            return original({ ...options, select: value => select ? select(preserveUnknownAssets(value)) : preserveUnknownAssets(value) });
        } };
    });
    decorateModule(817202, (descriptors, exports) => {
        let original = exports.J;
        descriptors.J = { enumerable: true, value: response => {
            let normalized = original(response);
            return { ...normalized, overallAvailableBalanceFiatAmount: unknownWalletAmount(normalized.overallAvailableBalanceFiatAmount) ? 'NaN' : normalized.overallAvailableBalanceFiatAmount };
        } };
    });
    decorateModule(607159, (descriptors, exports) => {
        let original = exports.t;
        descriptors.t = { enumerable: true, value: options => unknownWalletAmount(options.amount) ? [{ type: 'integer', value: '—' }] : original(options) };
    });
    decorateModule(180717, (descriptors, exports) => {
        let original = exports.y;
        let compact = exports.hs;
        descriptors.y = { enumerable: true, value: options => unknownWalletAmount(options.amount) ? '—' : original(options) };
        descriptors.hs = { enumerable: true, value: (amount, ...args) => unknownWalletAmount(amount) ? '—' : compact(amount, ...args) };
    });
}

requireModule.u = chunk => chunkFilename(chunk) + '?greengram-adapter=transfer-14';
requireModule.miniCssF = chunk => cssChunkFilename(chunk) + '?greengram-adapter=transfer-14';
requireModule.C = function (modules) {
    registerModules(modules);
    installLocalModules();
};
// in the page's order: core needs the numbered chunks already registered
for (const path of [entry.react, entry.openapi, ...configuration.shared, entry.core, entry.earlyLangpack, entry.vendors, entry.app]) {
    module = await import('/__miniapps/walt/' + path);
    if (module.__webpack_esm_modules__) requireModule.C(module);
}
installLocalModules();
Object.keys(requireModule(979980).$W).filter(key => /(?:ApiHost|apiHost|ProxyHost)$/.test(key) && !['hermesApiHost', 'bazarApiHost'].includes(key)).forEach(key => { requireModule(979980).$W[key] = location.origin + '/api/walt'; });
// Plain opens land on DeFi if the server says there's money on it, else on
// main. A link to somewhere goes there.
async function landingPath(path) {
    if (path !== '/' || new URLSearchParams(configuration.initData).get('start_param')) return path;
    try {
        let response = await fetch('/api/walt/local/defi');
        let { opens_here: opensHere } = await response.json();
        return response.ok && opensHere ? '/scw' : path;
    } catch {
        return path;
    }
}
replaceState(null, '', await landingPath((configuration.originalPath || '/wallet').replace(/^\/wallet(?=\/|$)/, '') || '/'));
window.history.replaceState = (state, unused, url) => replaceState(state, unused, walletLocation(url));
window.history.pushState = (state, unused, url) => pushState(state, unused, walletLocation(url));
await import('/__miniapps/walt/' + entry.init);
await import('/__miniapps/walt/' + entry.main);


stopMarketRefresh = installWalletMarketRefresh({ document, window, client: requireModule(624661).q, queryKeys: () => [['assets'], requireModule(526967).f.user.getAccounts.getQueryKey(), ['coins'], ['assetCharts']], schedule: setInterval, cancel: clearInterval });
window.addEventListener('pagehide', () => stopMarketRefresh(), { once: true });
