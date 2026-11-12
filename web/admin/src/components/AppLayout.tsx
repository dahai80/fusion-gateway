import { useState } from "react";
import { Outlet, useNavigate, useLocation } from "react-router-dom";
import { Layout, Menu, Button, Typography, theme } from "antd";
import {
    LayoutDashboard, KeyRound, Server, ChartLine, ArrowLeftRight, Search,
    Lightbulb, TriangleAlert, FileText, Database, Landmark, Zap, RefreshCw,
    CircleDollarSign, ShieldCheck, Lock, User, Cloud, Cpu, Wrench, Eye,
    Filter, Network, AudioLines, FlaskConical, Inbox, CircleCheck,
    PanelLeftClose, PanelLeftOpen, LogOut, Languages,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import client from "../api/client";
import { useLocale } from "../i18n";

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

// #473: lucide icons at 16px, matching the shell sidebar convention
// (menu entries 32px, hover zinc, active indigo — antd Menu tokens handle the
// colors now that ConfigProvider carries the zinc/indigo scale).
const ic = (Icon: LucideIcon) => (
    <Icon size={16} strokeWidth={1.8} />
);

// Legacy in-memory Logs/Analytics pages are deprecated — they read only the
// in-memory ring (empty after restart). Their routes redirect into Insights.
// The top-level Dashboard (#421) is the PG-backed persistent landing page.
function useMenuItems() {
    const { t } = useLocale();
    return [
        { key: "/admin/", icon: ic(LayoutDashboard), label: t("menu.dashboard") },
        { key: "/admin/keys", icon: ic(KeyRound), label: t("menu.keys") },
        { key: "/admin/config/providers", icon: ic(Server), label: t("menu.providers") },
        // #652: Channels menu hidden — the page stays routable (direct URL
        // /admin/channels still works) but is not advertised in the sidebar.
        // { key: "/admin/channels", icon: ic(Server), label: t("menu.channels") },
        {
            key: "insights-group",
            icon: ic(ChartLine),
            label: t("menu.insights"),
            children: [
                { key: "/admin/insights/latency", icon: ic(ChartLine), label: t("menu.insights.latency") },
                { key: "/admin/insights/compare", icon: ic(ArrowLeftRight), label: t("menu.insights.compare") },
                { key: "/admin/insights/explorer", icon: ic(Search), label: t("menu.insights.explorer") },
                { key: "/admin/insights/optimize", icon: ic(Lightbulb), label: t("menu.insights.optimize") },
                { key: "/admin/insights/anomalies", icon: ic(TriangleAlert), label: t("menu.insights.anomalies") },
                { key: "/admin/insights/reports", icon: ic(FileText), label: t("menu.insights.reports") },
                { key: "/admin/insights/governance", icon: ic(ShieldCheck), label: t("menu.insights.governance") },
                { key: "/admin/insights/capacity", icon: ic(Database), label: t("menu.insights.capacity") },
            ],
        },
        {
            key: "config-group",
            icon: ic(Wrench),
            label: t("menu.config"),
            children: [
                { key: "/admin/config/server", icon: ic(Landmark), label: t("menu.config.server") },
                { key: "/admin/config/auth", icon: ic(KeyRound), label: t("menu.config.auth") },
                { key: "/admin/config/rate-limit", icon: ic(Zap), label: t("menu.config.rateLimit") },
                { key: "/admin/config/retry", icon: ic(RefreshCw), label: t("menu.config.retry") },
                { key: "/admin/config/cache", icon: ic(Database), label: t("menu.config.cache") },
                { key: "/admin/config/cost-markup", icon: ic(CircleDollarSign), label: t("menu.config.costMarkup") },
                { key: "/admin/config/pii", icon: ic(ShieldCheck), label: t("menu.config.pii") },
                { key: "/admin/config/prompt-injection", icon: ic(TriangleAlert), label: t("menu.config.promptInjection") },
                { key: "/admin/config/oidc", icon: ic(Lock), label: t("menu.config.oidc") },
                { key: "/admin/config/rbac", icon: ic(User), label: t("menu.config.rbac") },
                { key: "/admin/config/negotiation", icon: ic(ArrowLeftRight), label: t("menu.config.negotiation") },
                { key: "/admin/config/cloud-routing", icon: ic(Cloud), label: t("menu.config.cloudRouting") },
                { key: "/admin/config/hardware", icon: ic(Cpu), label: t("menu.config.hardware") },
                { key: "/admin/config/tokenizer", icon: ic(Wrench), label: t("menu.config.tokenizer") },
                { key: "/admin/config/observability", icon: ic(Eye), label: t("menu.config.observability") },
                { key: "/admin/config/cors", icon: ic(Filter), label: t("menu.config.cors") },
                { key: "/admin/config/hot-reload", icon: ic(RefreshCw), label: t("menu.config.hotReload") },
                { key: "/admin/config/cluster", icon: ic(Network), label: t("menu.config.cluster") },
                { key: "/admin/config/realtime", icon: ic(AudioLines), label: t("menu.config.realtime") },
                { key: "/admin/config/admin", icon: ic(User), label: t("menu.config.admin") },
                { key: "/admin/config/semantic-cache", icon: ic(FlaskConical), label: t("menu.config.semanticCache") },
                { key: "/admin/config/batch", icon: ic(Inbox), label: t("menu.config.batch") },
                { key: "/admin/config/store", icon: ic(Database), label: t("menu.config.store") },
                { key: "/admin/config/persistence", icon: ic(Database), label: t("menu.config.persistence") },
                { key: "/admin/config/validation", icon: ic(CircleCheck), label: t("menu.config.validation") },
            ],
        },
    ];
}

export default function AppLayout() {
    const [collapsed, setCollapsed] = useState(false);
    const navigate = useNavigate();
    const location = useLocation();
    const { token: themeToken } = theme.useToken();
    const { t, locale, setLocale } = useLocale();
    const menuItems = useMenuItems();

    const handleLogout = async () => {
        try {
            await client.post("/logout");
        } catch {
            // cookie clear is best-effort; UI proceeds to login regardless
        }
        localStorage.removeItem("admin_logged_in");
        navigate("/admin/login");
    };

    return (
        <Layout style={{ minHeight: "100vh" }}>
            <Sider
                collapsible
                collapsed={collapsed}
                onCollapse={setCollapsed}
                trigger={null}
                style={{ background: themeToken.colorBgContainer }}
            >
                <div style={{ height: 64, display: "flex", alignItems: "center", justifyContent: "center", borderBottom: `1px solid ${themeToken.colorBorderSecondary}` }}>
                    <Text strong style={{ fontSize: collapsed ? 14 : 18, whiteSpace: "nowrap" }}>
                        {collapsed ? "FG" : t("app.title")}
                    </Text>
                </div>
                <Menu
                    mode="inline"
                    selectedKeys={[location.pathname]}
                    items={menuItems}
                    onClick={({ key }) => navigate(key)}
                    style={{ borderRight: "none" }}
                />
            </Sider>
            <Layout>
                <Header style={{ padding: "0 24px", background: themeToken.colorBgContainer, display: "flex", alignItems: "center", justifyContent: "space-between", borderBottom: `1px solid ${themeToken.colorBorderSecondary}` }}>
                    <Button
                        type="text"
                        icon={collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
                        onClick={() => setCollapsed(!collapsed)}
                    />
                    <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                        <Button
                            type="text"
                            icon={<Languages size={16} />}
                            onClick={() => setLocale(locale === "zh" ? "en" : "zh")}
                        >
                            {t("app.localeLabel")}
                        </Button>
                        <Button
                            type="text"
                            icon={<LogOut size={16} />}
                            onClick={handleLogout}
                            danger
                        >
                            {t("app.logout")}
                        </Button>
                    </div>
                </Header>
                <Content style={{ margin: 24, padding: 24, background: themeToken.colorBgContainer, borderRadius: themeToken.borderRadiusLG, minHeight: 280 }}>
                    <Outlet />
                </Content>
            </Layout>
        </Layout>
    );
}
