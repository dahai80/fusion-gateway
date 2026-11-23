import { useState, useEffect, useCallback } from "react";
import { useNavigate } from "react-router-dom";
import { Table, Tag, Typography, Alert, Button, Space, Tooltip } from "antd";
import { Edit3, Lock, Unlock } from "lucide-react";
import client from "../../api/client";
import ConfigPageLayout from "../../components/ConfigPageLayout";

// Issue #709: read-only model routing surface. The writable half of the
// allowlist story lives on the Auth page (api_keys[].allowed_models); this
// page gives the operator one place to see supply routes, the cloud
// allowlist, per-key allowlists and the model catalog they validate against.

interface ModelsConfig {
    supply: Record<string, string[]>;
    aliases: Record<string, string>;
    allow_cross_supply: boolean;
    cloud_models: string[];
    keys: { name: string; allowed_models: string[]; allowed_backends: string[] }[];
    catalog: Record<string, string[]>;
    mutation_lock: boolean;
    config_version: number;
}

const { Text } = Typography;

function patternTags(values: string[] | undefined, color: string) {
    if (!values || values.length === 0) {
        return <Text type="secondary">—</Text>;
    }
    return (
        <Space size={4} wrap>
            {values.map((v) => (
                <Tag key={v} color={color}>{v}</Tag>
            ))}
        </Space>
    );
}

export default function ModelsConfig() {
    const [loading, setLoading] = useState(true);
    const [data, setData] = useState<ModelsConfig | null>(null);
    const navigate = useNavigate();

    const fetchCfg = useCallback(async () => {
        setLoading(true);
        try {
            const res = await client.get<ModelsConfig>("/config/models");
            setData(res.data);
        } catch { /* surfaced by the empty state */ }
        setLoading(false);
    }, []);

    useEffect(() => { fetchCfg(); }, [fetchCfg]);

    const supplyRows = Object.entries(data?.supply ?? {}).map(([model, backends]) => ({
        key: model,
        model,
        backends,
    }));

    const keyRows = (data?.keys ?? []).map((k) => ({
        key: k.name,
        ...k,
    }));

    const catalogCount = Object.keys(data?.catalog ?? {}).length;

    return (
        <ConfigPageLayout
            title="Model Routing"
            loading={loading}
            onRefresh={fetchCfg}
        >
            {data && (
                <Space direction="vertical" size={16} style={{ width: "100%" }}>
                    <Alert
                        type={data.mutation_lock ? "warning" : "info"}
                        showIcon
                        icon={data.mutation_lock ? <Lock size={16} /> : <Unlock size={16} />}
                        message={data.mutation_lock
                            ? "Config mutation lock is ON — allowlist edits via the Auth page will be refused until unlocked (#662)."
                            : `Read-only routing view (config version ${data.config_version}, ${catalogCount} catalog models). Edit key allowlists on the Auth page.`}
                        action={
                            <Button
                                size="small"
                                icon={<Edit3 size={14} />}
                                onClick={() => navigate("/admin/config/auth")}
                            >
                                Edit key allowlists
                            </Button>
                        }
                    />

                    <Table
                        size="small"
                        title={() => "Supply routes (models.supply — ordered resolution priority)"}
                        dataSource={supplyRows}
                        pagination={false}
                        columns={[
                            { title: "Model", dataIndex: "model", width: 280 },
                            {
                                title: "Backend priority",
                                dataIndex: "backends",
                                render: (backends: string[]) => patternTags(backends, "geekblue"),
                            },
                        ]}
                    />

                    <Table
                        size="small"
                        title={() => "API key allowlists (edit on the Auth page)"}
                        dataSource={keyRows}
                        pagination={false}
                        columns={[
                            { title: "Key", dataIndex: "name", width: 200 },
                            {
                                title: "Allowed models",
                                dataIndex: "allowed_models",
                                render: (v: string[]) => patternTags(v, "purple"),
                            },
                            {
                                title: "Allowed backends",
                                dataIndex: "allowed_backends",
                                render: (v: string[]) => patternTags(v, "cyan"),
                            },
                        ]}
                    />

                    <Table
                        size="small"
                        title={() => "Cloud-routable allowlist (routing.cloud_models — empty = unrestricted)"}
                        dataSource={[]}
                        pagination={false}
                        columns={[{ title: "Patterns", render: () => patternTags(data.cloud_models, "gold") }]}
                    />

                    {Object.keys(data.aliases ?? {}).length > 0 && (
                        <Table
                            size="small"
                            title={() => "Aliases (models.aliases)"}
                            dataSource={Object.entries(data.aliases).map(([alias, target]) => ({ key: alias, alias, target }))}
                            pagination={false}
                            columns={[
                                { title: "Alias", dataIndex: "alias", width: 280 },
                                { title: "Target", dataIndex: "target" },
                            ]}
                        />
                    )}

                    <Tooltip title="Every model name the config knows about, with the source that mentions it">
                        <Table
                            size="small"
                            title={() => `Model catalog (${catalogCount} models)`}
                            dataSource={Object.entries(data.catalog ?? {}).map(([model, sources]) => ({ key: model, model, sources }))}
                            pagination={{ pageSize: 10, hideOnSinglePage: true }}
                            columns={[
                                { title: "Model", dataIndex: "model", width: 280 },
                                {
                                    title: "Sources",
                                    dataIndex: "sources",
                                    render: (sources: string[]) => patternTags(sources, "default"),
                                },
                            ]}
                        />
                    </Tooltip>
                </Space>
            )}
        </ConfigPageLayout>
    );
}
