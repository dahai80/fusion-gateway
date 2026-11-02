import { useState } from "react";
import { Form, Input, Button, Card, message, Typography, theme } from "antd";
import { User, Lock } from "lucide-react";
import { useNavigate } from "react-router-dom";
import client from "../api/client";
import { useLocale } from "../i18n";

const { Title } = Typography;

interface LoginForm {
    username: string;
    password: string;
}

export default function Login() {
    const [loading, setLoading] = useState(false);
    const navigate = useNavigate();
    const { token: themeToken } = theme.useToken();
    const { t } = useLocale();

    const onFinish = async (values: LoginForm) => {
        setLoading(true);
        try {
            const res = await client.post("/login", values);
            const token = res.data?.data?.token || res.data?.token;
            if (token) {
                localStorage.setItem("admin_logged_in", "true");
                message.success(t("app.loginSuccess"));
                navigate("/admin/");
            } else {
                message.error(res.data?.message || t("app.loginFailed"));
            }
        } catch (err: any) {
            message.error(err.response?.data?.message || t("app.loginFailed"));
        } finally {
            setLoading(false);
        }
    };

    return (
        <div style={{ display: "flex", justifyContent: "center", alignItems: "center", minHeight: "100vh", background: themeToken.colorBgLayout }}>
            <Card style={{ width: 400 }} bordered={false}>
                <div style={{ textAlign: "center", marginBottom: 24 }}>
                    <Title level={3}>Fusion Gateway</Title>
                    <p style={{ color: themeToken.colorTextTertiary }}>{t("app.title")}</p>
                </div>
                <Form name="login" onFinish={onFinish} size="large">
                    <Form.Item name="username" rules={[{ required: true, message: t("app.usernameRequired") }]}>
                        <Input prefix={<User size={16} />} placeholder={t("app.username")} />
                    </Form.Item>
                    <Form.Item name="password" rules={[{ required: true, message: t("app.passwordRequired") }]}>
                        <Input.Password prefix={<Lock size={16} />} placeholder={t("app.password")} />
                    </Form.Item>
                    <Form.Item>
                        <Button type="primary" htmlType="submit" loading={loading} block>
                            {t("app.login")}
                        </Button>
                    </Form.Item>
                </Form>
            </Card>
        </div>
    );
}
