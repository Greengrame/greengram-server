import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { createKumoToastManager } from '@cloudflare/kumo';
import { api } from './api.js';

export const toasts = createKumoToastManager();

export function toast(title, variant = 'default') {
    toasts.add({ title, variant });
}

export function toastError(error) {
    toast(error?.message || String(error), 'error');
}

const AdminContext = createContext(null);

export function useAdmin() {
    return useContext(AdminContext);
}

export function AdminProvider({ children, fallback }) {
    let [state, setState] = useState({ user: null, data: null, branding: null, error: null, updatedAt: 0, version: 0 });
    let refresh = useCallback(async function () {
        let [data, branding] = await Promise.all([api('/admin'), api('/branding')]);
        setState(current => ({ ...current, data, branding, updatedAt: Date.now(), version: current.version + 1 }));
        return data;
    }, []);
    useEffect(() => {
        Promise.all([api('/me'), api('/admin'), api('/branding')])
            .then(([user, data, branding]) => setState(current => ({ ...current, user, data, branding, updatedAt: Date.now() })))
            .catch(error => setState(current => ({ ...current, error })));
    }, []);
    useEffect(() => {
        if (!state.data) return;
        document.title = state.data.settings.siteName + ' · Administration';
        document.querySelector('#admin-favicon')?.setAttribute('href', state.branding?.logoUrl || '/branding/logo');
    }, [state.data, state.branding]);
    if (!state.data) return fallback(state.error);
    return <AdminContext.Provider value={{ ...state, refresh, setBranding: branding => setState(current => ({ ...current, branding })) }}>{children}</AdminContext.Provider>;
}

export function logoUrl(branding) {
    return branding?.logoUrl || '/branding/logo';
}

// fetches a side endpoint for one view; refetches when the admin data refreshes
export function useLive(path, { interval = 0 } = {}) {
    let { version } = useAdmin();
    let [state, setState] = useState({ data: null, error: null, loading: Boolean(path) });
    let current = useRef(path);
    let load = useCallback(async function () {
        if (!path) return;
        current.current = path;
        setState(previous => ({ ...previous, loading: true }));
        try {
            let data = await api(path);
            if (current.current === path) setState({ data, error: null, loading: false });
        } catch (error) {
            if (current.current === path) setState(previous => ({ ...previous, error, loading: false }));
        }
    }, [path]);
    // keeps the previous data on screen while a new period or filter loads
    useEffect(() => { load(); }, [load, version]);
    useEffect(() => {
        if (!interval) return undefined;
        let timer = setInterval(() => { if (!document.hidden) load(); }, interval);
        return () => clearInterval(timer);
    }, [load, interval]);
    return { ...state, reload: load };
}

export function useHashView(views, fallback) {
    let read = () => {
        let value = location.hash.slice(1);
        return Object.hasOwn(views, value) ? value : fallback;
    };
    let [view, setView] = useState(read);
    useEffect(() => {
        let change = () => setView(read());
        window.addEventListener('hashchange', change);
        return () => window.removeEventListener('hashchange', change);
    }, []);
    return view;
}
