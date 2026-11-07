// Two things the server can't do for this page:
//
// A link from another site arrives without the session cookie (it's
// SameSite=Strict), so a signed-in visitor can get this page instead of the
// app. Requests from here carry it: check, and go on to the app.
//
// The part after # (a chat Web A opens) never reaches the server; sign-in
// takes it along so it ends up where the link pointed.

const hash = location.hash.length > 1 ? location.hash : '';
const target = '/' + hash;

if (hash) {
    for (let link of document.querySelectorAll('a[data-next]')) {
        let url = new URL(link.href);
        url.searchParams.set('next', target);
        link.href = url.pathname + url.search;
    }
}

const KEY = 'greengram-landing-redirect';
// at most once in 10 seconds, so a server that keeps sending this page can't loop
if (Date.now() - (Number(sessionStorage.getItem(KEY)) || 0) > 10000) {
    fetch('/api/me', { credentials: 'same-origin' }).then(response => {
        if (!response.ok) return;
        sessionStorage.setItem(KEY, String(Date.now()));
        location.replace(target);
    }, () => {});
}
