import { useState } from "react";
import { Form, Input, Button, Card, message, Typography } from "antd";
import { UserOutlined, LockOutlined } from "@ant-design/icons";
import { useNavigate } from "react-router-dom";
import client from "../api/client";

const { Title } = Typography;

interface LoginForm {
    username: string;
    password: string;
}

export default function Login() {
    const [loading, setLoading] = useState(false);
    const navigate = useNavigate();

    const onFinish = async (values: LoginForm) => {
        setLoading(true);
        try {
            const res = await client.post("/login", values);
            const token = res.data?.data?.token || res.data?.token;
            if (token) {
                document.cookie = `admin_token=${encodeURIComponent(token)}; path=/; max-age=86400; SameSite=Strict`;
                message.success("Login successful");
                navigate("/admin/");
            } else {
                message.error(res.data?.message || "Login failed");
            }
        } catch (err: any) {
            message.error(err.response?.data?.message || "Login failed");
        } finally {
            setLoading(false);
        }
    };

    return (
        <div style={{ display: "flex", justifyContent: "center", alignItems: "center", minHeight: "100vh", background: "#f0f2f5" }}>
            <Card style={{ width: 400 }} bordered={false}>
                <div style={{ textAlign: "center", marginBottom: 24 }}>
                    <Title level={3}>Fusion Gateway</Title>
                    <p style={{ color: "#888" }}>Admin Dashboard</p>
                </div>
                <Form name="login" onFinish={onFinish} size="large">
                    <Form.Item name="username" rules={[{ required: true, message: "Please enter username" }]}>
                        <Input prefix={<UserOutlined />} placeholder="Username" />
                    </Form.Item>
                    <Form.Item name="password" rules={[{ required: true, message: "Please enter password" }]}>
                        <Input.Password prefix={<LockOutlined />} placeholder="Password" />
                    </Form.Item>
                    <Form.Item>
                        <Button type="primary" htmlType="submit" loading={loading} block>
                            Sign In
                        </Button>
                    </Form.Item>
                </Form>
            </Card>
        </div>
    );
}
