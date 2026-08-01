import { ReactNode } from "react";
import { Card, Button, Spin, Space, message } from "antd";
import { SaveOutlined, ReloadOutlined } from "@ant-design/icons";

interface ConfigPageLayoutProps {
    title: string;
    loading: boolean;
    saving: boolean;
    onSave: () => void;
    onRefresh: () => void;
    children: ReactNode;
}

export default function ConfigPageLayout({
    title, loading, saving, onSave, onRefresh, children,
}: ConfigPageLayoutProps) {
    return (
        <Spin spinning={loading}>
            <Card
                title={title}
                extra={
                    <Space>
                        <Button icon={<ReloadOutlined />} onClick={onRefresh} disabled={saving}>
                            Refresh
                        </Button>
                        <Button type="primary" icon={<SaveOutlined />} onClick={onSave} loading={saving}>
                            Save
                        </Button>
                    </Space>
                }
            >
                {children}
            </Card>
        </Spin>
    );
}

export function useConfigNotifier() {
    const [msg, ctx] = message.useMessage();
    return { success: () => msg.success("Config saved"), error: (e: string) => msg.error(e), ctx };
}
