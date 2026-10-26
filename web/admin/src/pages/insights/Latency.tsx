import { useState, useEffect } from "react";
import { Card, Spin, Select, Switch, Typography, Input, Row, Col, Alert, Tooltip } from "antd";
import {
    ComposedChart,
    Line,
    Bar,
    XAxis,
    YAxis,
    CartesianGrid,
    Tooltip as RTooltip,
    Legend,
    ResponsiveContainer,
    ReferenceLine,
} from "recharts";
import client from "../../api/client";
import { isUnavailable, UnavailableAlert, PERIOD_OPTIONS, windowFor } from "./shared";

const { Title } = Typography;

interface LatencyBucket {
    bucket: string;
    requests: number;
    ttft_p50_ms: number;
    ttft_p95_ms: number;
    ttft_p99_ms: number;
    tpot_p50_ms: number;
    tpot_p95_ms: number;
    tpot_p99_ms: number;
    avg_duration_ms: number;
    error_count: number;
}

interface LatencyResponse {
    from: string;
    to: string;
    granularity: string;
    series: LatencyBucket[];
}

interface AnomalyMark {
    id: number;
    ts: string;
    kind: string;
    severity: string;
}

const granularityFor = (hours: number) => (hours <= 24 ? "hour" : "day");

export default function Latency() {
    const [loading, setLoading] = useState(true);
    const [unavailable, setUnavailable] = useState(false);
    const [data, setData] = useState<LatencyResponse | null>(null);
    const [anomalies, setAnomalies] = useState<AnomalyMark[]>([]);
    const [showAnomalies, setShowAnomalies] = useState(true);
    const [period, setPeriod] = useState(24);
    const [backend, setBackend] = useState("");
    const [model, setModel] = useState("");
    const [projectID, setProjectID] = useState("");

    useEffect(() => {
        let cancelled = false;
        setLoading(true);
        const params: Record<string, unknown> = { ...windowFor(period), granularity: granularityFor(period) };
        if (backend) params.backend = backend;
        if (model) params.model = model;
        if (projectID) params.project_id = projectID;
        client
            .get("/insights/latency", { params })
            .then((res) => {
                if (!cancelled) {
                    setData(res.data);
                    setUnavailable(false);
                }
            })
            .catch((err) => {
                if (!cancelled) setUnavailable(isUnavailable(err));
            })
            .finally(() => {
                if (!cancelled) setLoading(false);
            });
        // Anomaly timeline overlay (plan §15.2): fetched alongside the latency
        // series so detected anomalies line up against the latency curve.
        client
            .get("/anomalies", { params: { limit: 200 } })
            .then((res) => {
                if (!cancelled) {
                    setAnomalies((res.data.anomalies || []).map((a: { id: number; detected_at: string; kind: string; severity: string }) => ({
                        id: a.id,
                        ts: a.detected_at,
                        kind: a.kind,
                        severity: a.severity,
                    })));
                }
            })
            .catch(() => {
                // overlay is best-effort — a failed fetch just drops the marks
            });
        return () => {
            cancelled = true;
        };
    }, [period, backend, model, projectID]);

    if (unavailable) return <UnavailableAlert />;

    const series = data?.series || [];

    // Match each anomaly to the latency bucket that contains it so the
    // ReferenceLine x value is a bucket string the chart actually has.
    const stepMS = (data?.granularity === "day" ? 24 : 1) * 3600 * 1000;
    const anomalyLines = showAnomalies
        ? anomalies
              .map((a) => {
                  const at = new Date(a.ts).getTime();
                  const hit = series.find((b) => {
                      const start = new Date(b.bucket).getTime();
                      return at >= start && at < start + stepMS;
                  });
                  return hit ? { x: hit.bucket, kind: a.kind, severity: a.severity, id: a.id } : null;
              })
              .filter(Boolean) as { x: string; kind: string; severity: string; id: number }[]
        : [];

    return (
        <div>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 16, gap: 8 }}>
                <Title level={4} style={{ margin: 0 }}>Latency (TTFT / TPOT)</Title>
                <Row gutter={8}>
                    <Col>
                        <Input
                            placeholder="backend"
                            value={backend}
                            onChange={(e) => setBackend(e.target.value)}
                            style={{ width: 120 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Input
                            placeholder="model"
                            value={model}
                            onChange={(e) => setModel(e.target.value)}
                            style={{ width: 140 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Input
                            placeholder="project"
                            value={projectID}
                            onChange={(e) => setProjectID(e.target.value)}
                            style={{ width: 120 }}
                            allowClear
                        />
                    </Col>
                    <Col>
                        <Tooltip title="Overlay detected anomalies on the latency timeline (plan §15.2)">
                            <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                                anomalies <Switch size="small" checked={showAnomalies} onChange={setShowAnomalies} />
                            </span>
                        </Tooltip>
                    </Col>
                    <Col>
                        <Select
                            value={period}
                            onChange={setPeriod}
                            style={{ width: 120 }}
                            options={PERIOD_OPTIONS}
                        />
                    </Col>
                </Row>
            </div>
            {loading ? (
                <div style={{ textAlign: "center", padding: 80 }}>
                    <Spin size="large" />
                </div>
            ) : series.length === 0 ? (
                <Alert type="info" showIcon message="No generation traffic in this window (only requests with a measured TTFT are aggregated)." />
            ) : (
                <>
                    <Card title="Time to First Token (ms)" style={{ marginBottom: 24 }}>
                        <ResponsiveContainer width="100%" height={320}>
                            <ComposedChart data={series}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="bucket" />
                                <YAxis yAxisId="left" unit="ms" />
                                <YAxis yAxisId="right" orientation="right" />
                                <RTooltip />
                                <Legend />
                                <Bar yAxisId="right" dataKey="requests" name="requests" fill="#1677ff" fillOpacity={0.15} />
                                <Line yAxisId="left" type="monotone" dataKey="ttft_p50_ms" name="TTFT p50" stroke="#1677ff" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="ttft_p95_ms" name="TTFT p95" stroke="#ff4d4f" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="ttft_p99_ms" name="TTFT p99" stroke="#722ed1" strokeWidth={1.5} strokeDasharray="6 3" dot={false} />
                                {anomalyLines.map((a) => (
                                    <ReferenceLine
                                        key={`ttft-${a.id}`}
                                        x={a.x}
                                        stroke={a.severity === "high" ? "#ff4d4f" : "#faad14"}
                                        strokeDasharray="2 4"
                                        label={{ value: a.kind, position: "insideTopLeft", fontSize: 10, fill: a.severity === "high" ? "#ff4d4f" : "#faad14" }}
                                    />
                                ))}
                            </ComposedChart>
                        </ResponsiveContainer>
                    </Card>
                    <Card title="Time Per Output Token (ms/token)" style={{ marginBottom: 24 }}>
                        <ResponsiveContainer width="100%" height={320}>
                            <ComposedChart data={series}>
                                <CartesianGrid strokeDasharray="3 3" />
                                <XAxis dataKey="bucket" />
                                <YAxis yAxisId="left" unit="ms" />
                                <YAxis yAxisId="right" orientation="right" />
                                <RTooltip />
                                <Legend />
                                <Bar yAxisId="right" dataKey="error_count" name="errors" fill="#faad14" fillOpacity={0.2} />
                                <Line yAxisId="left" type="monotone" dataKey="tpot_p50_ms" name="TPOT p50" stroke="#52c41a" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="tpot_p95_ms" name="TPOT p95" stroke="#faad14" strokeWidth={2} dot={false} />
                                <Line yAxisId="left" type="monotone" dataKey="tpot_p99_ms" name="TPOT p99" stroke="#722ed1" strokeWidth={1.5} strokeDasharray="6 3" dot={false} />
                                {anomalyLines.map((a) => (
                                    <ReferenceLine
                                        key={`tpot-${a.id}`}
                                        x={a.x}
                                        stroke={a.severity === "high" ? "#ff4d4f" : "#faad14"}
                                        strokeDasharray="2 4"
                                    />
                                ))}
                            </ComposedChart>
                        </ResponsiveContainer>
                    </Card>
                </>
            )}
        </div>
    );
}
