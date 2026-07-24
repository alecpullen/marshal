package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CalibrationSample is one paired observation of the EstimatorCounter's
// token count versus the provider-reported prompt-token count for a single
// chat turn.
type CalibrationSample struct {
	ID              int64
	ProjectID       int64
	SessionID       string
	Provider        string
	Model           string
	EstimatorTokens int
	ProviderTokens  int
	CreatedAt       time.Time
}

// CalibrationSummary aggregates paired calibration observations for a
// project/session. Ratios are estimator/provider; a ratio < 1.0 means the
// estimator under-counts (the safe direction for rollover, since
// UsageCounter takes the larger of the two).
type CalibrationSummary struct {
	Samples       int
	MeanEstimator float64
	MeanProvider  float64
	MeanRatio     float64
	MaxRatio      float64
	MinRatio      float64
}

// InsertCalibrationSample persists one paired observation.
func (db *DB) InsertCalibrationSample(s CalibrationSample) (int64, error) {
	var sessionID sql.NullString
	if s.SessionID != "" {
		sessionID = sql.NullString{String: s.SessionID, Valid: true}
	}
	res, err := db.sqlDB.Exec(
		`INSERT INTO token_calibration
		 (project_id, session_id, provider, model, estimator_tokens, provider_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ProjectID, sessionID, s.Provider, s.Model,
		s.EstimatorTokens, s.ProviderTokens,
		s.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert calibration sample: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("calibration insert id: %w", err)
	}
	return id, nil
}

// CalibrationSummary aggregates calibration samples for a project (and,
// optionally, a single session). When sessionID is empty, all sessions for
// the project are summed.
func (db *DB) CalibrationSummary(projectID int64, sessionID string) (CalibrationSummary, error) {
	query := `SELECT estimator_tokens, provider_tokens FROM token_calibration WHERE project_id = ?`
	args := []any{projectID}
	if sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return CalibrationSummary{}, fmt.Errorf("calibration summary: %w", err)
	}
	defer rows.Close()

	var sum CalibrationSummary
	var sumEst, sumProv, ratioSum float64
	var ratioSamples int
	for rows.Next() {
		var est, prov int
		if err := rows.Scan(&est, &prov); err != nil {
			return CalibrationSummary{}, fmt.Errorf("scan calibration: %w", err)
		}
		sum.Samples++
		sumEst += float64(est)
		sumProv += float64(prov)
		if prov > 0 {
			r := float64(est) / float64(prov)
			if ratioSamples == 0 {
				sum.MinRatio = r
				sum.MaxRatio = r
			}
			if r < sum.MinRatio {
				sum.MinRatio = r
			}
			if r > sum.MaxRatio {
				sum.MaxRatio = r
			}
			ratioSum += r
			ratioSamples++
		}
	}
	if err := rows.Err(); err != nil {
		return CalibrationSummary{}, fmt.Errorf("iterate calibration: %w", err)
	}
	if sum.Samples > 0 {
		sum.MeanEstimator = sumEst / float64(sum.Samples)
		sum.MeanProvider = sumProv / float64(sum.Samples)
		// Mean ratio is over samples with a non-zero provider count only;
		// samples with prov==0 are excluded from the ratio (undefined).
		if ratioSamples > 0 {
			sum.MeanRatio = ratioSum / float64(ratioSamples)
		}
	}
	return sum, nil
}
