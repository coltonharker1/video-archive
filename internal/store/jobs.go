package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// JobType identifies what work a job performs.
type JobType string

const (
	JobSample  JobType = "sample"
	JobDetect  JobType = "detect"
	JobEmbed   JobType = "embed"
	JobTrack   JobType = "track"
	JobCluster JobType = "cluster"
)

// JobStatus represents a job's position in its lifecycle.
type JobStatus string

const (
	StatusPending  JobStatus = "pending"
	StatusRunning  JobStatus = "running"
	StatusComplete JobStatus = "complete"
	StatusFailed   JobStatus = "failed"
)

// Default retry policy per job type.
var defaultMaxRetries = map[JobType]int{
	JobSample:  3,
	JobDetect:  3,
	JobEmbed:   3,
	JobTrack:   2,
	JobCluster: 2,
}

// predecessorType maps each job type to the type it depends on.
var predecessorType = map[JobType]JobType{
	JobDetect:  JobSample,
	JobEmbed:   JobDetect,
	JobTrack:   JobEmbed,
	JobCluster: JobTrack,
}

// successorTypes maps each job type to downstream types that become
// unrunnable if this one fails permanently.
var successorTypes = map[JobType][]JobType{
	JobSample:  {JobDetect, JobEmbed, JobTrack, JobCluster},
	JobDetect:  {JobEmbed, JobTrack, JobCluster},
	JobEmbed:   {JobTrack, JobCluster},
	JobTrack:   {JobCluster},
	JobCluster: nil,
}

// Job is a single unit of work in the queue.
type Job struct {
	ID          int64
	RecordingID int64
	Type        JobType
	Status      JobStatus
	Attempt     int
	MaxRetries  int
	Error       string
	Progress    string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// ErrNoJob is returned by ClaimNextPendingJob when no work is available.
var ErrNoJob = errors.New("no pending jobs")

// EnqueueJob inserts a new pending job for the given recording.
func (db *DB) EnqueueJob(recordingID int64, jobType JobType) (int64, error) {
	maxRetries, ok := defaultMaxRetries[jobType]
	if !ok {
		maxRetries = 3
	}

	res, err := db.conn.Exec(`
		INSERT INTO jobs (recording_id, type, status, max_retries)
		VALUES (?, ?, 'pending', ?)`,
		recordingID, string(jobType), maxRetries,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueuing job: %w", err)
	}
	return res.LastInsertId()
}

// ClaimNextPendingJob atomically fetches the oldest pending job and marks
// it as running. It skips jobs in backoff and jobs whose predecessor stage
// has not completed. Returns ErrNoJob if nothing is ready.
func (db *DB) ClaimNextPendingJob() (*Job, error) {
	query := fmt.Sprintf(`
		UPDATE jobs
		SET status = 'running',
		    attempt = attempt + 1
		WHERE id = (
			SELECT id FROM jobs AS j
			WHERE j.status = 'pending'
			  AND (j.finished_at IS NULL
			       OR datetime(j.finished_at, '+' || (j.attempt * %d) || ' seconds') <= datetime('now'))
			  AND (
			       j.type = 'sample'
			       OR EXISTS (
			            SELECT 1 FROM jobs AS p
			            WHERE p.recording_id = j.recording_id
			              AND p.status = 'complete'
			              AND p.type = CASE j.type
			                             WHEN 'detect'  THEN 'sample'
			                             WHEN 'embed'   THEN 'detect'
			                             WHEN 'track'   THEN 'embed'
			                             WHEN 'cluster' THEN 'track'
			                           END
			       )
			  )
			ORDER BY j.created_at
			LIMIT 1
		)
		RETURNING id, recording_id, type, status, attempt, max_retries,
		          COALESCE(error, ''), COALESCE(progress, ''), created_at
	`, db.backoffSecondsPerAttempt)

	row := db.conn.QueryRow(query)

	var j Job
	var jobType, status, createdAt string

	err := row.Scan(
		&j.ID, &j.RecordingID, &jobType, &status,
		&j.Attempt, &j.MaxRetries, &j.Error, &j.Progress, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNoJob
	}
	if err != nil {
		return nil, fmt.Errorf("claiming job: %w", err)
	}

	j.Type = JobType(jobType)
	j.Status = JobStatus(status)
	j.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &j, nil
}

// MarkJobStarted records the real execution start time.
func (db *DB) MarkJobStarted(id int64) error {
	_, err := db.conn.Exec(`
		UPDATE jobs SET started_at = datetime('now') WHERE id = ?`, id)
	return err
}

// MarkJobComplete transitions a job to complete status.
func (db *DB) MarkJobComplete(id int64) error {
	_, err := db.conn.Exec(`
		UPDATE jobs
		SET status = 'complete', finished_at = datetime('now'), error = NULL
		WHERE id = ?`, id)
	return err
}

// MarkJobFailed transitions a job to terminal failed status and cascade-fails
// downstream pending jobs for the same recording.
func (db *DB) MarkJobFailed(id int64, errMsg string) error {
	if _, err := db.conn.Exec(`
		UPDATE jobs
		SET status = 'failed', finished_at = datetime('now'), error = ?
		WHERE id = ?`, errMsg, id); err != nil {
		return err
	}

	var failedType string
	var recordingID int64
	if err := db.conn.QueryRow(`
		SELECT type, recording_id FROM jobs WHERE id = ?`, id).Scan(&failedType, &recordingID); err != nil {
		return nil
	}

	successors, ok := successorTypes[JobType(failedType)]
	if !ok || len(successors) == 0 {
		return nil
	}

	inList := "'" + string(successors[0]) + "'"
	for _, t := range successors[1:] {
		inList += ",'" + string(t) + "'"
	}

	cascadeMsg := fmt.Sprintf("cascade: predecessor %s failed for this recording", failedType)
	_, _ = db.conn.Exec(fmt.Sprintf(`
		UPDATE jobs
		SET status = 'failed', finished_at = datetime('now'), error = ?
		WHERE recording_id = ?
		  AND status IN ('pending', 'running')
		  AND type IN (%s)`, inList), cascadeMsg, recordingID)
	return nil
}

// MarkJobPendingRetry returns a job to pending status for retry.
func (db *DB) MarkJobPendingRetry(id int64, errMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE jobs
		SET status = 'pending', finished_at = datetime('now'), error = ?
		WHERE id = ?`, errMsg, id)
	return err
}

// MarkJobPendingCanceled returns a job to pending without burning an attempt.
func (db *DB) MarkJobPendingCanceled(id int64, errMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE jobs
		SET status = 'pending',
		    attempt = CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END,
		    finished_at = NULL,
		    error = ?
		WHERE id = ?`, errMsg, id)
	return err
}

// UpdateJobProgress updates the progress JSON for a running job.
func (db *DB) UpdateJobProgress(id int64, progress string) error {
	_, err := db.conn.Exec(`UPDATE jobs SET progress = ? WHERE id = ?`, progress, id)
	return err
}

// ResetStuckJobs moves orphaned running jobs back to pending on startup.
func (db *DB) ResetStuckJobs() (int, error) {
	res, err := db.conn.Exec(`
		UPDATE jobs
		SET status = 'pending', error = 'reset after crash recovery'
		WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("resetting stuck jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// JobCounts holds job counts by status.
type JobCounts struct {
	Pending  int
	Running  int
	Complete int
	Failed   int
}

// CountJobs returns job counts grouped by status.
func (db *DB) CountJobs() (JobCounts, error) {
	rows, err := db.conn.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return JobCounts{}, fmt.Errorf("counting jobs: %w", err)
	}
	defer rows.Close()

	var counts JobCounts
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return counts, err
		}
		switch JobStatus(status) {
		case StatusPending:
			counts.Pending = n
		case StatusRunning:
			counts.Running = n
		case StatusComplete:
			counts.Complete = n
		case StatusFailed:
			counts.Failed = n
		}
	}
	return counts, rows.Err()
}

// ListRecentJobs returns the most recent jobs across all statuses.
func (db *DB) ListRecentJobs(limit int) ([]Job, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, type, status, attempt, max_retries,
		       COALESCE(error, ''), COALESCE(progress, ''), created_at,
		       COALESCE(started_at, ''), COALESCE(finished_at, '')
		FROM jobs
		ORDER BY id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		var jobType, status, createdAt, startedAt, finishedAt string

		if err := rows.Scan(&j.ID, &j.RecordingID, &jobType, &status,
			&j.Attempt, &j.MaxRetries, &j.Error, &j.Progress,
			&createdAt, &startedAt, &finishedAt); err != nil {
			return nil, err
		}

		j.Type = JobType(jobType)
		j.Status = JobStatus(status)
		j.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		if startedAt != "" {
			t, _ := time.Parse("2006-01-02 15:04:05", startedAt)
			j.StartedAt = &t
		}
		if finishedAt != "" {
			t, _ := time.Parse("2006-01-02 15:04:05", finishedAt)
			j.FinishedAt = &t
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
