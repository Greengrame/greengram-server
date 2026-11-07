// The wallet's own passcode screens, in front of the whole app: this instance
// asks when Wallet opens and again once an unlock runs out (see RequireUnlocked),
// where the real app only asks before some operations.
export function createWalletSecurity(requireModule) {
    let ready;
    let load = () => ready ??= Promise.all([12745, 51050, 14056, 72895, 55070, 16761, 35970, 32168, 55602].map(id => requireModule.e(id)));

    function Enter({ onDone, onExit }) {
        let React = requireModule(57500);
        let { verifyPasscode, passcodeVerificationScreen } = requireModule(127389).ty();
        let client = requireModule(951820).e();
        React.useEffect(() => {
            verifyPasscode(async credentials => {
                let { error } = await requireModule(131875).Ts(client).passcode.verifyPasscode({ body: credentials });
                if (error) throw error;
            }, { forcePasscode: true, cancelCallback: onExit }).then(onDone, () => {});
        }, []);
        return passcodeVerificationScreen || null;
    }

    function Create({ onDone, onExit }) {
        let React = requireModule(57500);
        let { createPasscode, createPasscodeScreen } = requireModule(127389).oF('unskippable_screen');
        React.useEffect(() => { createPasscode(onDone, { cancelCallback: onExit }); }, []);
        return createPasscodeScreen;
    }

    // turning the passcode off in settings has to land here at once
    function Unlocked({ children, onOff }) {
        let React = requireModule(57500);
        let { isPasscodeTurnedOn } = requireModule(127389).Ki();
        React.useEffect(() => { if (isPasscodeTurnedOn === false) onOff(); }, [isPasscodeTurnedOn]);
        return children;
    }

    function Boundary({ children }) {
        let React = requireModule(57500);
        let [status, setStatus] = React.useState();
        let [failed, setFailed] = React.useState(false);
        let alive = React.useRef(true);
        let refresh = async function () {
            try {
                await load();
                let response = await fetch('/api/walt/local/passcode/status');
                let result = await response.json();
                if (!response.ok || typeof result.unlocked !== 'boolean') throw new Error(result.error);
                if (!alive.current) return;
                setStatus(result);
                setFailed(false);
                requireModule(660072).W.ready();
            } catch {
                if (alive.current) setFailed(true);
            }
        };
        // nothing behind this screen to go back to
        let leave = () => requireModule(660072).W.close();
        React.useEffect(() => {
            let timer = setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, 30000);
            alive.current = true;
            refresh();
            return () => { alive.current = false; clearInterval(timer); };
        }, []);
        if (status?.unlocked) return React.createElement(Unlocked, { onOff: refresh }, children);
        if (!status) {
            if (!failed) return null;
            let Button = requireModule(103676).H;
            let { t } = requireModule(344995).B();
            return React.createElement(Button, { stretched: true, onClick: refresh }, t('common.try_again'));
        }
        return React.createElement(status.configured ? Enter : Create, { onDone: refresh, onExit: leave });
    }

    return { Boundary };
}
