package domain

type UpdateStatus string

const (
	StatusPending   UpdateStatus = "pending"
	StatusCompleted UpdateStatus = "completed"
	StatusFailed    UpdateStatus = "failed"
)
