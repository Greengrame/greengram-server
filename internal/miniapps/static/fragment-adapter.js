// Greengram: the Fragment page is per person, so the server renders it for
// the launch (miniapp-runtime.js GreengramMiniappPage) and fragment.js runs
// once its markup and config are in place.
(async function () {
    let content = document.querySelector('#aj_content');
    let page;
    let config;
    let script;
    try { page = await window.GreengramMiniappPage(); }
    catch (error) { window.GreengramMiniappFailed(error); return; }
    content.innerHTML = page.markup;
    if (page.extra) document.body.insertAdjacentHTML('beforeend', page.extra);
    if (page.config.selectedItem) document.documentElement.className = '';
    document.title = page.title || document.title;
    // signed out: a read-only page with a sign-in link, nothing to drive
    if (page.config.public) return;
    config = document.createElement('script');
    config.type = 'application/json';
    config.id = 'fragment-config';
    config.textContent = JSON.stringify(page.config);
    document.body.append(config);
    script = document.createElement('script');
    script.src = '/__greengram/fragment.js';
    document.body.append(script);
})();
