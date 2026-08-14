import { useMemo, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import type { BarSeriesOption, LineSeriesOption } from 'echarts/charts';
import type {
  DataZoomComponentOption,
  GridComponentOption,
  LegendComponentOption,
  TooltipComponentOption,
} from 'echarts/components';
import type { ComposeOption } from 'echarts/core';
import { EChartsView } from '@/components/charts/EChartsView';
import { useThemeStore } from '@/stores';
import type {
  DashboardTodayRequestHealthTimeline,
  DashboardTodayRequestHealthTimelinePoint,
  DashboardTokenMixSegment,
  DashboardTrafficPoint,
} from '@/services/api/usageService';
import { getDataPalette, hexToRgba, type DataPalette } from '@/utils/dataPalette';
import { formatCompactNumber } from '@/utils/usage';
import { buildVisibleTrafficTimeline, isCurrentTrafficBucket } from './trafficOverviewChartModel';
import styles from './TrafficOverviewCard.module.scss';

interface TrafficOverviewCardProps {
  timeline: DashboardTrafficPoint[];
  trafficNowMs?: number | null;
  todayRequestHealthTimeline: DashboardTodayRequestHealthTimeline | null;
  tokenMix: DashboardTokenMixSegment[];
  loading: boolean;
}

type TrafficTrendChartOption = ComposeOption<
  | BarSeriesOption
  | DataZoomComponentOption
  | GridComponentOption
  | LegendComponentOption
  | LineSeriesOption
  | TooltipComponentOption
>;

type TokenRankStyle = CSSProperties & Record<'--rank-share' | '--rank-color', number | string>;
type HealthCellStyle = CSSProperties & Record<'--cell-intensity', number>;

const fallbackHealthBucketMs = 10 * 60 * 1000;

const formatHour = (bucketMs: number, locale: string) =>
  new Date(bucketMs).toLocaleTimeString(locale, {
    hour: '2-digit',
    minute: '2-digit',
  });

const tokenLabelMap: Record<string, string> = {
  input: 'dashboard.token_mix_input',
  cached: 'dashboard.token_mix_cached',
  output: 'dashboard.token_mix_output',
  reasoning: 'dashboard.token_mix_reasoning',
  unclassified: 'dashboard.token_mix_unclassified',
};

const buildTokenColorMap = (dataPalette: DataPalette): Record<string, string> => ({
  input: dataPalette.blue,
  cached: dataPalette.amber,
  output: dataPalette.emerald,
  reasoning: dataPalette.violet,
  unclassified: dataPalette.red,
});

const visibleTokenMixKeys = new Set(['input', 'cached', 'output', 'reasoning', 'unclassified']);

const healthToneClassMap: Record<string, string> = {
  future: 'healthFuture',
  empty: 'healthEmpty',
  good: 'healthGood',
  warn: 'healthWarn',
  bad: 'healthBad',
};

const formatPercent = (value: number | undefined, digits = 1) => {
  if (value === undefined || !Number.isFinite(value)) return '-';
  return `${(value * 100).toFixed(digits)}%`;
};

const formatMinuteLabel = (bucketMs: number | undefined, locale: string) => {
  if (!bucketMs) return '--';
  return new Date(bucketMs).toLocaleTimeString(locale, {
    minute: '2-digit',
  });
};

const buildHealthTitle = (
  point: DashboardTodayRequestHealthTimelinePoint,
  locale: string,
  t: (key: string) => string
) => {
  const time = formatHour(point.bucket_ms, locale);
  return `${time} · ${t('dashboard.traffic_calls')}: ${formatCompactNumber(point.calls)} · ${t('dashboard.request_health_success')}: ${formatCompactNumber(point.success)} · ${t('dashboard.request_health_failure')}: ${formatCompactNumber(point.failure)} · ${t('dashboard.success_rate')}: ${formatPercent(point.success_rate)}`;
};

const buildTrafficTrendOption = ({
  dataPalette,
  locale,
  nowMs,
  t,
  timeline,
}: {
  dataPalette: DataPalette;
  locale: string;
  nowMs?: number | null;
  t: (key: string) => string;
  timeline: DashboardTrafficPoint[];
}): TrafficTrendChartOption => ({
  animationDuration: 260,
  backgroundColor: 'transparent',
  color: [dataPalette.blue, dataPalette.emerald],
  dataZoom:
    timeline.length > 12
      ? [
          {
            type: 'inside',
            xAxisIndex: 0,
            filterMode: 'none',
            minSpan: Math.min(100, Math.max(12, (6 / timeline.length) * 100)),
            moveOnMouseMove: true,
            moveOnMouseWheel: false,
            zoomOnMouseWheel: true,
          },
        ]
      : [],
  grid: {
    bottom: 24,
    containLabel: true,
    left: 4,
    right: 10,
    top: 18,
  },
  legend: { show: false },
  tooltip: {
    appendToBody: true,
    axisPointer: {
      lineStyle: { color: dataPalette.surface.axisPointer, type: 'dashed', width: 1 },
      snap: true,
      type: 'line',
    },
    backgroundColor: dataPalette.surface.tooltipBackground,
    borderColor: dataPalette.surface.tooltipBorder,
    borderRadius: 10,
    borderWidth: 1,
    className: styles.echartsTooltipWrapper,
    confine: true,
    extraCssText: dataPalette.surface.tooltipShadow,
    formatter: (params: unknown) => {
      const items = Array.isArray(params) ? params : [params];
      const first = items[0] as { dataIndex?: number } | undefined;
      const point = typeof first?.dataIndex === 'number' ? timeline[first.dataIndex] : undefined;
      const rows = [
        [t('dashboard.traffic_calls'), point?.calls ?? 0],
        [t('dashboard.traffic_tokens'), point?.tokens ?? 0],
      ]
        .map(
          ([label, value]) =>
            `<div class="${styles.echartsTooltipRow}"><span>${label}</span><strong>${formatCompactNumber(Number(value))}</strong></div>`
        )
        .join('');
      return `<div class="${styles.echartsTooltip}"><b>${
        point ? formatHour(point.bucket_ms, locale) : ''
      }</b>${rows}</div>`;
    },
    padding: 0,
    trigger: 'axis',
  },
  xAxis: {
    axisLabel: {
      color: dataPalette.surface.axisLabel,
      fontSize: 10,
      hideOverlap: true,
      margin: 10,
    },
    axisLine: { lineStyle: { color: dataPalette.surface.axisLine } },
    axisTick: { show: false },
    data: timeline.map((point) => formatHour(point.bucket_ms, locale)),
    type: 'category',
  },
  yAxis: [
    {
      axisLabel: {
        color: dataPalette.surface.axisLabel,
        formatter: (value: number) => formatCompactNumber(value),
        fontSize: 10,
      },
      nameTextStyle: { color: dataPalette.surface.axisLabel },
      scale: true,
      splitLine: { lineStyle: { color: dataPalette.surface.splitLine, type: 'dashed' } },
      type: 'value',
    },
    {
      axisLabel: {
        color: dataPalette.blue,
        formatter: (value: number) => formatCompactNumber(value),
        fontSize: 10,
      },
      position: 'right',
      scale: true,
      splitLine: { show: false },
      type: 'value',
    },
  ],
  series: [
    {
      barMaxWidth: 20,
      data: timeline.map((point) => ({
        itemStyle: {
          opacity: isCurrentTrafficBucket(point, nowMs) ? 0.58 : 1,
        },
        value: point.tokens,
      })),
      itemStyle: {
        borderRadius: [4, 4, 0, 0],
        color: dataPalette.emerald,
      },
      name: t('dashboard.traffic_tokens'),
      type: 'bar',
      yAxisIndex: 0,
    },
    {
      areaStyle: {
        color: {
          colorStops: [
            { color: hexToRgba(dataPalette.blue, 0.18), offset: 0 },
            { color: hexToRgba(dataPalette.blue, 0), offset: 1 },
          ],
          x: 0,
          x2: 0,
          y: 0,
          y2: 1,
          type: 'linear',
        },
      },
      data: timeline.map((point) => point.calls),
      lineStyle: { color: dataPalette.blue, width: 2.4 },
      name: t('dashboard.traffic_calls'),
      showSymbol: timeline.length <= 24,
      smooth: 0.22,
      symbol: 'circle',
      symbolSize: 5,
      type: 'line',
      yAxisIndex: 1,
    },
  ],
});

export function TrafficOverviewCard({
  timeline,
  trafficNowMs,
  todayRequestHealthTimeline,
  tokenMix,
  loading,
}: TrafficOverviewCardProps) {
  const { t, i18n } = useTranslation();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const dataPalette = getDataPalette(resolvedTheme);
  const tokenColorMap = buildTokenColorMap(dataPalette);
  const [activeTokenSegment, setActiveTokenSegment] = useState<DashboardTokenMixSegment | null>(
    null
  );
  const visibleTimeline = buildVisibleTrafficTimeline(timeline, trafficNowMs);
  const hasData = visibleTimeline.some((point) => point.calls > 0 || point.tokens > 0);
  const visibleTokenMix = tokenMix.filter((segment) => visibleTokenMixKeys.has(segment.key));
  const tokenMixTotal = visibleTokenMix.reduce((acc, s) => acc + s.tokens, 0);
  const displayTotalTokens = tokenMixTotal;
  const hasTokenMixData = visibleTokenMix.some((segment) => segment.tokens > 0);
  const rankedTokenMix = [...visibleTokenMix].sort((left, right) => right.tokens - left.tokens);
  const maxTokenMixTokens = rankedTokenMix.reduce(
    (max, segment) => Math.max(max, segment.tokens),
    0
  );
  const healthPoints = todayRequestHealthTimeline?.points ?? [];
  const healthBucketMs = todayRequestHealthTimeline?.bucket_ms || fallbackHealthBucketMs;
  const healthRowsPerHour = Math.max(1, Math.round((60 * 60 * 1000) / healthBucketMs));
  const healthRows = healthPoints.length
    ? Array.from({ length: healthRowsPerHour }, (_, minuteIndex) =>
        Array.from(
          { length: 24 },
          (_, hourIndex) => healthPoints[hourIndex * healthRowsPerHour + minuteIndex]
        )
      )
    : [];
  const hourLabelIndexes = [0, 6, 12, 18, 23];
  const trafficTrendOption = useMemo(
    () =>
      buildTrafficTrendOption({
        dataPalette,
        locale: i18n.language,
        nowMs: trafficNowMs,
        t,
        timeline: visibleTimeline,
      }),
    [dataPalette, i18n.language, t, trafficNowMs, visibleTimeline]
  );

  return (
    <div className={styles.chartsGrid}>
      {/* 1. 流量趋势 */}
      <section className={`${styles.chartCard} ${styles.activityCard}`}>
        <div className={styles.cardHeader}>
          <h3>{t('dashboard.traffic_trend_today')}</h3>
          <div className={styles.legend}>
            <span className={styles.legendItem}>
              <span className={styles.dot} style={{ background: dataPalette.blue }} />{' '}
              {t('dashboard.traffic_calls')}
            </span>
            <span className={styles.legendItem}>
              <span className={styles.dot} style={{ background: dataPalette.emerald }} />{' '}
              {t('dashboard.traffic_tokens')}
            </span>
          </div>
        </div>
        <div className={styles.trafficChart}>
          <div className={styles.chartViewport}>
            {hasData ? (
              <EChartsView
                option={trafficTrendOption}
                className={styles.echartsCanvas}
                ariaLabel={t('dashboard.traffic_trend_today')}
              />
            ) : null}
            {!hasData && !loading && (
              <div className={styles.empty}>{t('dashboard.no_traffic_data')}</div>
            )}
            {loading && !hasData && <div className={styles.empty}>...</div>}
          </div>
        </div>
      </section>

      {/* 2. 请求健康时间线 */}
      <section className={`${styles.chartCard} ${styles.healthTimelineCard}`}>
        <div className={`${styles.cardHeader} ${styles.healthHeader}`}>
          <div className={styles.healthTitle}>
            <h3>{t('dashboard.request_health_timeline')}</h3>
            <p>{t('dashboard.request_health_timeline_desc')}</p>
          </div>
          <div className={styles.healthSummary}>
            <strong>
              {todayRequestHealthTimeline
                ? formatPercent(todayRequestHealthTimeline.success_rate)
                : '-'}
            </strong>
            <div className={styles.healthCounts}>
              <span>
                <i className={styles.healthGood} />{' '}
                {formatCompactNumber(todayRequestHealthTimeline?.success_calls ?? 0)}
              </span>
              <span>
                <i className={styles.healthBad} />{' '}
                {formatCompactNumber(todayRequestHealthTimeline?.failure_calls ?? 0)}
              </span>
            </div>
          </div>
        </div>
        <div className={styles.healthMatrix}>
          {healthRows.length > 0 ? (
            <div className={styles.healthHourAxis}>
              {hourLabelIndexes.map((index) => {
                const point = healthRows[0]?.[index];
                return (
                  <span key={index} style={{ gridColumn: index + 2 }}>
                    {point ? formatHour(point.bucket_ms, i18n.language).slice(0, 2) : '--'}
                  </span>
                );
              })}
            </div>
          ) : null}
          {healthRows.map((row, minuteIndex) => (
            <div key={minuteIndex} className={styles.healthDayRow}>
              <span className={styles.healthDayLabel}>
                {formatMinuteLabel(row[0]?.bucket_ms, i18n.language)}
              </span>
              <div className={styles.healthDayCells}>
                {row.map((point, hourIndex) =>
                  point ? (
                    <div
                      key={point.bucket_ms}
                      className={`${styles.healthCell} ${styles[healthToneClassMap[point.tone] ?? 'healthEmpty']}`}
                      style={{ '--cell-intensity': point.intensity } as HealthCellStyle}
                      title={buildHealthTitle(point, i18n.language, t)}
                    />
                  ) : (
                    <div
                      key={`${minuteIndex}-${hourIndex}`}
                      className={`${styles.healthCell} ${styles.healthFuture}`}
                    />
                  )
                )}
              </div>
            </div>
          ))}
          {healthPoints.length === 0 && !loading ? (
            <div className={styles.healthEmptyState}>{t('dashboard.request_health_no_data')}</div>
          ) : null}
          {healthPoints.length === 0 && loading ? (
            <div className={styles.healthEmptyState}>...</div>
          ) : null}
        </div>
        <div className={styles.healthLegend}>
          <span>
            <i className={styles.healthEmpty} /> {t('dashboard.request_health_no_request')}
          </span>
          <span>
            <i className={styles.healthGood} /> {t('dashboard.request_health_success')}
          </span>
          <span>
            <i className={styles.healthWarn} /> {t('dashboard.request_health_warning')}
          </span>
          <span>
            <i className={styles.healthBad} /> {t('dashboard.request_health_failure')}
          </span>
          <span>
            <i className={styles.healthFuture} /> {t('dashboard.request_health_future')}
          </span>
        </div>
      </section>

      {/* 3. Token 构成 */}
      <section className={`${styles.chartCard} ${styles.tokenCard}`}>
        <div className={styles.cardHeader}>
          <h3>{t('dashboard.token_mix_today')}</h3>
        </div>
        <div className={styles.tokenRankSection}>
          <div className={styles.tokenSummary}>
            <span>{t('dashboard.total_tokens')}</span>
            <strong>
              {loading && !hasTokenMixData ? '...' : formatCompactNumber(displayTotalTokens)}
            </strong>
          </div>
          {hasTokenMixData ? (
            <div className={styles.tokenRankList}>
              {rankedTokenMix.map((segment) => (
                <button
                  type="button"
                  key={segment.key}
                  className={`${styles.tokenRankRow} ${
                    activeTokenSegment?.key === segment.key ? styles.tokenRankRowActive : ''
                  }`}
                  onMouseEnter={() => setActiveTokenSegment(segment)}
                  onMouseLeave={() => setActiveTokenSegment(null)}
                  onFocus={() => setActiveTokenSegment(segment)}
                  onBlur={() => setActiveTokenSegment(null)}
                  aria-label={`${t(tokenLabelMap[segment.key] || segment.key)} ${formatCompactNumber(segment.tokens)} ${(segment.share * 100).toFixed(1)}%`}
                >
                  <span className={styles.tokenRankHeader}>
                    <span className={styles.tokenRankIdentity}>
                      <i
                        className={styles.tokenRankSwatch}
                        style={{ background: tokenColorMap[segment.key] || dataPalette.slateMuted }}
                        aria-hidden="true"
                      />
                      <span>{t(tokenLabelMap[segment.key] || segment.key)}</span>
                    </span>
                    <span className={styles.tokenRankMeta}>
                      <strong>{formatCompactNumber(segment.tokens)}</strong>
                      <span>{(segment.share * 100).toFixed(1)}%</span>
                    </span>
                  </span>
                  <span className={styles.tokenRankTrack}>
                    <span
                      className={styles.tokenRankBar}
                      style={
                        {
                          '--rank-share':
                            maxTokenMixTokens > 0 ? segment.tokens / maxTokenMixTokens : 0,
                          '--rank-color': tokenColorMap[segment.key] || dataPalette.slateMuted,
                        } as TokenRankStyle
                      }
                    />
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <div className={styles.tokenEmptyState}>
              {loading ? '...' : t('dashboard.no_token_mix_data')}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
