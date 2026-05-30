package metric

import (
	"net/http"
	"qqbot-ai/internal/common"
	"qqbot-ai/internal/db"
	"sort"
	"time"
)

// MetricService 记录数值观测。
type MetricService struct {
	store *db.Store
}

// NewMetricService 创建由 Store 支撑的指标记录器。
func NewMetricService(store *db.Store) *MetricService { return &MetricService{store: store} }

// MetricChartService 管理图表定义和时间分桶聚合。
type MetricChartService struct {
	store  *db.Store
	metric *MetricService
}

func NewMetricChartService(store *db.Store, metric *MetricService) *MetricChartService {
	return &MetricChartService{store: store, metric: metric}
}

func (s *MetricChartService) List() map[string]any {
	data := s.store.Snapshot()
	items := append([]db.MetricChart(nil), data.MetricCharts...)
	sort.Slice(items, func(i, j int) bool { return items[i].ChartName < items[j].ChartName })
	return map[string]any{"items": items}
}

func (s *MetricChartService) Create(req map[string]any) (map[string]any, int) {
	now := time.Now()
	chart := db.MetricChart{
		ChartName: common.AsString(req["chartName"]), MetricName: common.AsString(req["metricName"]),
		Aggregator: common.AsString(req["aggregator"]), TagFilters: map[string]string{}, GroupByTag: common.AsString(req["groupByTag"]),
		CreatedAt: now, UpdatedAt: now,
	}
	if chart.Aggregator == "" {
		chart.Aggregator = "sum"
	}
	if filters, ok := req["tagFilters"].(map[string]any); ok {
		for k, v := range filters {
			chart.TagFilters[k] = common.AsString(v)
		}
	}
	chart = s.store.UpsertMetricChart(chart)
	return map[string]any{"chart": chart}, http.StatusOK
}

func (s *MetricChartService) Delete(name string) map[string]any {
	s.store.DeleteMetricChart(name)
	return map[string]any{"chartName": name, "deleted": true}
}

func (s *MetricChartService) Data(chartName, bucket, rangePreset string, startAt, endAt *time.Time) (map[string]any, int) {
	data := s.store.Snapshot()
	var chart *db.MetricChart
	for _, c := range data.MetricCharts {
		if c.ChartName == chartName {
			chart = new(c)
			break
		}
	}
	if chart == nil {
		return map[string]any{"message": "chart not found"}, http.StatusNotFound
	}
	start, end := resolveRange(rangePreset, startAt, endAt)
	step := bucketDuration(bucket)
	points := map[string]map[time.Time][]float64{}
	for _, m := range data.Metrics {
		if m.MetricName != chart.MetricName || m.OccurredAt.Before(start) || m.OccurredAt.After(end) || !matchTags(m.Tags, chart.TagFilters) {
			continue
		}
		key := "default"
		if chart.GroupByTag != "" {
			key = m.Tags[chart.GroupByTag]
			if key == "" {
				key = "(none)"
			}
		}
		if points[key] == nil {
			points[key] = map[time.Time][]float64{}
		}
		b := start.Add(time.Duration(int64(m.OccurredAt.Sub(start)) / int64(step) * int64(step)))
		points[key][b] = append(points[key][b], m.Value)
	}
	series := []map[string]any{}
	for key, values := range points {
		pts := []map[string]any{}
		for t := start; !t.After(end); t = t.Add(step) {
			pts = append(pts, map[string]any{"bucketStart": common.ISO(t), "value": aggregate(values[t], chart.Aggregator)})
		}
		series = append(series, map[string]any{"key": key, "label": key, "points": pts})
	}
	return map[string]any{"chart": chart, "bucket": bucket, "startAt": common.ISO(start), "endAt": common.ISO(end), "series": series}, http.StatusOK
}

func resolveRange(preset string, startAt, endAt *time.Time) (time.Time, time.Time) {
	if startAt != nil && endAt != nil {
		return *startAt, *endAt
	}
	end := time.Now()
	d := map[string]time.Duration{"1m": time.Minute, "10m": 10 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour, "3h": 3 * time.Hour, "6h": 6 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour, "2d": 48 * time.Hour}[preset]
	if d == 0 {
		d = time.Hour
	}
	return end.Add(-d), end
}

func bucketDuration(bucket string) time.Duration {
	switch bucket {
	case "10s":
		return 10 * time.Second
	case "5m":
		return 5 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	default:
		return time.Minute
	}
}

func matchTags(tags, filters map[string]string) bool {
	for k, v := range filters {
		if tags[k] != v {
			return false
		}
	}
	return true
}

func aggregate(values []float64, agg string) any {
	if len(values) == 0 {
		return nil
	}
	switch agg {
	case "count":
		return len(values)
	case "avg":
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return sum / float64(len(values))
	case "max":
		m := values[0]
		for _, v := range values {
			if v > m {
				m = v
			}
		}
		return m
	case "min":
		m := values[0]
		for _, v := range values {
			if v < m {
				m = v
			}
		}
		return m
	case "last":
		return values[len(values)-1]
	default:
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return sum
	}
}

// IthomePoller 拉取 IThome RSS 文章并发送 Agent 事件。
