import { ReactNode } from "react";
import { Card, Button, Spin, Space, message } from "antd";
import { Save, RefreshCw } from "lucide-react";
import { useLocale } from "../i18n";

interface ConfigPageLayoutProps {
    title: string;
    loading: boolean;
    saving?: boolean;
    // Omit onSave on a read-only surface (#709): the Save button is hidden.
    onSave?: () => void;
    onRefresh: () => void;
    children: ReactNode;
}

export default function ConfigPageLayout({
    title, loading, saving = false, onSave, onRefresh, children,
}: ConfigPageLayoutProps) {
    const { t } = useLocale();
    return (
        <Spin spinning={loading}>
            <Card
                title={title}
                extra={
                    <Space>
                        <Button icon={<RefreshCw size={16} />} onClick={onRefresh} disabled={saving}>
                            {t("config.refresh")}
                        </Button>
                        {onSave && (
                            <Button type="primary" icon={<Save size={16} />} onClick={onSave} loading={saving}>
                                {t("config.save")}
                            </Button>
                        )}
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
    const { t } = useLocale();
    return { success: () => msg.success(t("config.saved")), error: (e: string) => msg.error(e), ctx };
}

