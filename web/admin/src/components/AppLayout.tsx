import { useState } from "react";
import { Outlet, useNavigate, useLocation } from "react-router-dom";
import { Layout, Menu, Button, Typography, theme } from "antd";
import client from "../api/client";
import {
    DashboardOutlined,
    KeyOutlined,
    CloudServerOutlined,
    FileTextOutlined,
    BarChartOutlined,
    SettingOutlined,
    LogoutOutlined,
    MenuFoldOutlined,
    MenuUnfoldOutlined,
    SafetyCertificateOutlined,
    ApiOutlined,
    ThunderboltOutlined,
    DatabaseOutlined,
    DollarOutlined,
    CloudOutlined,
    ToolOutlined,
    EyeOutlined,
    SwapOutlined,
    ReloadOutlined,
    ClusterOutlined,
    AudioOutlined,
    UserOutlined,
    LockOutlined,
    FilterOutlined,
    WarningOutlined,
    ExperimentOutlined,
    InboxOutlined,
    CheckCircleOutlined,
    BankOutlined,
} from "@ant-design/icons";

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

const menuItems = [
    { key: "/admin/", icon: <DashboardOutlined />, label: "Dashboard" },
    { key: "/admin/keys", icon: <KeyOutlined />, label: "API Keys" },
    { key: "/admin/channels", icon: <CloudServerOutlined />, label: "Channels" },
    { key: "/admin/logs", icon: <FileTextOutlined />, label: "Logs" },
    { key: "/admin/analytics", icon: <BarChartOutlined />, label: "Analytics" },
    {
        key: "config-group",
        icon: <SettingOutlined />,
        label: "Configuration",
        children: [
            { key: "/admin/config/server", icon: <BankOutlined />, label: "Server" },
            { key: "/admin/config/auth", icon: <KeyOutlined />, label: "Auth" },
            { key: "/admin/config/rate-limit", icon: <ThunderboltOutlined />, label: "Rate Limit" },
            { key: "/admin/config/retry", icon: <ReloadOutlined />, label: "Retry" },
            { key: "/admin/config/cache", icon: <DatabaseOutlined />, label: "Cache" },
            { key: "/admin/config/cost", icon: <DollarOutlined />, label: "Cost" },
            { key: "/admin/config/cost-markup", icon: <DollarOutlined />, label: "Cost Markup" },
            { key: "/admin/config/pii", icon: <SafetyCertificateOutlined />, label: "PII" },
            { key: "/admin/config/prompt-injection", icon: <WarningOutlined />, label: "Prompt Injection" },
            { key: "/admin/config/oidc", icon: <LockOutlined />, label: "OIDC" },
            { key: "/admin/config/rbac", icon: <UserOutlined />, label: "RBAC" },
            { key: "/admin/config/negotiation", icon: <SwapOutlined />, label: "Negotiation" },
            { key: "/admin/config/cloud-routing", icon: <CloudOutlined />, label: "Cloud Routing" },
            { key: "/admin/config/hardware", icon: <ApiOutlined />, label: "Hardware" },
            { key: "/admin/config/tokenizer", icon: <ToolOutlined />, label: "Tokenizer" },
            { key: "/admin/config/observability", icon: <EyeOutlined />, label: "Observability" },
            { key: "/admin/config/cors", icon: <FilterOutlined />, label: "CORS" },
            { key: "/admin/config/hot-reload", icon: <ReloadOutlined />, label: "Hot Reload" },
            { key: "/admin/config/cluster", icon: <ClusterOutlined />, label: "Cluster" },
            { key: "/admin/config/realtime", icon: <AudioOutlined />, label: "Realtime" },
            { key: "/admin/config/admin", icon: <UserOutlined />, label: "Admin Panel" },
            { key: "/admin/config/semantic-cache", icon: <ExperimentOutlined />, label: "Semantic Cache" },
            { key: "/admin/config/batch", icon: <InboxOutlined />, label: "Batch" },
            { key: "/admin/config/store", icon: <DatabaseOutlined />, label: "Store" },
            { key: "/admin/config/validation", icon: <CheckCircleOutlined />, label: "Validation" },
            { key: "/admin/settings", icon: <SettingOutlined />, label: "Routing & Backends" },
        ],
    },
];

export default function AppLayout() {
    const [collapsed, setCollapsed] = useState(false);
    const navigate = useNavigate();
    const location = useLocation();
    const { token: themeToken } = theme.useToken();

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
                        {collapsed ? "FG" : "Fusion Gateway"}
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
                        icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
                        onClick={() => setCollapsed(!collapsed)}
                    />
                    <Button
                        type="text"
                        icon={<LogoutOutlined />}
                        onClick={handleLogout}
                        danger
                    >
                        Logout
                    </Button>
                </Header>
                <Content style={{ margin: 24, padding: 24, background: themeToken.colorBgContainer, borderRadius: themeToken.borderRadiusLG, minHeight: 280 }}>
                    <Outlet />
                </Content>
            </Layout>
        </Layout>
    );
}
