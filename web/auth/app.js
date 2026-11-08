// app.js — unified auth portal SPA. Talks to the same-origin gateway
// surface only: /auth/api/identity/* (allowlisted public auth proxy) and
// /auth/api/code (one-time handoff). After a successful sign-in the portal
// redirects to the allowlisted client URL with ?code=... when a redirect
// parameter is present; otherwise it shows the signed-in state.
(function () {
    "use strict";

    var I18N = {
        en: {
            "title": "Sign in to Fusion",
            "tab-login": "Sign in",
            "tab-register": "Create account",
            "tab-tenant": "Register a team",
            "f-username": "Username",
            "f-password": "Password",
            "f-password-confirm": "Confirm password",
            "err-password-mismatch": "Passwords do not match.",
            "f-tenant": "Tenant",
            "f-optional": "(optional)",
            "f-email": "Email",
            "f-team": "Team name",
            "f-token": "Verification token",
            "btn-login": "Sign in",
            "btn-register": "Create account",
            "btn-tenant": "Register team",
            "btn-verify": "Verify email",
            "link-forgot": "Forgot password?",
            "link-back-signin": "Back to sign in",
            "btn-forgot": "Send reset link",
            "btn-reset": "Set new password",
            "hint-forgot": "Enter the email bound to your account. Unregistered addresses never receive mail — the uniform response never reveals whether an account exists.",
            "hint-reset": "Enter the token from the reset email and a new password.",
            "ok-forgot": "If the address belongs to an account, a reset link has been sent. Nothing arrives? The address is not bound to any account, or the mail is in spam.",
            "ok-reset": "Password updated. You can sign in now.",
            "err-reset-mismatch": "Passwords do not match.",
            "hint-verify": "We will send a verification email. Address verification is required before sign-in.",
            "hint-tenant": "The team activates immediately; token usage is capped by the plan quota.",
            "hint-verify-sent": "Verification sent. Enter the token from the email link.",
            "foot": "Protected by the fusion gateway · one-time handoff to your app",
            "ok-register": "Account created. Check your inbox for the verification link, then sign in.",
            "ok-tenant": "Team registered and active. You can sign in now.",
            "ok-verify": "Email verified. You can sign in now.",
            "ok-login": "Signed in.",
            "err-generic": "Request failed. Try again shortly."
        },
        zh: {
            "title": "登录 Fusion",
            "tab-login": "登录",
            "tab-register": "注册账号",
            "tab-tenant": "注册团队",
            "f-username": "用户名",
            "f-password": "密码",
            "f-password-confirm": "确认密码",
            "err-password-mismatch": "两次输入的密码不一致。",
            "f-tenant": "租户",
            "f-optional": "（可选）",
            "f-email": "邮箱",
            "f-team": "团队名称",
            "f-token": "验证令牌",
            "btn-login": "登录",
            "btn-register": "创建账号",
            "btn-tenant": "注册团队",
            "btn-verify": "验证邮箱",
            "link-forgot": "忘记密码？",
            "link-back-signin": "返回登录",
            "btn-forgot": "发送重置链接",
            "btn-reset": "设置新密码",
            "hint-forgot": "请输入注册账号时绑定的邮箱。未注册的地址不会收到任何邮件（响应不区分账号是否存在，防止撞库探测）。",
            "hint-reset": "输入重置邮件中的令牌和新密码。",
            "ok-forgot": "若该邮箱属于某账号，重置链接已发送。没收到？说明该地址未绑定账号，或邮件在垃圾箱里。",
            "ok-reset": "密码已更新，现在可以登录。",
            "err-reset-mismatch": "两次输入的密码不一致。",
            "hint-verify": "我们将发送验证邮件，验证邮箱后才能登录。",
            "hint-tenant": "团队立即激活，令牌消耗受套餐配额限制。",
            "hint-verify-sent": "验证邮件已发送，请输入邮件链接中的令牌。",
            "foot": "由 fusion 网关保护 · 一次性交接给你的应用",
            "ok-register": "账号已创建，请查收验证邮件后再登录。",
            "ok-tenant": "团队已注册并激活，现在可以登录。",
            "ok-verify": "邮箱已验证，现在可以登录。",
            "ok-login": "登录成功。",
            "err-generic": "请求失败，请稍后重试。"
        }
    };

    var lang = localStorage.getItem("fs_auth_lang") ||
        (navigator.language && navigator.language.indexOf("zh") === 0 ? "zh" : "en");

    var params = new URLSearchParams(location.search);
    // redirect = allowlisted client return URL; return = client-side state
    // (e.g. the page that sent the user to the portal).
    var redirectURI = params.get("redirect") || "";
    var returnTo = params.get("return") || "/";

    function t(key) {
        return (I18N[lang] && I18N[lang][key]) || I18N.en[key] || key;
    }

    function applyLang() {
        document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
        document.getElementById("title").textContent = t("title");
        document.getElementById("verify-msg").textContent = t("hint-verify-sent");
        document.querySelectorAll("[data-i18n]").forEach(function (el) {
            el.textContent = t(el.getAttribute("data-i18n"));
        });
        document.getElementById("lang-en").classList.toggle("active", lang === "en");
        document.getElementById("lang-zh").classList.toggle("active", lang === "zh");
        localStorage.setItem("fs_auth_lang", lang);
    }

    function show(el) { el.hidden = false; }
    function hide(el) { el.hidden = true; }

    var msgEl = document.getElementById("message");
    function say(text, kind) {
        msgEl.textContent = text;
        msgEl.className = "msg " + (kind || "err");
        show(msgEl);
    }
    function clearMsg() { hide(msgEl); }

    function busy(form, on) {
        var btn = form.querySelector("button[type=submit]");
        btn.disabled = on;
    }

    function postJSON(url, body) {
        return fetch(url, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body || {})
        }).then(function (res) {
            return res.json().catch(function () { return {}; }).then(function (data) {
                return { ok: res.ok, status: res.status, data: data };
            });
        });
    }

    function errMsg(r) {
        var detail = (r.data && (r.data.detail || r.data.message)) || "";
        if (detail === "tenant_mismatch") {
            return "This account does not belong to the selected tenant. Leave the tenant field empty or use the tenant you registered under.";
        }
        if (detail === "invalid credentials") {
            return "Wrong username or password.";
        }
        return detail || t("err-generic");
    }

    // finishLogin: hand the token pair to the waiting client via a one-time
    // code, or just report success when no redirect is configured.
    function finishLogin(tokens) {
        if (!redirectURI) {
            say(t("ok-login"), "ok");
            return;
        }
        say(t("ok-login") + " …", "ok");
        postJSON("/auth/api/code", {
            access_token: tokens.access_token,
            refresh_token: tokens.refresh_token,
            expires_in: tokens.expires_in || 0,
            redirect_uri: redirectURI,
            username: tokens.username || ""
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            var sep = redirectURI.indexOf("?") >= 0 ? "&" : "?";
            location.href = redirectURI + sep + "code=" + encodeURIComponent(r.data.code);
        }).catch(function () { say(t("err-generic")); });
    }

    function bindTab(name) {
        document.querySelectorAll(".tab").forEach(function (tab) {
            tab.classList.toggle("active", tab.getAttribute("data-tab") === name);
        });
        document.querySelectorAll(".pane").forEach(function (pane) {
            pane.classList.toggle("active", pane.id === "form-" + name);
        });
        clearMsg();
    }
    document.querySelectorAll(".tab").forEach(function (tab) {
        tab.addEventListener("click", function () { bindTab(tab.getAttribute("data-tab")); });
    });
    document.getElementById("lang-en").addEventListener("click", function () { lang = "en"; applyLang(); });
    document.getElementById("lang-zh").addEventListener("click", function () { lang = "zh"; applyLang(); });

    document.getElementById("form-login").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        busy(f, true);
        var payload = {
            username: f.username.value.trim(),
            password: f.password.value
        };
        // Omit tenant_id when empty: identity resolves the tenant from the
        // user's memberships server-side; pinning "default" rejected every
        // user registered under another tenant (401 tenant_mismatch).
        if (f.tenant_id.value.trim()) payload.tenant_id = f.tenant_id.value.trim();
        postJSON("/auth/api/identity/api/v1/auth/login", payload).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            if (r.data.mfa_required) { say("MFA code required is not supported here yet."); return; }
            finishLogin({
                access_token: r.data.access_token,
                refresh_token: r.data.refresh_token,
                expires_in: r.data.expires_in,
                username: f.username.value.trim()
            });
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    document.getElementById("form-register").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        if (f.password.value !== f.password_confirm.value) {
            say(t("err-password-mismatch"));
            return;
        }
        busy(f, true);
        postJSON("/auth/api/identity/api/v1/auth/register", {
            username: f.username.value.trim(),
            email: f.email.value.trim(),
            password: f.password.value
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            say(t("ok-register"), "ok");
            f.reset();
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    document.getElementById("form-tenant").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        if (f.admin_password.value !== f.admin_password_confirm.value) {
            say(t("err-password-mismatch"));
            return;
        }
        busy(f, true);
        postJSON("/auth/api/identity/api/v1/auth/register-tenant", {
            tenant_display_name: f.tenant_display_name.value.trim(),
            admin_username: f.admin_username.value.trim(),
            admin_email: f.admin_email.value.trim(),
            admin_password: f.admin_password.value
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            say(t("ok-tenant"), "ok");
            f.reset();
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    document.getElementById("form-verify").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        busy(f, true);
        postJSON("/auth/api/identity/api/v1/auth/verify-email", {
            token: f.token.value.trim()
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            say(t("ok-verify"), "ok");
            bindTab("login");
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    // #549: password recovery panes. "Forgot password?" is a plain link (no
    // tab — recovery is a rare path, not a first-class mode); the reset pane
    // prefills the token when the landing URL carries ?reset=<token>.
    document.getElementById("forgot-link").addEventListener("click", function () { bindTab("forgot"); });
    document.getElementById("forgot-back").addEventListener("click", function () { bindTab("login"); });

    document.getElementById("form-forgot").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        busy(f, true);
        // Identity returns the same envelope regardless of account existence
        // (no enumeration); surface it verbatim.
        postJSON("/auth/api/identity/api/v1/auth/password/forgot", {
            email: f.email.value.trim()
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            say(t("ok-forgot"), "ok");
            f.reset();
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    document.getElementById("form-reset").addEventListener("submit", function (e) {
        e.preventDefault();
        clearMsg();
        var f = e.target;
        if (f.password.value !== f.password_confirm.value) {
            say(t("err-reset-mismatch"));
            return;
        }
        busy(f, true);
        postJSON("/auth/api/identity/api/v1/auth/password/reset", {
            token: f.token.value.trim(),
            new_password: f.password.value
        }).then(function (r) {
            if (!r.ok) { say(errMsg(r)); return; }
            say(t("ok-reset"), "ok");
            f.reset();
            bindTab("login");
        }).catch(function () { say(t("err-generic")); }).finally(function () { busy(f, false); });
    });

    // Email links land here as /auth/?verify=<token>.
    var verifyToken = params.get("verify");
    if (verifyToken) {
        bindTab("login");
        document.querySelector('[data-tab="register"]').hidden = false;
        var vf = document.getElementById("form-verify");
        vf.classList.add("active");
        vf.token.value = verifyToken;
        document.getElementById("form-login").classList.remove("active");
    }

    // #549: /auth/?reset=<token> prefills the reset pane.
    var resetToken = params.get("reset");
    if (resetToken) {
        bindTab("reset");
        document.getElementById("form-reset").token.value = resetToken;
    }

    applyLang();
})();
