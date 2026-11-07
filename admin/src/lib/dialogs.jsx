import { createContext, useContext, useState } from 'react';

const DialogContext = createContext(null);

// one dialog at a time, like the old <dialog>: opening another replaces it
export function DialogProvider({ registry, children }) {
    let [current, setCurrent] = useState(null);
    let api = { open: (kind, props = {}) => setCurrent({ kind, props, key: Date.now() }), close: () => setCurrent(null) };
    let Component = current ? registry[current.kind] : null;
    return <DialogContext.Provider value={api}>
        {children}
        {Component ? <Component key={current.key} {...current.props} onClose={api.close} /> : null}
    </DialogContext.Provider>;
}

// kinds: member {id}, premium {ids}, stars {recipientId}, receipt {entry}, role {id}
export function useDialogs() {
    return useContext(DialogContext);
}
