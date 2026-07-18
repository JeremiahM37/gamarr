package db

// Job is the persisted state of a download job. Jobs are stored as JSON
// documents, so values are limited to the JSON scalar types (string, bool,
// float64, nil). Job's underlying type is a plain string map, which keeps it
// assignable from untyped map literals while giving callers typed accessors
// and named field keys instead of scattered string literals.
type Job map[string]interface{}

// Job field keys. Use these instead of raw strings when reading or writing
// job fields so typos fail at compile time.
const (
	JobStatus       = "status"
	JobTitle        = "title"
	JobPlatform     = "platform"
	JobPlatformSlug = "platform_slug"
	JobIsPC         = "is_pc"
	JobError        = "error"
	JobDetail       = "detail"
	JobRetryCount   = "retry_count"
	JobUpdatedAt    = "updated_at"
)

// Job status values.
const (
	StatusQueued               = "queued"
	StatusDownloading          = "downloading"
	StatusScanning             = "scanning"
	StatusOrganizing           = "organizing"
	StatusCompleted            = "completed"
	StatusCompletedUnorganized = "completed_unorganized"
	StatusError                = "error"
	StatusInterrupted          = "interrupted"
	StatusDeadLetter           = "dead_letter"
)

func (j Job) str(key string) string {
	v, _ := j[key].(string)
	return v
}

// Status returns the job's status ("" if unset).
func (j Job) Status() string { return j.str(JobStatus) }

// Title returns the job's title.
func (j Job) Title() string { return j.str(JobTitle) }

// Platform returns the job's platform display name.
func (j Job) Platform() string { return j.str(JobPlatform) }

// PlatformSlug returns the job's platform slug.
func (j Job) PlatformSlug() string { return j.str(JobPlatformSlug) }

// Error returns the job's error message ("" when the field is nil/unset).
func (j Job) Error() string { return j.str(JobError) }

// Detail returns the job's human-readable progress detail.
func (j Job) Detail() string { return j.str(JobDetail) }

// IsPC reports whether the job targets a PC game.
func (j Job) IsPC() bool {
	v, _ := j[JobIsPC].(bool)
	return v
}

// RetryCount returns the number of retries recorded on the job.
// JSON numbers decode as float64, hence the conversion.
func (j Job) RetryCount() int {
	v, _ := j[JobRetryCount].(float64)
	return int(v)
}

// IsFinished reports whether the job reached a terminal state.
func (j Job) IsFinished() bool {
	switch j.Status() {
	case StatusCompleted, StatusError, StatusInterrupted, StatusDeadLetter:
		return true
	}
	return false
}

// JobItem pairs a job with its store ID.
type JobItem struct {
	ID   string
	Data Job
}
