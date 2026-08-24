package probequery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const supportedTargetTypesSQL = `('tcp', 'http', 'https')`

const minimumProbeInterval = 10 * time.Second

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, ErrInvalidArgument
	}
	return &Service{pool: pool}, nil
}

func (s *Service) ListTargets(ctx context.Context, nodeID string) (PanelProbeTargetListResponse, error) {
	if !ValidUUID(nodeID) {
		return PanelProbeTargetListResponse{}, ErrInvalidArgument
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return PanelProbeTargetListResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1::uuid)`, nodeID).Scan(&exists); err != nil {
		return PanelProbeTargetListResponse{}, fmt.Errorf("check probe target node: %w", err)
	}
	if !exists {
		return PanelProbeTargetListResponse{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, name, probe_type, enabled, retention_seconds
		FROM probe_targets
		WHERE node_id = $1::uuid
		  AND probe_type IN `+supportedTargetTypesSQL+`
		ORDER BY created_at ASC, id ASC
		LIMIT 33
	`, nodeID)
	if err != nil {
		return PanelProbeTargetListResponse{}, fmt.Errorf("query panel probe targets: %w", err)
	}
	defer rows.Close()
	targets := make([]PanelProbeTargetSummary, 0)
	for rows.Next() {
		var target PanelProbeTargetSummary
		if err := rows.Scan(&target.TargetID, &target.Name, &target.Type, &target.Enabled, &target.RetentionSeconds); err != nil {
			return PanelProbeTargetListResponse{}, fmt.Errorf("scan panel probe target: %w", err)
		}
		if !ValidUUID(target.TargetID) || !supportedTargetType(target.Type) || target.RetentionSeconds < 1 || target.RetentionSeconds > 7776000 {
			return PanelProbeTargetListResponse{}, ErrInvariant
		}
		targets = append(targets, target)
		if len(targets) > MaxTargets {
			return PanelProbeTargetListResponse{}, ErrInvariant
		}
	}
	if err := rows.Err(); err != nil {
		return PanelProbeTargetListResponse{}, fmt.Errorf("iterate panel probe targets: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return PanelProbeTargetListResponse{}, fmt.Errorf("commit panel probe target query: %w", err)
	}
	return PanelProbeTargetListResponse{NodeID: nodeID, Targets: targets}, nil
}

func (s *Service) Probes(ctx context.Context, request ProbeSeriesRequest) (ProbeSeriesResponse, error) {
	if !validRequest(request) {
		return ProbeSeriesResponse{}, ErrInvalidArgument
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return ProbeSeriesResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	target, asOf, err := queryTarget(ctx, tx, request.NodeID, request.TargetID)
	if err != nil {
		return ProbeSeriesResponse{}, err
	}
	from, to := clippedWindow(request.From.UTC(), request.To.UTC(), asOf, time.Duration(target.RetentionSeconds)*time.Second)
	resolution, err := chooseResolution(request.Resolution, from, to, asOf, target)
	if err != nil {
		return ProbeSeriesResponse{}, err
	}
	response := ProbeSeriesResponse{
		Target: target, Resolution: resolution, From: from, To: to, AsOf: asOf,
		Points: make([]ProbeSeriesPoint, 0),
	}
	if from.Before(to) {
		candidates := []Resolution{resolution}
		if request.Resolution == ResolutionAuto {
			switch resolution {
			case ResolutionRaw:
				candidates = append(candidates, Resolution5m, Resolution1h)
			case Resolution5m:
				candidates = append(candidates, Resolution1h)
			}
		}
		var points []ProbeSeriesPoint
		for _, candidate := range candidates {
			if candidate != resolution {
				if _, candidateErr := chooseResolution(candidate, from, to, asOf, target); candidateErr != nil {
					continue
				}
			}
			covered, coverageErr := resolutionCovered(ctx, tx, candidate, to, asOf)
			if coverageErr != nil {
				return ProbeSeriesResponse{}, coverageErr
			}
			if !covered {
				if request.Resolution == ResolutionAuto {
					continue
				}
				return ProbeSeriesResponse{}, ErrResolutionUnavailable
			}
			queried, queryErr := queryPoints(ctx, tx, target.TargetID, candidate, from, to)
			if queryErr == ErrResolutionUnavailable && request.Resolution == ResolutionAuto {
				continue
			}
			if queryErr != nil {
				return ProbeSeriesResponse{}, queryErr
			}
			resolution = candidate
			points = queried
			break
		}
		if points == nil {
			return ProbeSeriesResponse{}, ErrResolutionUnavailable
		}
		response.Resolution = resolution
		response.Points = points
	}
	if err := tx.Commit(ctx); err != nil {
		return ProbeSeriesResponse{}, fmt.Errorf("commit probe trend query: %w", err)
	}
	return response, nil
}

func (s *Service) beginRead(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin probe query transaction: %w", err)
	}
	return tx, nil
}

func queryTarget(ctx context.Context, tx pgx.Tx, nodeID, targetID string) (ProbeTarget, time.Time, error) {
	var target ProbeTarget
	var asOf time.Time
	var port pgtype.Int4
	var path pgtype.Text
	err := tx.QueryRow(ctx, `
		SELECT CURRENT_TIMESTAMP, id::text, node_id::text, name, probe_type,
		       host, port, path, interval_seconds, timeout_seconds,
		       retention_seconds, enabled, config_version, created_at, updated_at
		FROM probe_targets
		WHERE node_id = $1::uuid AND id = $2::uuid
		  AND probe_type IN `+supportedTargetTypesSQL+`
	`, nodeID, targetID).Scan(
		&asOf, &target.TargetID, &target.NodeID, &target.Name, &target.Type,
		&target.Host, &port, &path, &target.IntervalSeconds, &target.TimeoutSeconds,
		&target.RetentionSeconds, &target.Enabled, &target.ConfigVersion, &target.CreatedAt, &target.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return ProbeTarget{}, time.Time{}, ErrNotFound
	}
	if err != nil {
		return ProbeTarget{}, time.Time{}, fmt.Errorf("query probe target: %w", err)
	}
	if port.Valid {
		value := port.Int32
		target.Port = &value
	}
	if path.Valid {
		value := path.String
		target.Path = &value
	}
	target.CreatedAt = target.CreatedAt.UTC()
	target.UpdatedAt = target.UpdatedAt.UTC()
	asOf = asOf.UTC()
	if target.NodeID != nodeID || target.TargetID != targetID || !supportedTargetType(target.Type) ||
		target.IntervalSeconds < 10 || target.RetentionSeconds < 1 || target.RetentionSeconds > 7776000 {
		return ProbeTarget{}, time.Time{}, ErrInvariant
	}
	return target, asOf, nil
}

func validRequest(request ProbeSeriesRequest) bool {
	return ValidUUID(request.NodeID) && ValidUUID(request.TargetID) &&
		!request.From.IsZero() && !request.To.IsZero() && request.From.Before(request.To) &&
		ValidResolution(request.Resolution)
}

func supportedTargetType(value string) bool {
	switch value {
	case "tcp", "http", "https":
		return true
	default:
		return false
	}
}

func clippedWindow(from, to, asOf time.Time, retention time.Duration) (time.Time, time.Time) {
	cutoff := asOf.Add(-retention)
	if from.Before(cutoff) {
		from = cutoff
	}
	if to.After(asOf) {
		to = asOf
	}
	if !from.Before(to) {
		if !to.After(cutoff) {
			return cutoff, cutoff
		}
		return asOf, asOf
	}
	return from.UTC(), to.UTC()
}

func chooseResolution(requested Resolution, from, to, asOf time.Time, target ProbeTarget) (Resolution, error) {
	if !from.Before(to) {
		if requested == ResolutionAuto {
			return ResolutionRaw, nil
		}
		return requested, nil
	}
	retentionCutoff := asOf.Add(-time.Duration(target.RetentionSeconds) * time.Second)
	rawCutoff := laterTime(retentionCutoff, asOf.Add(-24*time.Hour))
	fiveMinuteCutoff := laterTime(retentionCutoff, asOf.Add(-7*24*time.Hour))
	available := func(resolution Resolution) bool {
		switch resolution {
		case ResolutionRaw:
			return !from.Before(rawCutoff) && to.Sub(from) <= 24*time.Hour &&
				estimatedPoints(from, to, minimumProbeInterval) <= MaxPoints
		case Resolution5m:
			return !from.Before(fiveMinuteCutoff) && to.Sub(from) <= 7*24*time.Hour &&
				estimatedPoints(from, to, 5*time.Minute) <= MaxPoints
		case Resolution1h:
			return !from.Before(retentionCutoff) && estimatedPoints(from, to, time.Hour) <= MaxPoints
		default:
			return false
		}
	}
	if requested != ResolutionAuto {
		if !available(requested) {
			return "", ErrResolutionUnavailable
		}
		return requested, nil
	}
	for _, candidate := range []Resolution{ResolutionRaw, Resolution5m, Resolution1h} {
		if available(candidate) {
			return candidate, nil
		}
	}
	return "", ErrResolutionUnavailable
}

func estimatedPoints(from, to time.Time, step time.Duration) int64 {
	if step <= 0 || !from.Before(to) {
		return 0
	}
	duration := to.Sub(from)
	return int64((duration + step - 1) / step)
}

func resolutionCovered(ctx context.Context, tx pgx.Tx, resolution Resolution, to, asOf time.Time) (bool, error) {
	if resolution == ResolutionRaw {
		return true, nil
	}
	var name string
	var width time.Duration
	switch resolution {
	case Resolution5m:
		name = "probe-result-5m"
		width = 5 * time.Minute
	case Resolution1h:
		name = "probe-result-1h"
		width = time.Hour
	default:
		return false, ErrInvalidArgument
	}
	var watermark pgtype.Timestamptz
	err := tx.QueryRow(ctx, `SELECT watermark_at FROM job_watermarks WHERE job_name = $1`, name).Scan(&watermark)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query %s coverage watermark: %w", resolution, err)
	}
	if !watermark.Valid {
		return false, nil
	}
	value := watermark.Time.UTC()
	asOfBoundary := alignedFloor(asOf, width)
	if !value.Equal(alignedFloor(value, width)) || value.After(asOfBoundary) {
		return false, ErrInvariant
	}
	return !value.Before(alignedFloor(to, width)), nil
}

func queryPoints(ctx context.Context, tx pgx.Tx, targetID string, resolution Resolution, from, to time.Time) ([]ProbeSeriesPoint, error) {
	var query string
	switch resolution {
	case ResolutionRaw:
		query = `
			SELECT effective_at, 1::bigint, sent_count, received_count,
			       http_status_code,
			       CASE
			           WHEN http_status_code < 200 OR http_status_code >= 400 THEN received_count
			           ELSE 0
			       END::bigint AS http_error_count,
			       latency_sum_us::text, latency_min_us, latency_max_us
			FROM probe_result_raw
			WHERE target_id = $1::uuid
			  AND effective_at >= $2::timestamptz
			  AND effective_at < $3::timestamptz
			ORDER BY effective_at ASC, id ASC
			LIMIT 2201`
	case Resolution5m:
		query = `
			SELECT bucket_start, result_count::bigint, sent_count, received_count,
			       NULL::integer AS http_status_code, http_error_count,
			       latency_sum_us::text, latency_min_us, latency_max_us
			FROM probe_result_5m
			WHERE target_id = $1::uuid
			  AND bucket_start >= $2::timestamptz
			  AND bucket_start < $3::timestamptz
			ORDER BY bucket_start ASC
			LIMIT 2201`
	case Resolution1h:
		query = `
			SELECT bucket_start, result_count::bigint, sent_count, received_count,
			       NULL::integer AS http_status_code, http_error_count,
			       latency_sum_us::text, latency_min_us, latency_max_us
			FROM probe_result_1h
			WHERE target_id = $1::uuid
			  AND bucket_start >= $2::timestamptz
			  AND bucket_start < $3::timestamptz
			ORDER BY bucket_start ASC
			LIMIT 2201`
	default:
		return nil, ErrInvalidArgument
	}
	rows, err := tx.Query(ctx, query, targetID, from, to)
	if err != nil {
		return nil, fmt.Errorf("query %s probe points: %w", resolution, err)
	}
	defer rows.Close()
	points := make([]ProbeSeriesPoint, 0)
	for rows.Next() {
		var point ProbeSeriesPoint
		var latencySum string
		var httpStatus pgtype.Int4
		var minimum, maximum pgtype.Int8
		if err := rows.Scan(&point.Time, &point.ResultCount, &point.SentCount, &point.ReceivedCount,
			&httpStatus, &point.HTTPErrorCount, &latencySum, &minimum, &maximum); err != nil {
			return nil, fmt.Errorf("scan %s probe point: %w", resolution, err)
		}
		point.Time = point.Time.UTC()
		if httpStatus.Valid {
			value := httpStatus.Int32
			point.HTTPStatusCode = &value
		}
		if minimum.Valid {
			value := minimum.Int64
			point.LatencyMinUS = &value
		}
		if maximum.Valid {
			value := maximum.Int64
			point.LatencyMaxUS = &value
		}
		if err := derivePoint(&point, latencySum, resolution); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s probe points: %w", resolution, err)
	}
	if len(points) > MaxPoints {
		return nil, ErrResolutionUnavailable
	}
	return points, nil
}

func derivePoint(point *ProbeSeriesPoint, latencySum string, resolution Resolution) error {
	sum, ok := new(big.Int).SetString(latencySum, 10)
	if !ok || sum.Sign() < 0 || point.ResultCount < 1 || point.SentCount < 1 ||
		point.ReceivedCount < 0 || point.ReceivedCount > point.SentCount ||
		point.HTTPErrorCount < 0 || point.HTTPErrorCount > point.ReceivedCount {
		return ErrInvariant
	}
	switch resolution {
	case ResolutionRaw:
		if point.ResultCount != 1 || point.SentCount != 1 || point.ReceivedCount > 1 {
			return ErrInvariant
		}
		expectedHTTPErrorCount := int64(0)
		if point.HTTPStatusCode != nil {
			if *point.HTTPStatusCode < 100 || *point.HTTPStatusCode > 599 {
				return ErrInvariant
			}
			if *point.HTTPStatusCode < 200 || *point.HTTPStatusCode >= 400 {
				expectedHTTPErrorCount = point.ReceivedCount
			}
		}
		if point.HTTPErrorCount != expectedHTTPErrorCount {
			return ErrInvariant
		}
	case Resolution5m, Resolution1h:
		if point.HTTPStatusCode != nil {
			return ErrInvariant
		}
	default:
		return ErrInvariant
	}
	if point.ReceivedCount == 0 {
		if sum.Sign() != 0 || point.LatencyMinUS != nil || point.LatencyMaxUS != nil {
			return ErrInvariant
		}
	} else {
		if point.LatencyMinUS == nil || point.LatencyMaxUS == nil || *point.LatencyMinUS < 0 ||
			*point.LatencyMaxUS < *point.LatencyMinUS {
			return ErrInvariant
		}
		average := new(big.Rat).SetFrac(sum, big.NewInt(point.ReceivedCount))
		if average.Cmp(new(big.Rat).SetInt64(*point.LatencyMinUS)) < 0 ||
			average.Cmp(new(big.Rat).SetInt64(*point.LatencyMaxUS)) > 0 {
			return ErrInvariant
		}
		value, _ := average.Float64()
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return ErrInvariant
		}
		point.AverageLatencyUS = &value
	}
	point.LatencySumUS = jsonNumber(latencySum)
	lost := point.SentCount - point.ReceivedCount
	loss := new(big.Rat).SetFrac(big.NewInt(lost), big.NewInt(point.SentCount))
	point.LossRate, _ = loss.Float64()
	failed := lost + point.HTTPErrorCount
	failure := new(big.Rat).SetFrac(big.NewInt(failed), big.NewInt(point.SentCount))
	point.FailureRate, _ = failure.Float64()
	return nil
}

func jsonNumber(value string) json.Number {
	return json.Number(value)
}

func laterTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func alignedFloor(value time.Time, width time.Duration) time.Time {
	seconds := int64(width / time.Second)
	return time.Unix((value.UTC().Unix()/seconds)*seconds, 0).UTC()
}
