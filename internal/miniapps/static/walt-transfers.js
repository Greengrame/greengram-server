import { probeWalletRecipientPicker, requestWalletRecipient } from './wallet-recipient-request.js';

export function installWalletTransfers(requireModule, decorateModule, SecurityBoundary = ({ children }) => children) {
    let selectedRecipient;
    let view;
    let listeners = new Set();
    let quotes = new Map();
    let pendingTransfers = new Map();
    let ready;

    probeWalletRecipientPicker(window);

    function publish(next) {
        view = next;
        listeners.forEach(listener => listener(next));
    }

    function loadComponents() {
        if (!ready) ready = Promise.all([88910, 12745, 11286, 85134, 51050, 14056, 72895, 63691, 55070, 16761, 78239, 55602].map(id => requireModule.e(id)));
        return ready;
    }

    async function chooseRecipient(currency) {
        let response;
        let accounts;
        let userId;
        let result;
        let photoUrl;
        await loadComponents();
        if (!currency) {
            response = await fetch('/api/walt/api/v1/accounts/');
            if (!response.ok) throw new Error('Wallet balances could not be loaded.');
            accounts = (await response.json()).accounts || [];
            currency = accounts.find(asset => !/^0*(?:\.0*)?$/.test(String(asset.available_balance)))?.currency || 'TON';
        }
        selectedRecipient = undefined;
        userId = await requestWalletRecipient(window);
        if (!userId) return;
        response = await fetch('/api/walt/telegram/recipient?user_id=' + encodeURIComponent(userId));
        result = await response.json();
        if (!response.ok || !result.user) {
            // Web A only knows who looks like a person; the server has the last word
            window.Telegram?.WebApp?.showAlert('This account can\'t receive transfers. Choose another person.');
            return;
        }
        photoUrl = result.user.tg_photo_url ? new URL(result.user.tg_photo_url, window.location.origin) : undefined;
        if (photoUrl) photoUrl.searchParams.set('account', String(window.GreengramMiniapp?.slot || 1));
        selectedRecipient = { id: result.user.id, username: result.user.mention, first_name: result.user.tg_full_name, photo_url: photoUrl?.href };
        return currency;
    }

    function TransferConfirmation({ quote, settle }) {
        let React = requireModule(57500);
        let create = React.createElement;
        let Modal = requireModule(263691).Xn;
        let Button = requireModule(103676).H;
        let currency = quote.crypto_currency || quote.currency || quote.asset;
        let symbol = currency === 'TON' ? 'GRAM' : currency;
        let Cell = requireModule(485229).f;
        let recipient = quote.recipient;
        let label = recipient.username ? '@' + recipient.username : recipient.first_name;
        let Avatar = requireModule(978239)._;
        let photo = recipient.photo_url || selectedRecipient?.photo_url;

        return create(Modal, { isOpen: true, className: 'greengram-transfer-confirmation', title: 'Confirm transfer', onClose: () => settle(false) },
            create(Cell, { start: create(Cell.Part, { type: 'avatar' }, create(Avatar, { src: photo || false, size: 40, fallbackSource: recipient.first_name || label, userId: recipient.id })) },
                create(Cell.Text, { title: recipient.first_name || label, description: recipient.first_name ? label : undefined })),
            create(Cell, {}, create(Cell.Text, { title: quote.amount + ' ' + symbol, description: 'Amount' })),
            create(Cell, {}, create(Cell.Text, { title: '0 ' + symbol, description: 'Fee' })),
            create('div', { style: { padding: '16px', paddingBottom: 'calc(16px + var(--tg-safe-area-inset-bottom, 0px))' } },
                create(Button, { stretched: true, onClick: () => settle(true) }, 'Confirm and send')));
    }

    function TransferOverlay() {
        let React = requireModule(57500);
        let [current, setCurrent] = React.useState(view);

        React.useEffect(() => {
            listeners.add(setCurrent);
            setCurrent(view);
            return () => listeners.delete(setCurrent);
        }, []);
        if (!current) return null;
        return React.createElement(TransferConfirmation, { key: current.quote.transfer_request_id, quote: current.quote, settle: current.settle });
    }

    function wrapPage(descriptors, exports) {
        let Original = exports.component;
        descriptors.component = { enumerable: true, value: function LocalWalletTransferPage(properties) {
            let React = requireModule(57500);
            return React.createElement(React.Fragment, {}, React.createElement(SecurityBoundary, {}, React.createElement(Original, properties)), React.createElement(TransferOverlay));
        } };
    }

    return function registerTransferModules() {
        decorateModule(680877, wrapPage);
        decorateModule(283244, wrapPage);
        decorateModule(93691, descriptors => {
            descriptors.q = { enumerable: true, value: function useLocalRecipientPicker() {
                let navigate = requireModule(378351).Z();
                return { sendToTelegramContact: async currency => {
                    let selectedCurrency = await chooseRecipient(currency);
                    if (selectedCurrency) return navigate({ to: requireModule(18194).JVX, search: { assetCurrency: selectedCurrency } });
                } };
            } };
        });
        decorateModule(397400, (descriptors, exports) => {
            let original = exports.C;
            descriptors.C = { enumerable: true, value: () => selectedRecipient ? { receiver: selectedRecipient, chat: undefined, isYourSelf: false, isReceiverValid: true } : original() };
        });
        decorateModule(350588, (descriptors, exports) => {
            let Original = exports.m;
            descriptors.m = { enumerable: true, value: properties => requireModule(57500).createElement(Original, { ...properties, key: String(selectedRecipient?.id || '') }) };
        });
        decorateModule(27613, (descriptors, exports) => {
            let original = exports.d;
            descriptors.d = { enumerable: true, value: () => {
                let native = original();
                return { ...native, verifyPasscode: (action, options = {}) => {
                    let id = options.operation?.payload?.transfer_request_id;
                    let quote = quotes.get(id);
                    if (options.operation?.name !== 'transfers/process-transfer-request') return native.verifyPasscode(action, options);
                    if (!quote || !selectedRecipient || quote.recipient?.id !== selectedRecipient.id) return Promise.reject(new Error('Create a new transfer request before sending.'));
                    return Promise.resolve().then(() => action({}));
                } };
            } };
        });
        decorateModule(939739, (descriptors, exports) => {
            let api = exports.A;
            let createRequest = api.transfers.createTransferRequest;
            let processRequest = api.transfers.processRequest;
            let create;
            let process;
            let transfers;
            create = async function (options) {
                let response;
                if (pendingTransfers.size) return { error: new Error('Confirm or cancel the current transfer first.') };
                if (!selectedRecipient) return { error: new Error('Choose a recipient before sending.') };
                response = await createRequest({ ...options, body: { ...options.body, receiver_tg_id: selectedRecipient.id } });
                if (response.data?.transfer_request_id) quotes.set(response.data.transfer_request_id, response.data);
                return response;
            };
            process = function (options) {
                let id = options.parameters?.path?.transfer_request_id;
                let quote = quotes.get(id);
                let pending;
                let button = window.Telegram?.WebApp?.MainButton;
                let visible = button?.isVisible;
                if (pendingTransfers.has(id)) return pendingTransfers.get(id);
                if (!quote) return Promise.resolve({ error: new Error('Create a new transfer request before sending.') });
                pending = Promise.resolve().then(async () => {
                    let accepted;
                    let response;
                    window.GreengramWalletTransferConfirming = true;
                    button?.hide();
                    try {
                        await loadComponents();
                        accepted = await new Promise(resolve => publish({ type: 'confirmation', quote, settle: value => { publish(undefined); resolve(value); } }));
                        if (!accepted) return { error: new Error('Transfer cancelled.') };
                        response = await processRequest(options);
                        if (!response.error) {
                            quotes.delete(id);
                            requireModule(624661).q.invalidateQueries({ queryKey: ['assets'] }, { cancelRefetch: false });
                            requireModule(624661).q.invalidateQueries({ queryKey: requireModule(526967).f.user.getAccounts.getQueryKey() }, { cancelRefetch: false });
                        }
                        return response;
                    } finally {
                        window.GreengramWalletTransferConfirming = false;
                        if (visible) { button.hide(); button.show(); }
                    }
                }).then(result => { pendingTransfers.delete(id); return result; }, error => { pendingTransfers.delete(id); throw error; });
                pendingTransfers.set(id, pending);
                return pending;
            };
            transfers = new Proxy(api.transfers, { get: (target, key) => key === 'createTransferRequest' ? new Proxy(createRequest, { apply: (target, context, args) => create(...args) }) : key === 'processRequest' ? new Proxy(processRequest, { apply: (target, context, args) => process(...args) }) : target[key] });
            descriptors.A = { enumerable: true, value: new Proxy(api, { get: (target, key) => key === 'transfers' ? transfers : target[key] }) };
        });
    };
}
