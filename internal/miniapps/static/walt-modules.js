export function installWalletModuleDecorators(requireModule) {
    let factories = requireModule.m;
    let decorate = function (id, transform) {
        let descriptor = Object.getOwnPropertyDescriptor(factories, id);
        let original = factories[id];
        let wrapped;
        if (descriptor?.get?.greengramDecoration) return;
        wrapped = function () { return original; };
        wrapped.greengramDecoration = true;
        Object.defineProperty(factories, id, {
            configurable: true,
            enumerable: true,
            get: wrapped,
            set(factory) {
                let replacement = function (module, exports, require) {
                    let descriptors;
                    factory(module, exports, require);
                    descriptors = Object.getOwnPropertyDescriptors(module.exports);
                    transform(descriptors, module.exports);
                    module.exports = Object.defineProperties({}, descriptors);
                };
                original = replacement;
            }
        });
        if (original) factories[id] = original;
    };
    return decorate;
}

export function fundedExchangeOptions(options) {
    let funded;
    if (options.payCurrency || options.receiveCurrency) return options;
    funded = options.assets?.find(asset => !/^0*(?:\.0*)?$/.test(String(asset.balance)) && (options.availableExchanges?.[asset.currency]?.length || 0) > 0);
    return funded ? { ...options, payCurrency: funded.currency } : options;
}

export function exchangeCurrencyOptions(options, original) {
    let pairs = options.availableExchanges;
    if (!pairs || options.currenciesLocked || options.fundingMode) return original;
    return {
        ...original,
        getPayCurrenciesFor: target => Object.keys(pairs).filter(code => code === target || pairs[code].includes(target)),
        getReceiveCurrenciesFor: source => [source, ...(pairs[source] || [])]
    };
}
