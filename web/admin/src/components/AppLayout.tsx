import { useState } from "react";
import { Outlet, useNavigate, useLocation } from "react-router-dom";
import { Layout, Menu, Button, Typography, theme } from "antd";
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
} from "@ant-design/icons";

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

const menuItems = [
    { key: "/admin/", icon: <DashboardOutlined />, label: "Dashboard" },
    { key: "/admin/keys", icon: <KeyOutlined />, label: "API Keys" },
    { key: "/admin/channels", icon: <CloudServerOutlined />, label: "Channels" },
    { key: "/admin/logs", icon: <FileTextOutlined />, label: "Logs" },
    { key: "/admin/analytics", icon: <BarChartOutlined />, label: "Analytics" },
    { key: "/admin/settings", icon: <SettingOutlined />, label: "Settings" },
];

export default function AppLayout() {
    const [collapsed, setCollapsed] = useState(false);
    const navigate = useNavigate();
    const location = useLocation();
    const { token: themeToken } = theme.useToken();

    const handleLogout = () => {
        document.cookie = "admin_token=; path=/; max-age=0";
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
