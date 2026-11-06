// #473: single locale source for the admin console chrome. Follows the shell
// schema (fusion-web-shell-menu-taxonomy-1102.md MT-4): 中文正式名 primary,
// English product name secondary. Locale persists in localStorage and flips
// every subscribed surface via a tiny pub-sub (no i18n framework — the
// chrome has ~40 strings; a framework would outweigh them).
import { useEffect, useState } from "react";

export type Locale = "zh" | "en";

type Entry = { zh: string; en: string };

const DICT: Record<string, Entry> = {
    // shell chrome
    "app.title": { zh: "管理控制台", en: "Admin Console" },
    "app.subtitle": { zh: "Fusion Gateway", en: "Fusion Gateway" },
    "app.login": { zh: "登录", en: "Sign In" },
    "app.logout": { zh: "退出登录", en: "Logout" },
    "app.username": { zh: "用户名", en: "Username" },
    "app.password": { zh: "密码", en: "Password" },
    "app.usernameRequired": { zh: "请输入用户名", en: "Please enter username" },
    "app.passwordRequired": { zh: "请输入密码", en: "Please enter password" },
    "app.loginSuccess": { zh: "登录成功", en: "Login successful" },
    "app.loginFailed": { zh: "登录失败", en: "Login failed" },
    "app.localeLabel": { zh: "EN", en: "中文" },
    // menu
    "menu.dashboard": { zh: "仪表盘", en: "Dashboard" },
    "menu.keys": { zh: "API 密钥", en: "API Keys" },
    "menu.providers": { zh: "模型供应商", en: "Model Providers" },
    "menu.channels": { zh: "渠道", en: "Channels" },
    "menu.insights": { zh: "洞察", en: "Insights" },
    "menu.insights.latency": { zh: "延迟分析", en: "Latency" },
    "menu.insights.compare": { zh: "对比", en: "Compare" },
    "menu.insights.explorer": { zh: "日志探索", en: "Explorer" },
    "menu.insights.optimize": { zh: "优化建议", en: "Optimize" },
    "menu.insights.anomalies": { zh: "异常检测", en: "Anomalies" },
    "menu.insights.reports": { zh: "报告", en: "Reports" },
    "menu.insights.governance": { zh: "治理与支出", en: "Governance & Spend" },
    "menu.insights.capacity": { zh: "容量规划", en: "Capacity" },
    "menu.config": { zh: "配置", en: "Configuration" },
    "menu.config.server": { zh: "服务器", en: "Server" },
    "menu.config.auth": { zh: "认证", en: "Auth" },
    "menu.config.rateLimit": { zh: "限流", en: "Rate Limit" },
    "menu.config.retry": { zh: "重试", en: "Retry" },
    "menu.config.cache": { zh: "缓存", en: "Cache" },
    "menu.config.costMarkup": { zh: "成本加价", en: "Cost Markup" },
    "menu.config.pii": { zh: "PII 防护", en: "PII" },
    "menu.config.promptInjection": { zh: "提示注入防护", en: "Prompt Injection" },
    "menu.config.oidc": { zh: "OIDC 认证", en: "OIDC" },
    "menu.config.rbac": { zh: "访问控制", en: "RBAC" },
    "menu.config.negotiation": { zh: "路由协商", en: "Negotiation" },
    "menu.config.cloudRouting": { zh: "云路由", en: "Cloud Routing" },
    "menu.config.hardware": { zh: "硬件感知", en: "Hardware" },
    "menu.config.tokenizer": { zh: "分词器", en: "Tokenizer" },
    "menu.config.observability": { zh: "可观测性", en: "Observability" },
    "menu.config.cors": { zh: "CORS 跨域", en: "CORS" },
    "menu.config.hotReload": { zh: "热更新", en: "Hot Reload" },
    "menu.config.cluster": { zh: "集群", en: "Cluster" },
    "menu.config.realtime": { zh: "实时通道", en: "Realtime" },
    "menu.config.admin": { zh: "管理员", en: "Admin Panel" },
    "menu.config.semanticCache": { zh: "语义缓存", en: "Semantic Cache" },
    "menu.config.batch": { zh: "批处理", en: "Batch" },
    "menu.config.store": { zh: "存储", en: "Store" },
    "menu.config.persistence": { zh: "持久化", en: "Persistence" },
    "menu.config.validation": { zh: "校验", en: "Validation" },
    // config page chrome
    "config.refresh": { zh: "刷新", en: "Refresh" },
    "config.save": { zh: "保存", en: "Save" },
    "config.saved": { zh: "配置已保存", en: "Config saved" },
};

let listeners: (() => void)[] = [];
// Default en: the console content is historically English and the Playwright
// suite asserts English chrome; the header toggle flips to 中文 per session.
let current: Locale = (localStorage.getItem("admin_locale") as Locale) || "en";

export function setLocale(next: Locale) {
    if (next !== current) {
        current = next;
        localStorage.setItem("admin_locale", next);
        listeners.forEach((fn) => fn());
    }
}

export function useLocale() {
    const [, tick] = useState(0);
    useEffect(() => {
        const fn = () => tick((n) => n + 1);
        listeners.push(fn);
        return () => {
            listeners = listeners.filter((x) => x !== fn);
        };
    }, []);
    return {
        locale: current,
        setLocale,
        t: (key: string) => DICT[key]?.[current] ?? key,
    };
}
