import { createRoot } from 'react-dom/client';
import { useEffect, useState } from 'react';
import { Breadcrumbs, Button, Sidebar, Toasty, Tooltip, TooltipProvider } from '@cloudflare/kumo';
import { ArrowsClockwiseIcon, ChatCircleIcon, MoonIcon, SignOutIcon, SunIcon } from '@phosphor-icons/react';
import { AdminProvider, logoUrl, toastError, toasts, useAdmin, useHashView } from './lib/store.jsx';
import { api } from './lib/api.js';
import { number } from './lib/format.js';
import { Avatar, Loading } from './components/ui.jsx';
import { groups, views } from './views/index.js';
import { ThemeContext } from './lib/theme.js';
import { DialogProvider } from './lib/dialogs.jsx';
import { dialogs } from './dialogs/index.js';
import './app.css';

function storedTheme() {
    try { return localStorage.getItem('greengram-admin-theme') === 'light' ? 'light' : 'dark'; } catch { return 'dark'; }
}

function Shell({ theme, setTheme }) {
    let admin = useAdmin();
    let view = useHashView(views, 'overview');
    let current = views[view];
    let View = current.component;
    let [refreshing, setRefreshing] = useState(false);
    let counts = { members: admin.data.users.length, bots: (admin.data.bots || []).length, reports: admin.data.openReports || undefined };
    useEffect(() => { document.querySelector('#content')?.scrollTo({ top: 0 }); }, [view]);
    async function refresh() {
        setRefreshing(true);
        try { await admin.refresh(); } catch (error) { toastError(error); } finally { setRefreshing(false); }
    }
    async function logout() {
        try { await api('/logout', 'POST', {}); location.replace('/login'); } catch (error) { toastError(error); }
    }
    return <Sidebar.Provider defaultOpen className="h-dvh">
        <Sidebar>
            <Sidebar.Header className="gap-3 px-4">
                <img src={logoUrl(admin.branding)} alt="" className="size-7 shrink-0 rounded-full object-cover" />
                <span className="min-w-0">
                    <span className="block truncate text-sm font-semibold text-kumo-default">{admin.data.settings.siteName}</span>
                    <span className="block text-xs text-kumo-subtle">Administration</span>
                </span>
            </Sidebar.Header>
            <Sidebar.Content>
                {groups.map(group => <Sidebar.Group key={group.label}>
                    <Sidebar.GroupLabel>{group.label}</Sidebar.GroupLabel>
                    <Sidebar.Menu>
                        {group.views.map(key => <Sidebar.MenuItem key={key}>
                            <Sidebar.MenuButton icon={views[key].icon} href={'#' + key} active={view === key} tooltip={views[key].title} className={view === key ? 'text-kumo-brand [&_svg]:text-kumo-brand' : ''}>
                                {views[key].title}
                                {counts[key] !== undefined ? <Sidebar.MenuBadge>{number(counts[key])}</Sidebar.MenuBadge> : null}
                            </Sidebar.MenuButton>
                        </Sidebar.MenuItem>)}
                    </Sidebar.Menu>
                </Sidebar.Group>)}
            </Sidebar.Content>
            <Sidebar.Footer className="flex items-center gap-2 px-3">
                <Avatar user={admin.data.users.find(person => person.id === admin.user.id) || admin.user} size={28} />
                <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm text-kumo-default">{admin.user.name}</span>
                    <span className="block text-xs text-kumo-subtle">Administrator</span>
                </span>
                <Tooltip content="Open messenger" render={<Button variant="ghost" size="sm" shape="square" icon={ChatCircleIcon} aria-label="Open messenger" onClick={() => location.assign('/')} />} />
                <Tooltip content="Log out" render={<Button variant="ghost" size="sm" shape="square" icon={SignOutIcon} aria-label="Log out" onClick={logout} />} />
            </Sidebar.Footer>
        </Sidebar>
        <div className="flex h-dvh min-w-0 flex-1 flex-col bg-kumo-canvas">
            <header className="flex h-12 shrink-0 items-center gap-2 border-b border-kumo-line bg-kumo-base px-3">
                <Sidebar.Trigger />
                <div className="min-w-0 flex-1">
                    <Breadcrumbs size="sm">
                        <Breadcrumbs.Link href="#overview">{admin.data.settings.siteName}</Breadcrumbs.Link>
                        <Breadcrumbs.Separator />
                        <Breadcrumbs.Current>{current.title}</Breadcrumbs.Current>
                    </Breadcrumbs>
                </div>
                <span className="hidden text-xs text-kumo-subtle tabular-nums sm:inline">Updated {new Date(admin.updatedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
                <Tooltip content="Refresh" render={<Button variant="ghost" size="sm" shape="square" icon={ArrowsClockwiseIcon} aria-label="Refresh" loading={refreshing} onClick={refresh} />} />
                <Tooltip content={theme === 'dark' ? 'Light mode' : 'Dark mode'} render={<Button variant="ghost" size="sm" shape="square" icon={theme === 'dark' ? SunIcon : MoonIcon} aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'} onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} />} />
            </header>
            <main id="content" className="min-h-0 flex-1 overflow-y-auto" tabIndex={-1}>
                <div className="mx-auto flex w-full max-w-7xl flex-col gap-6 px-4 py-6 sm:px-8">
                    <div className="flex flex-col gap-1">
                        <h1 className="text-2xl font-semibold text-kumo-default">{current.title}</h1>
                        <p className="text-sm text-kumo-subtle">{current.description}</p>
                    </div>
                    <View key={view} />
                </div>
            </main>
        </div>
    </Sidebar.Provider>;
}

function App() {
    let [theme, setTheme] = useState(storedTheme);
    useEffect(() => {
        document.documentElement.dataset.mode = theme;
        document.documentElement.style.colorScheme = theme;
        document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#212121' : '#ffffff');
        try { localStorage.setItem('greengram-admin-theme', theme); } catch {}
    }, [theme]);
    return <ThemeContext.Provider value={{ dark: theme === 'dark' }}>
        <TooltipProvider>
            <Toasty toastManager={toasts}>
                <AdminProvider fallback={error => error ? <div className="flex h-dvh flex-col items-center justify-center gap-3 text-kumo-default"><h1 className="text-xl font-semibold">Unable to open administration</h1><p className="text-sm text-kumo-subtle">{error.message}</p><a className="text-sm underline" href="/login?next=admin">Return to sign-in</a></div> : <div className="h-dvh"><Loading label="Opening administration…" /></div>}>
                    <DialogProvider registry={dialogs}><Shell theme={theme} setTheme={setTheme} /></DialogProvider>
                </AdminProvider>
            </Toasty>
        </TooltipProvider>
    </ThemeContext.Provider>;
}

createRoot(document.querySelector('#admin-app')).render(<App />);
