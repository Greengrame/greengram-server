// The DeFi account here is a second local balance, not a chain wallet, so the
// real /scw pages (TON keys, recovery phrases, DEX sockets) can't run. This
// draws the same screen from the wallet's own components with what's true here:
// no address, no recovery phrase, no Transfer (the balance can only move to and
// from the main one, which also has to stay in reach from here).
export function installWalletLocal(requireModule, decorateModule, SecurityBoundary) {
    let api = async (path, body) => {
        let response = await fetch('/api/walt' + path, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : undefined);
        let result = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(result.error || 'Something went wrong. Try again.');
        return result;
    };
    let shown = code => code === 'TON' ? 'GRAM' : code;
    let held = value => !/^0*(?:\.0*)?$/.test(String(value));
    // getters: a page only pulls in what it draws, and these live in chunks
    // that load() brings in first
    let ui = () => ({
        get React() { return requireModule(57500); }, get Cell() { return requireModule(485229).f; }, get Section() { return requireModule(997396).w; }, get Button() { return requireModule(103676).H; },
        get Logo() { return requireModule(213850).Ay; }, get Icon() { return requireModule(133498).d; }, get Modal() { return requireModule(263691).Xn; }, get Page() { return requireModule(679915).Y; }, get Back() { return requireModule(612591).o; },
    });
    let ready;
    // 62086 repeats the app's base rules, and nothing but this file asks for it.
    // Appended last it undid overrides of them on whatever was drawn afterwards,
    // like the send page's Deposit chip, so it goes first instead.
    let append = document.head.appendChild;
    document.head.appendChild = function (node) {
        return node.tagName === 'LINK' && /\/62086\./.test(node.href) ? this.insertBefore(node, this.firstChild) : append.call(this, node);
    };
    let load = () => ready ??= Promise.all([88910, 12745, 11286, 85134, 51050, 14056, 72895, 63691, 55070, 16761, 78239, 55602, 13850, 62086, 27010, 7039, 7770].map(id => requireModule.e(id)));
    let openStars = () => window.Telegram?.WebApp?.openTelegramLink?.('https://t.me/stars');
    let sheetFont = 'var(--default-font, -apple-system, BlinkMacSystemFont, Roboto, "Segoe UI", sans-serif)';
    // sheets mount outside the app's themed root, so the browser's serif default showed through
    document.head.appendChild(Object.assign(document.createElement('style'), { textContent: `body { font-family: ${sheetFont}; }` }));

    function useStars() {
        let [stars, setStars] = requireModule(57500).useState();
        requireModule(57500).useEffect(() => {
            let alive = true;
            api('/local/stars').then(result => { if (alive) setStars(result.balance); }, () => {});
            return () => { alive = false; };
        }, []);
        return stars;
    }

    // the two rows, for both the wallet's settings and the DeFi page
    function Rows({ chevron }) {
        let { React, Cell, Icon } = ui();
        let create = React.createElement;
        let { t } = requireModule(344995).B();
        let stars = useStars();
        let [apps, setApps] = React.useState(false);
        let openApps = () => load().then(() => setApps(true));
        return create(React.Fragment, {},
            create(Cell, { chevron, tappable: true, onClick: openStars, start: create(Cell.Part, { type: 'squareIcon' }, create(Icon, { icon: 'stars' })),
                end: stars === undefined ? null : create(Cell.Text, { align: 'end', titleAppearance: chevron ? 'subtitleText' : 'accentText', title: String(stars) }) },
                create(Cell.Text, { title: t('scw.settings.stars.title') })),
            create(Cell, { chevron, tappable: true, onClick: openApps, start: create(Cell.Part, { type: 'squareIcon' }, create(Icon, { icon: 'apps' })) },
                create(Cell.Text, { title: t('scw.settings.connected_apps.title') })),
            apps ? create(ConnectedApps, { onClose: () => setApps(false) }) : null);
    }
    window.GreengramWalletRows = Rows;

    // the send page's bottom row, from the wallet's own balance cell
    window.GreengramSendBalance = ({ currency, balance, onChoose, onMax, onDeposit }) => {
        let create = requireModule(57500).createElement;
        let languageCode = requireModule(512507).o();
        let theme = requireModule(881626).D();
        let amount = requireModule(180717).F({ amount: balance, currency, languageCode, currencyDisplay: 'code', maximumFractionDigits: requireModule(979980).__[currency] });
        return create(requireModule(205632).U, { withoutSideBorders: true }, create(requireModule(29473).h, { icon: create(requireModule(213850).Ay, { currency, theme }), amount, balance, onChangeCrypto: onChoose, onUseMaxAmount: onMax, onDeposit, hideMaxButton: !onDeposit && !(Number(balance) > 0) }));
    };

    // the home screen's DeFi cell asks the chain wallet's store for an address,
    // which never comes; an account here shows its total instead of "create"
    window.GreengramDefiTile = balance => {
        let React = requireModule(57500);
        let [state, setState] = React.useState();
        React.useEffect(() => {
            let alive = true;
            api('/local/defi').then(result => { if (alive) setState(result); }, () => {});
            return () => { alive = false; };
        }, []);
        if (!state?.created) return balance;
        let total = new Intl.NumberFormat(navigator.language, { style: 'currency', currency: state.fiat_currency }).format(Number(state.total_fiat));
        return React.createElement(requireModule(485229).f.Text, { align: 'end', title: React.createElement(requireModule(658288).vS, {}, total) });
    };

    // nothing here can connect to a wallet: there's no TON Connect behind it
    function ConnectedApps({ onClose }) {
        let { React, Modal } = ui();
        let create = React.createElement;
        let { t } = requireModule(344995).B();
        return create(Modal, { isOpen: true, title: t('scw.settings.connected_apps.title'), onClose },
            create('div', { style: { padding: '8px 16px calc(24px + var(--tg-safe-area-inset-bottom, 0px))', fontFamily: sheetFont, color: 'var(--tg-theme-hint-color)' } }, t('scw.settings.connected_apps.empty')));
    }

    // the tab bar sits over sheets and takes taps meant for their buttons
    let noTabbar = { tabbar: false };

    function MoveSheet({ state, direction, onClose, onDone }) {
        let { React, Cell, Button, Modal, Logo } = ui();
        requireModule(817616).AO(noTabbar);
        let create = React.createElement;
        let [asset, setAsset] = React.useState();
        let [amount, setAmount] = React.useState('');
        let [busy, setBusy] = React.useState(false);
        let [error, setError] = React.useState('');
        let toDefi = direction === 'to_defi';
        let avail = row => toDefi ? row.main_balance : row.balance;
        let options = state.balances.filter(row => held(avail(row)));
        let row = options.find(item => item.asset === asset) || options[0];
        let submit = async () => {
            setBusy(true);
            setError('');
            try {
                let quote = await api('/local/defi/quote', { asset: row.asset, amount: amount.trim(), direction });
                await api('/local/defi/confirm', { id: quote.id });
                onDone();
            } catch (failure) {
                setError(failure.message);
                setBusy(false);
            }
        };
        let field = { width: '100%', fontFamily: sheetFont, fontSize: 16, boxSizing: 'border-box', padding: '12px 16px', border: 0, borderRadius: 12, outline: 'none', color: 'var(--tg-theme-text-color)', background: 'var(--tg-theme-section-bg-color, var(--tg-theme-bg-color))' };
        return create(Modal, { isOpen: true, title: toDefi ? 'Move to DeFi Account' : 'Move to Wallet', onClose },
            !row ? create('div', { style: { padding: 16, color: 'var(--tg-theme-hint-color)' } }, toDefi ? 'You have nothing in your Wallet to move.' : 'Your DeFi Account is empty.') :
                create('div', { style: { padding: '0 16px calc(16px + var(--tg-safe-area-inset-bottom, 0px))', display: 'flex', flexDirection: 'column', gap: 12, fontFamily: sheetFont } },
                    create('div', { style: { display: 'flex', flexWrap: 'wrap', gap: 8 } }, options.map(item => create('button', { key: item.asset, type: 'button', onClick: () => setAsset(item.asset),
                        style: { display: 'flex', alignItems: 'center', gap: 6, padding: '6px 12px 6px 6px', border: 0, borderRadius: 18, fontFamily: sheetFont, fontSize: 15, cursor: 'pointer', color: 'var(--tg-theme-text-color)',
                            background: item.asset === row.asset ? 'var(--tg-theme-secondary-bg-color)' : 'transparent', outline: item.asset === row.asset ? '1px solid var(--tg-theme-button-color)' : 'none' } },
                        create(Logo, { currency: item.asset, size: 24 }), shown(item.asset)))),
                    create('input', { style: field, inputMode: 'decimal', placeholder: 'Amount', value: amount, onChange: event => setAmount(event.target.value) }),
                    create('div', { style: { fontSize: 13, color: error ? 'var(--tg-theme-destructive-text-color)' : 'var(--tg-theme-hint-color)' } }, error || 'Available: ' + avail(row) + ' ' + shown(row.asset)),
                    create(Button, { stretched: true, disabled: busy || !amount.trim(), loading: busy, onClick: submit }, 'Move')));
    }

    function Holding({ row, rate, currency }) {
        let { React, Cell, Logo } = ui();
        let create = React.createElement;
        let { t } = requireModule(344995).B();
        let languageCode = requireModule(512507).o();
        let fiat = rate == null ? null : requireModule(180717).y({ amount: Number(row.balance) * Number(rate), currency, languageCode });
        return create(Cell, { start: create(Cell.Part, { type: 'avatar' }, create(Logo, { currency: row.asset, size: 42 })), end: fiat && create(Cell.Text, { align: 'end', title: fiat }) },
            create(Cell.Text, { title: requireModule(180717).Zt({ currency: row.asset, t }), description: row.balance + ' ' + shown(row.asset) }));
    }

    function DefiPage() {
        let { React, Cell, Section, Button, Back } = ui();
        let create = React.createElement;
        let { t } = requireModule(344995).B();
        let navigate = requireModule(378351).Z();
        let languageCode = requireModule(512507).o();
        let { data: wallet } = requireModule(985331).dU();
        let [state, setState] = React.useState();
        let [error, setError] = React.useState('');
        let [moving, setMoving] = React.useState();
        let router = requireModule(814081).r();
        // it's the first page on a plain open, and the back button would lead nowhere
        let [canGoBack] = React.useState(() => router.history.canGoBack());
        // the account is just a row saying this balance is in use, and opening it is the only thing that needs it
        let refresh = React.useCallback(() => api('/local/defi').then(result => result.created ? result : api('/local/defi/create', {})).then(setState, failure => setError(failure.message)), []);
        React.useEffect(() => { refresh(); }, [refresh]);
        let moved = () => {
            setMoving(undefined);
            refresh();
            let client = requireModule(624661).q;
            client.invalidateQueries({ queryKey: ['assets'] }, { cancelRefetch: false });
            client.invalidateQueries({ queryKey: requireModule(526967).f.user.getAccounts.getQueryKey() }, { cancelRefetch: false });
        };
        if (error) return create(Section, { description: error }, create('div', { style: { padding: 16 } }, create(Button, { stretched: true, onClick: () => { setError(''); refresh(); } }, 'Retry')));
        if (!state) return create(Section, { skeleton: true }, create(Cell.List, {}, create(Cell, {}, create(Cell.Text, { skeleton: true }))));
        let Page = requireModule(634169).Y;
        let Tile = requireModule(185503).K;
        let mainHeld = state.balances.some(row => held(row.main_balance));
        let holdings = state.balances.filter(row => held(row.balance) || row.asset === 'TON' || row.asset === 'USDT');
        let rates = Object.fromEntries((wallet?.assets || []).map(asset => [asset.currency, asset.rate]));
        let hero = create(Page.Hero, { actions: create(requireModule(652478).e, {},
            create(Tile, { appearance: 'overlay', icon: create(requireModule(154526).h), 'data-testid': 'deposit-btn', onClick: () => setMoving('to_defi') }, t('scw.main.deposit_button')),
            create(Tile, { appearance: 'overlay', icon: create(requireModule(440073).h), 'data-testid': 'withdraw-btn', onClick: () => setMoving('to_main') }, t('scw.main.withdraw_button'))) },
            create(requireModule(248145).A, {}, create('div', { style: { paddingBlock: '32px 0' } },
                create(requireModule(524487).T, { amount: state.total_fiat || '0', currency: state.fiat_currency, amountTestId: 'main-balance', title: t('main.defi_account.title'), titleMode: 'hint', appearance: 'overlay' }))));
        return create(Page, { backgroundColor: 'secondary', backgroundAnimation: true, topHeight: 278, top: create(React.Fragment, {}, create(requireModule(533322).b, { type: 'cw' }), hero) },
            canGoBack ? create(Back, {}) : null,
            create(Page.Sections, {},
                create(Section, { separator: true }, create(Cell.List, {},
                    create(Cell, { chevron: true, tappable: true, onClick: () => navigate({ to: '/main' }), start: create(Cell.Part, { type: 'avatar' }, create(requireModule(219997).h, { width: 42, height: 42 })),
                        end: create(Cell.Text, { align: 'end', title: requireModule(180717).y({ amount: wallet?.availableBalanceTotalFiatAmount ?? 'NaN', currency: state.fiat_currency, languageCode }) }) },
                        create(Cell.Text, { title: t('tabbar.tab_wallet') })))),
                held(state.total_fiat || '0') ? null : create(Section, { separator: true, titleCondensed: true, title: t('scw.onboarding_checklist.title') }, create(Cell.List, {},
                    create(Cell, { tappable: true, onClick: () => mainHeld ? setMoving('to_defi') : navigate({ to: requireModule(18194).lvR, search: { assetCurrency: 'TON' } }),
                        start: create(Cell.Part, { type: 'avatar' }, create('div', { style: { width: 42, height: 42, borderRadius: '50%', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#fff', background: '#7bc862' } }, create(requireModule(177396).h))) },
                        create(Cell.Text, { title: t('scw.onboarding_checklist.steps.first_ton') })))),
                create(Section, { separator: true, titleCondensed: true, title: t('scw.tokens'), description: 'A second balance, kept apart from your main Wallet. It stays on this server: there is no blockchain address or recovery phrase.' }, create(Cell.List, {},
                    holdings.map(row => create(Holding, { key: row.asset, row, rate: rates[row.asset], currency: state.fiat_currency })))),
                state.history?.length ? create(Section, { title: 'History' }, create(Cell.List, {}, state.history.map(item => create(Cell, { key: item.id, start: create(Cell.Part, { type: 'avatar' }, create(ui().Logo, { currency: item.asset, size: 42 })) },
                    create(Cell.Text, { title: (item.direction === 'to_defi' ? 'From Wallet' : 'To Wallet'), description: item.amount + ' ' + shown(item.asset) }))))) : null),
            moving ? create(MoveSheet, { state, direction: moving, onClose: () => setMoving(undefined), onDone: moved }) : null);
    }

    // the real tab bar's other tabs all lead into the chain wallet, so this one
    // has DeFi and the way back to the main balance
    window.GreengramScwTabs = ({ activeTab, cwTabsData }) => {
        let React = requireModule(57500);
        let create = React.createElement;
        let { t } = requireModule(344995).B();
        let navigate = requireModule(378351).Z();
        let { w: Icons } = requireModule(354849);
        let Tab = requireModule(727868).g;
        let ids = requireModule(512410);
        let tabs = [{ id: ids.S.SCW_MAIN, icon: create(Icons.ScwMain), label: t('tabbar.tab_scw_main') }, { id: ids.t.WALLET, icon: create(Icons.Walt), label: t('tabbar.tab_wallet') }];
        let index = Math.max(0, tabs.findIndex(tab => tab.id === activeTab));
        let press = i => { if (tabs[i]?.id === ids.t.WALLET) navigate({ to: '/main' }); };
        return create(requireModule(322475).f, { activeKind: 'scw', tabs: { cw: cwTabsData, scw: { activeTab, activeIndex: index, tabsLength: tabs.length, tabIds: tabs.map(tab => tab.id), onSnapToNew: press, onSnapToSame: () => press(index),
            children: tabs.map(tab => create(Tab, { key: tab.id, icon: tab.icon, active: activeTab === tab.id, 'data-tab-id': tab.id }, tab.label)) } } });
    };

    function LocalDefi() {
        let React = requireModule(57500);
        let [loaded, setLoaded] = React.useState(false);
        React.useEffect(() => { load().then(() => setLoaded(true)); }, []);
        return loaded ? React.createElement(SecurityBoundary, {}, React.createElement(DefiPage)) : null;
    }

    return function register() {
        // the /scw layout: everything under it is the chain wallet's
        decorateModule(45604, descriptors => { descriptors.component = { enumerable: true, value: LocalDefi }; });
        decorateModule(149853, descriptors => { descriptors.loader = { enumerable: true, value: () => undefined }; });
        decorateModule(689852, descriptors => { descriptors.ScwTabsContent = { enumerable: true, value: window.GreengramScwTabs }; });
    };
}
